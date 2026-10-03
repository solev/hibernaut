package hibd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
)

// loadSetup loads what hibd writes into machines: the vault's placeholders, and git's identity
// once GitHub is connected.
func loadSetup(cfg config) (machines.Setup, error) {
	block, err := loadSecretBlock(cfg.vaultPath)
	if err != nil {
		return machines.Setup{}, err
	}
	p, err := github.LoadProfile(cfg.githubProfile)
	if err != nil {
		return machines.Setup{}, err
	}
	s := machines.Setup{EnvBlock: block}
	if p != nil {
		s.Git = &machines.GitIdentity{Name: p.Name, Email: p.Email()}
	}
	return s, nil
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
	setup, err := loadSetup(cfg)
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

// setupDelivery writes the machine setup into each running machine whose copy is out of date:
// one that just woke, one a push missed, or every machine after the daemon starts.
type setupDelivery struct {
	engine   engine.Engine
	cfg      config
	logf     func(format string, args ...any)
	hashes   map[string][32]byte // the setup each machine has
	problems map[string]string
}

func newSetupDelivery(e engine.Engine, cfg config, logf func(format string, args ...any)) *setupDelivery {
	return &setupDelivery{engine: e, cfg: cfg, logf: logf, hashes: map[string][32]byte{}, problems: map[string]string{}}
}

func (d *setupDelivery) look(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	setup, err := loadSetup(d.cfg)
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
		if d.hashes[m.ID] == hash {
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
			d.hashes[m.ID] = hash
		}
		d.report("updating "+name, err)
	}
	for id := range d.hashes {
		if !running[id] {
			delete(d.hashes, id)
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
