package hibd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
)

// loadSetup loads what hibd writes into machines: the vault's placeholders, git's identity
// once GitHub is connected, and Codex's login file once Codex is.
func loadSetup(cfg config, store *codex.Store) (machines.Setup, error) {
	block, err := loadSecretBlock(cfg.vaultPath)
	if err != nil {
		return machines.Setup{}, err
	}
	p, err := github.LoadProfile(cfg.githubProfile)
	if err != nil {
		return machines.Setup{}, err
	}
	auth, err := codexAuthFile(store)
	if err != nil {
		return machines.Setup{}, err
	}
	s := machines.Setup{EnvBlock: block, CodexAuth: auth}
	if p != nil {
		s.Git = &machines.GitIdentity{Name: p.Name, Email: p.Email()}
	}
	return s, nil
}

// codexAuthFile returns the login file machines get, or nil while Codex isn't connected.
func codexAuthFile(store *codex.Store) ([]byte, error) {
	pub, err := store.Public()
	if errors.Is(err, codex.ErrNotConnected) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the codex login: %w", err)
	}
	return codex.RenderAuthFile(pub)
}

// pushSetup writes the machine setup into every running machine hibd made, after done, such
// as "secrets saved". It leaves machines alone while the host goes to sleep. Failures are
// warnings: the daemon tries again.
func (a *App) pushSetup(ctx context.Context, cfg config, done string) {
	if _, err := os.Stat(cfg.quiescing); err == nil {
		return
	}
	warn := func(doing string, err error) {
		if err != nil {
			fmt.Fprintf(a.Stderr, "warning: %s, but %s failed: %s\n", done, termsafe.String(doing), termsafe.String(err.Error()))
		}
	}
	updateSetup(ctx, cfg, a.codexStore(cfg, a.warnf), a.engine, nil, warn)
}

// updateSetup shares the setup and machine selection rules. A nil written map makes an
// immediate, sequential push. A cache makes only stale machines receive the setup, in parallel.
func updateSetup(ctx context.Context, cfg config, store *codex.Store, open func() (engine.Engine, error), written map[string]delivered, report func(string, error)) bool {
	setup, err := loadSetup(cfg, store)
	if err != nil {
		report("loading the machine setup", err)
		return false
	}
	e, err := open()
	if err != nil {
		report("listing machines", err)
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ms, err := e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	if err != nil {
		report("listing machines", err)
		return false
	}
	hash := setup.Hash()
	stale, running := setupTargets(ms, hash, written)
	writeSetups(ctx, e, setup, stale, hash, written, report)
	for id := range written {
		if !running[id] {
			delete(written, id)
		}
	}
	return true
}

func setupTargets(ms []engine.Machine, hash [32]byte, written map[string]delivered) ([]engine.Machine, map[string]bool) {
	running := map[string]bool{}
	var stale []engine.Machine
	for _, m := range ms {
		if machines.Name(m) == "" {
			continue
		}
		running[m.ID] = true
		if w, ok := written[m.ID]; ok && w.hash == hash && w.started.Equal(m.StartedAt) {
			continue
		}
		stale = append(stale, m)
	}
	return stale, running
}

func writeSetups(ctx context.Context, e engine.Engine, setup machines.Setup, ms []engine.Machine, hash [32]byte, written map[string]delivered, report func(string, error)) {
	if written == nil {
		for _, m := range ms {
			report("updating "+machines.Name(m), setup.Write(ctx, e, m.ID))
		}
		return
	}
	results := make([]error, len(ms))
	var wg sync.WaitGroup
	for i, m := range ms {
		wg.Go(func() { results[i] = setup.Write(ctx, e, m.ID) })
	}
	wg.Wait()
	for i, m := range ms {
		if results[i] == nil {
			written[m.ID] = delivered{hash: hash, started: m.StartedAt}
		}
		report("updating "+machines.Name(m), results[i])
	}
}

// setupDelivery writes the machine setup into each running machine whose copy may be out of
// date: one that resumed since its last write, one a push missed, or every machine after the
// daemon starts.
type setupDelivery struct {
	engine engine.Engine
	cfg    config
	codex  *codex.Store
	logf   func(format string, args ...any)
	// looking serializes loading the setup through recording complete writes.
	looking  chan struct{}
	written  map[string]delivered // by machine ID
	problems map[string]string
}

// delivered is the setup a running machine got, and when that run started. A resume starts a
// new run, which gets the setup again: the machine may have changed its copy, as codex logout
// does.
type delivered struct {
	hash    [32]byte
	started time.Time
}

func newSetupDelivery(e engine.Engine, cfg config, store *codex.Store, logf func(format string, args ...any)) *setupDelivery {
	return &setupDelivery{
		engine: e, cfg: cfg, codex: store, logf: logf,
		looking: make(chan struct{}, 1), written: map[string]delivered{}, problems: map[string]string{},
	}
}

func (d *setupDelivery) look(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case d.looking <- struct{}{}:
		defer func() { <-d.looking }()
	case <-ctx.Done():
		return
	}
	if !updateSetup(ctx, d.cfg, d.codex, func() (engine.Engine, error) { return d.engine, nil }, d.written, d.report) {
		return
	}
	for key := range d.problems {
		if strings.HasPrefix(key, "updating ") {
			continue
		}
		d.report(key, nil)
	}
}

func (d *setupDelivery) report(doing string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if d.problems[doing] == msg {
		return
	}
	d.problems[doing] = msg
	if err != nil {
		d.logf("%s: %v", doing, err)
	}
}
