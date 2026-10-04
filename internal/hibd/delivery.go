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

type setupDelivery struct {
	engine engine.Engine
	cfg    config
	codex  *codex.Store
	logf   func(format string, args ...any)

	looking  chan struct{}
	written  map[string]delivered
	problems map[string]string
}

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
