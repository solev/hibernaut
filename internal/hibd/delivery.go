package hibd

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hostsblock"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/statefile"
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
	// Marking before and after the writes makes hibd daemon write every machine again, whether
	// this push finishes, is cut short or overlaps one of its passes.
	marked := markPush(cfg.stateDir)
	updateSetup(ctx, cfg, a.codexStore(cfg, a.warnf), links.NewStore(cfg.stateDir), a.engine, nil, warn)
	warn("telling hibd daemon about the update", cmp.Or(markPush(cfg.stateDir), marked))
}

func updateSetup(ctx context.Context, cfg config, store *codex.Store, linkStore links.Store, open func() (engine.Engine, error), written map[string]delivered, report func(string, error)) bool {
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
	naming := cfg.linksConfiguredOn()
	list := engine.ListOptions{States: []engine.State{engine.Running}}
	if naming {
		// A paused machine keeps its name in the block, so the list takes every state.
		list = engine.ListOptions{}
	}
	ms, err := e.List(ctx, list)
	if err != nil {
		report("listing machines", err)
		return false
	}
	if naming {
		hosts, err := hostsBlock(linkStore, ms)
		report("building the hosts block", err)
		setup.Hosts = hosts
		var running []engine.Machine
		for _, m := range ms {
			if m.State == engine.Running {
				running = append(running, m)
			}
		}
		ms = running
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

// hostsBlock renders the /etc/hosts block from links.json: NAME.hib for every machine in ms
// with a virtual address and a published port, the one reading it included. The block is the
// same for every machine. It returns an empty block when no machine has a published port,
// which removes the block, and nil when links are off, which leaves /etc/hosts alone. A name
// two machines share is left out rather than sent anywhere.
func hostsBlock(linkStore links.Store, ms []engine.Machine) ([]byte, error) {
	doc, err := linkStore.Read()
	if errors.Is(err, links.ErrLinksOff) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names, holders := map[string]string{}, map[string]int{}
	for _, m := range ms {
		if name := machines.Name(m); name != "" {
			names[m.ID] = name
			holders[name]++
		}
	}
	var hosts []hostsblock.Host
	for id, lm := range doc.Machines {
		name := names[id]
		if lm == nil || !lm.Addr.IsValid() || len(lm.Ports) == 0 || name == "" || holders[name] > 1 {
			continue
		}
		hosts = append(hosts, hostsblock.Host{IP: lm.Addr.String(), Name: name + ".hib"})
	}
	block, err := hostsblock.Render(hosts)
	if err != nil {
		return nil, err
	}
	if block == nil {
		return []byte{}, nil
	}
	return block, nil
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
	engine   engine.Engine
	cfg      config
	codex    *codex.Store
	links    links.Store
	logf     func(format string, args ...any)
	looking  chan struct{}
	written  map[string]delivered
	problems map[string]string
	// mark is the push mark the last pass began with.
	mark string
}

type delivered struct {
	hash    [32]byte
	started time.Time
}

func newSetupDelivery(e engine.Engine, cfg config, store *codex.Store, logf func(format string, args ...any)) *setupDelivery {
	return &setupDelivery{
		engine: e, cfg: cfg, codex: store, links: links.NewStore(cfg.stateDir), logf: logf,
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
	// A problem this pass reported stays reported, so the next pass doesn't log it again.
	reported := map[string]bool{}
	report := func(doing string, err error) {
		reported[doing] = true
		d.report(doing, err)
	}
	mark, err := readPushMark(d.cfg.stateDir)
	report("reading the push mark", err)
	if err != nil || mark != d.mark {
		// A push wrote machines since the last pass began, or may have.
		clear(d.written)
		d.mark = mark
	}
	if !updateSetup(ctx, d.cfg, d.codex, d.links, func() (engine.Engine, error) { return d.engine, nil }, d.written, report) {
		return
	}
	for key := range d.problems {
		if reported[key] || strings.HasPrefix(key, "updating ") {
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

// pushMark is a file in the state directory that every push replaces with a new value. hibd
// daemon skips a machine that holds what the daemon last wrote there. A push writes machines
// behind the daemon's back, so when the mark has changed since the daemon's last pass, the
// daemon writes every machine again.
const pushMark = "setup-pushed"

func markPush(stateDir string) error {
	d := statefile.Dir{Path: stateDir}
	root, owner, err := d.Root()
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := d.PublishOwned(root, pushMark, []byte(rand.Text()+"\n"), owner, true)
	if err != nil {
		return err
	}
	return f.Close()
}

// readPushMark reads the mark in stateDir, or "" when no push has left one. The value is only
// compared, but it is still read without following a link or waiting on a pipe.
func readPushMark(stateDir string) (string, error) {
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.OpenFile(pushMark, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s isn't a regular file", pushMark)
	}
	data, err := io.ReadAll(io.LimitReader(f, 64))
	return string(data), err
}
