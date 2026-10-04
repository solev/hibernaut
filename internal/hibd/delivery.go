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
		fmt.Fprintf(a.Stderr, "warning: %s, but %s failed: %s\n", done, termsafe.String(doing), termsafe.String(err.Error()))
	}
	setup, err := loadSetup(cfg, a.codexStore(cfg, a.warnf))
	if err != nil {
		warn("loading the machine setup", err)
		return
	}
	e, err := a.engine()
	if err != nil {
		warn("listing machines", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ms, err := e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	if err != nil {
		warn("listing machines", err)
		return
	}
	for _, m := range ms {
		name := machines.Name(m)
		if name == "" {
			continue
		}
		if err := setup.Write(ctx, e, m.ID); err != nil {
			warn("updating "+name, err)
		}
	}
}

// setupDelivery writes the machine setup into each running machine whose copy may be out of
// date: one that resumed since its last write, one a push missed, or every machine after the
// daemon starts.
type setupDelivery struct {
	engine   engine.Engine
	cfg      config
	codex    *codex.Store
	logf     func(format string, args ...any)
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
	return &setupDelivery{engine: e, cfg: cfg, codex: store, logf: logf, written: map[string]delivered{}, problems: map[string]string{}}
}

func (d *setupDelivery) look(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	setup, err := loadSetup(d.cfg, d.codex)
	if err != nil {
		d.report("loading the machine setup", err)
		return
	}
	hash := setup.Hash()
	ms, err := d.engine.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	if err != nil {
		d.report("listing machines", err)
		return
	}
	running := map[string]bool{}
	var stale []engine.Machine
	var wg sync.WaitGroup
	for _, m := range ms {
		if machines.Name(m) == "" {
			continue
		}
		running[m.ID] = true
		if w, ok := d.written[m.ID]; ok && w.hash == hash && w.started.Equal(m.StartedAt) {
			continue
		}
		stale = append(stale, m)
	}
	results := make([]error, len(stale))
	for i, m := range stale {
		wg.Go(func() {
			results[i] = setup.Write(ctx, d.engine, m.ID)
		})
	}
	wg.Wait()
	for i, m := range stale {
		name := machines.Name(m)
		err := results[i]
		if err == nil {
			d.written[m.ID] = delivered{hash: hash, started: m.StartedAt}
		}
		d.report("updating "+name, err)
	}
	for id := range d.written {
		if !running[id] {
			delete(d.written, id)
		}
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
