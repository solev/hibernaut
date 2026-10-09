package hibd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
)

// fakeLinks is every links piece hibd's own commands use, recording what they did in one log
// so tests can check the order.
type fakeLinks struct {
	mu        sync.Mutex
	doc       links.Doc
	events    []string
	fenceErr  error
	readErr   error
	updateErr error
	closeErr  error
	addr      netip.Addr
	addrErr   error
	dropErr   error
	onUpdate  func()
}

func (f *fakeLinks) parts() linkParts {
	return linkParts{store: f, ctl: f, dropper: f, resolver: f}
}

func (f *fakeLinks) event(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fmt.Sprintf(format, args...))
}

func (f *fakeLinks) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

func (f *fakeLinks) entry(id string) (links.Machine, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.doc.Machines[id]
	if !ok {
		return links.Machine{}, false
	}
	return *m, true
}

func cloneDoc(d links.Doc) links.Doc {
	c := d
	if d.Machines != nil {
		c.Machines = make(map[string]*links.Machine, len(d.Machines))
		for id, m := range d.Machines {
			mc := *m
			mc.Ports = slices.Clone(m.Ports)
			mc.PortGen = maps.Clone(m.PortGen)
			if m.Fence != nil {
				fence := *m.Fence
				mc.Fence = &fence
			}
			c.Machines[id] = &mc
		}
	}
	return c
}

func (f *fakeLinks) Read() (links.Doc, error) {
	f.event("read")
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneDoc(f.doc), f.readErr
}

func (f *fakeLinks) Update(_ context.Context, update func(*links.Doc) error) error {
	if f.onUpdate != nil {
		f.onUpdate()
	}
	f.mu.Lock()
	if f.updateErr != nil {
		f.mu.Unlock()
		f.event("update failed")
		return f.updateErr
	}
	d := cloneDoc(f.doc)
	err := update(&d)
	if err == nil {
		f.doc = d
	}
	f.mu.Unlock()
	if err == nil {
		f.event("update")
	}
	return err
}

func (f *fakeLinks) Fence(_ context.Context, id, why string) (func(), error) {
	f.event("fence %s %s", id, why)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fenceErr != nil {
		return func() {}, f.fenceErr
	}
	f.doc.Entry(id).Fence = &links.Fence{Why: why}
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			if m := f.doc.Machines[id]; m != nil {
				m.Fence = nil
			}
			f.mu.Unlock()
			f.event("release %s", id)
		})
	}, nil
}

func (f *fakeLinks) Close(_ context.Context, id string) (int, error) {
	f.event("close %s", id)
	return 0, f.closeErr
}

func (f *fakeLinks) Revoke(context.Context, string, []int) (int, error) {
	return 0, errors.New("unexpected revoke")
}

func (f *fakeLinks) Stat(context.Context) (links.Stats, error) {
	return links.Stats{}, errors.New("unexpected stat")
}

func (f *fakeLinks) Drop(addr netip.Addr) error {
	f.event("drop %s", addr)
	return f.dropErr
}

func (f *fakeLinks) Addr(id string) (netip.Addr, links.Run, error) {
	f.event("addr %s", id)
	return f.addr, "run", f.addrErr
}

func (f *fakeLinks) At(netip.Addr) (links.Slot, error) {
	return links.Slot{}, errors.New("unexpected At")
}

// withLinks gives a the fake links pieces, and turns links on when on is set. With links off
// the pieces are still there, so a test can show nothing reaches them.
func withLinks(a *App, f *fakeLinks, on bool) {
	a.linkParts = f.parts()
	if !on {
		return
	}
	getenv := a.Getenv
	a.Getenv = func(k string) string {
		if k == "HIBD_LINK_PORT" {
			return "5444"
		}
		return getenv(k)
	}
}

// pauseLogged is an engine that logs each pause of a running machine into f before it happens,
// and fails it with fail when that's set.
func pauseLogged(f *fakeLinks, fail error, opts ...fake.Option) *fake.Engine {
	return fake.New(append(opts, fake.WithPauseHook(func(_ context.Context, id string) error {
		f.event("pause %s", id)
		return fail
	}))...)
}

func addApp(e *fake.Engine, id, name string, state engine.State) {
	e.Add(engine.Machine{ID: id, State: state, Metadata: map[string]string{"hib.name": name, "hib.image": "dev"}})
}

func TestUsageAndVersionAdvertisePorts(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	if code := a.Run(t.Context(), []string{"help"}); code != 0 {
		t.Fatalf("help exit %d", code)
	}
	for _, want := range []string{"port add NAME PORT", "port rm NAME PORT", "port ls", "fork, port, image"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage lacks %q:\n%s", want, out)
		}
	}
	code, r, err := runJSON(t, a, out, "version", "-json")
	if code != 0 || err != nil || !slices.Contains(r.Features, hibapi.FeaturePorts) {
		t.Fatalf("version -json = %d, %+v, %v", code, r, err)
	}
}

func TestLsShowsPublishedPorts(t *testing.T) {
	newLs := func(t *testing.T, on bool) (*App, *fakeLinks, *bytes.Buffer, *bytes.Buffer) {
		e := fake.New()
		addApp(e, "app1", "app", engine.Running)
		addApp(e, "db1", "db", engine.Paused)
		f := &fakeLinks{doc: links.Doc{Machines: map[string]*links.Machine{
			"app1": {Addr: netip.MustParseAddr("10.11.0.2"), Ports: []int{3000, 8080}},
		}}}
		a, out, errOut := newApp(t, e)
		withLinks(a, f, on)
		return a, f, out, errOut
	}
	t.Run("links on", func(t *testing.T) {
		a, _, out, _ := newLs(t, true)
		if code := a.Run(t.Context(), []string{"ls"}); code != 0 {
			t.Fatalf("ls exit %d", code)
		}
		ports := map[string]string{}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if f := strings.Fields(lines[0]); len(f) < 7 || f[6] != "PORTS" {
			t.Fatalf("header = %q, want PORTS after IDLE", lines[0])
		}
		for _, l := range lines[1:] {
			f := strings.Fields(l)
			ports[f[1]] = f[6]
		}
		if ports["app1"] != "3000,8080" || ports["db1"] != "-" {
			t.Errorf("PORTS = %v; ls =\n%s", ports, out)
		}

		code, r, err := runJSON(t, a, out, "ls", "-json")
		if code != 0 || err != nil {
			t.Fatalf("ls -json = %d, %v", code, err)
		}
		got := map[string][]int{}
		for _, m := range r.Machines {
			got[m.ID] = m.Ports
		}
		if !slices.Equal(got["app1"], []int{3000, 8080}) || got["db1"] == nil || len(got["db1"]) != 0 {
			t.Errorf("ports = %#v, want app1's and an empty list for db1", got)
		}
	})
	t.Run("links off", func(t *testing.T) {
		a, f, out, _ := newLs(t, false)
		if code := a.Run(t.Context(), []string{"ls"}); code != 0 {
			t.Fatalf("ls exit %d", code)
		}
		if strings.Contains(out.String(), "PORTS") {
			t.Errorf("ls shows PORTS with links off:\n%s", out)
		}
		code, r, err := runJSON(t, a, out, "ls", "-json")
		if code != 0 || err != nil {
			t.Fatalf("ls -json = %d, %v", code, err)
		}
		for _, m := range r.Machines {
			if m.Ports != nil {
				t.Errorf("%s has ports %v with links off", m.ID, m.Ports)
			}
		}
		if log := f.log(); len(log) != 0 {
			t.Errorf("ls touched the link state with links off: %v", log)
		}
	})
	t.Run("unreadable state", func(t *testing.T) {
		a, f, out, errOut := newLs(t, true)
		f.readErr = errors.New("links.json is from a newer hibd")
		code, r, err := runJSON(t, a, out, "ls", "-json")
		if code != 0 || err != nil || len(r.Machines) != 2 {
			t.Fatalf("ls -json = %d, %+v, %v", code, r, err)
		}
		if !strings.Contains(errOut.String(), "newer hibd") {
			t.Errorf("stderr = %q, want a warning", errOut)
		}
	})
}

func TestPauseWithLinksOn(t *testing.T) {
	addr := netip.MustParseAddr("10.0.0.7")
	eperm := fmt.Errorf("delete element: %w", syscall.EPERM)
	for name, tc := range map[string]struct {
		fenceErr, closeErr, addrErr, dropErr, pauseErr, updateErr error
		wantCode                                                  int
		wantEvents                                                []string
		wantPaused, wantMarked                                    bool
		wantStderr                                                string
	}{
		"acknowledged": {
			wantEvents: []string{"fence app1 pause", "close app1", "pause app1", "update", "release app1"},
			wantPaused: true, wantMarked: true,
		},
		"no ack drops the gates": {
			closeErr:   errors.New("dial unix: no such file"),
			wantEvents: []string{"fence app1 pause", "close app1", "addr app1", "drop 10.0.0.7", "pause app1", "update", "release app1"},
			wantPaused: true, wantMarked: true,
		},
		"no ack and not root fails closed": {
			closeErr: errors.New("timed out"), dropErr: eperm, wantCode: 1,
			wantEvents: []string{"fence app1 pause", "close app1", "addr app1", "drop 10.0.0.7", "release app1"},
			wantStderr: "Run it as root: sudo hibd pause app",
		},
		"no ack and no gates found fails closed": {
			closeErr: errors.New("timed out"), addrErr: errors.New("cgroup unreadable"), wantCode: 1,
			wantEvents: []string{"fence app1 pause", "close app1", "addr app1", "release app1"},
			wantStderr: "cgroup unreadable",
		},
		"failed pause clears the fence": {
			pauseErr: errors.New("orchestrator unreachable"), wantCode: 1,
			wantEvents: []string{"fence app1 pause", "close app1", "pause app1", "release app1"},
			wantStderr: "orchestrator unreachable",
		},
		"no fence, no pause": {
			fenceErr: errors.New("links.lock: permission denied"), wantCode: 1,
			wantEvents: []string{"fence app1 pause"},
			wantStderr: "left app as it was",
		},
		"mark not saved": {
			updateErr: errors.New("disk full"), wantCode: 1,
			wantEvents: []string{"fence app1 pause", "close app1", "pause app1", "update failed", "release app1"},
			wantPaused: true, wantStderr: "paused app, but couldn't mark it",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeLinks{fenceErr: tc.fenceErr, closeErr: tc.closeErr, addr: addr, addrErr: tc.addrErr, dropErr: tc.dropErr, updateErr: tc.updateErr}
			e := pauseLogged(f, tc.pauseErr)
			addApp(e, "app1", "app", engine.Running)
			a, _, errOut := newApp(t, e)
			withLinks(a, f, true)
			if code := a.Run(t.Context(), []string{"pause", "app"}); code != tc.wantCode {
				t.Fatalf("exit %d, want %d; stderr %s", code, tc.wantCode, errOut)
			}
			if got := f.log(); !slices.Equal(got, tc.wantEvents) {
				t.Errorf("events = %q, want %q", got, tc.wantEvents)
			}
			m, err := e.Get(t.Context(), "app1")
			if err != nil {
				t.Fatal(err)
			}
			if paused := m.State == engine.Paused; paused != tc.wantPaused {
				t.Errorf("state = %s, want paused %t", m.State, tc.wantPaused)
			}
			entry, _ := f.entry("app1")
			if entry.PausedByHand != tc.wantMarked || entry.Fence != nil {
				t.Errorf("entry = %+v, want marked %t and no fence", entry, tc.wantMarked)
			}
			if !strings.Contains(errOut.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want %q", errOut, tc.wantStderr)
			}
		})
	}
}

func TestRmWithLinksOnDropsTheEntry(t *testing.T) {
	f := &fakeLinks{doc: links.Doc{
		Machines: map[string]*links.Machine{
			"app1": {Addr: netip.MustParseAddr("10.11.0.2"), Ports: []int{80}, PortGen: map[int]uint64{80: 1}},
			"db1":  {Addr: netip.MustParseAddr("10.11.0.3"), Ports: []int{5432}},
		},
		Next: netip.MustParseAddr("10.11.0.4"),
	}}
	e := pauseLogged(f, nil)
	addApp(e, "app1", "app", engine.Running)
	a, out, errOut := newApp(t, e)
	withLinks(a, f, true)
	if code, _, err := runJSON(t, a, out, "rm", "app", "-json"); code != 0 || err != nil {
		t.Fatalf("rm = %d, %v; stderr %s", code, err, errOut)
	}
	if _, err := e.Get(t.Context(), "app1"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("app1 still exists: %v", err)
	}
	want := []string{"fence app1 rm", "close app1", "release app1", "update"}
	if got := f.log(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
	if _, ok := f.entry("app1"); ok {
		t.Error("rm left app1's entry")
	}
	if _, ok := f.entry("db1"); !ok || f.doc.Next != netip.MustParseAddr("10.11.0.4") {
		t.Errorf("rm touched more than app1's entry: %+v", f.doc)
	}
}

func TestPauseAllWithLinksOnMakesTheMarkerAndFences(t *testing.T) {
	for _, tc := range []struct {
		name        string
		quiescing   bool
		stuck       bool
		wantCode    int
		wantMarker  bool
		wantEvents1 []string
	}{
		{name: "pauses", wantMarker: true},
		{name: "fails and removes its marker", stuck: true, wantCode: 1},
		{name: "fails and keeps quiesce's marker", quiescing: true, stuck: true, wantCode: 1, wantMarker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeLinks{}
			var a *App
			e := fake.New(fake.WithPauseHook(func(ctx context.Context, id string) error {
				if _, err := os.Stat(a.Getenv("HIBD_QUIESCING")); err != nil {
					t.Errorf("paused %s without the quiescing marker: %v", id, err)
				}
				f.event("pause %s", id)
				if tc.stuck {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}))
			e.Add(engine.Machine{ID: "run1", State: engine.Running})
			e.Add(engine.Machine{ID: "run2", State: engine.Running})
			var errOut *bytes.Buffer
			a, _, errOut = newApp(t, e)
			withLinks(a, f, true)
			marker := a.Getenv("HIBD_QUIESCING")
			if tc.quiescing {
				if err := os.WriteFile(marker, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"pause-all"}
			if tc.stuck {
				args = append(args, "-timeout", "50ms")
			}
			if code := a.Run(t.Context(), args); code != tc.wantCode {
				t.Fatalf("exit %d, want %d: %s", code, tc.wantCode, errOut)
			}
			if _, err := os.Stat(marker); (err == nil) != tc.wantMarker {
				t.Errorf("marker: %v, want present %t", err, tc.wantMarker)
			}
			log := f.log()
			for _, id := range []string{"run1", "run2"} {
				fence := slices.Index(log, "fence "+id+" pause-all")
				pause := slices.Index(log, "pause "+id)
				if fence < 0 || pause < fence || slices.Index(log, "close "+id) > pause || !slices.Contains(log, "release "+id) {
					t.Errorf("%s not paused through the fence: %q", id, log)
				}
			}
		})
	}
}

func TestLeakscanFencesItsPauseWithLinksOn(t *testing.T) {
	t.Run("machine", func(t *testing.T) {
		f := &fakeLinks{}
		e := pauseLogged(f, nil)
		e.Add(engine.Machine{ID: "sandbox123", State: engine.Running, Metadata: map[string]string{"hib.name": "app", "hib.image": "dev"}})
		a, out, errOut, cfg := newLeakscanApp(t, e)
		withLinks(a, f, true)
		writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), []byte("clean disk"))
		writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
		a.Query = oneBuildQuery(leakBuildA)
		if code := a.Run(t.Context(), []string{"leakscan", "app"}); code != 0 {
			t.Fatalf("exit %d; stdout %q stderr %q", code, out, errOut)
		}
		want := []string{"fence sandbox123 leak scan", "close sandbox123", "pause sandbox123", "release sandbox123"}
		if got := f.log(); !slices.Equal(got, want) {
			t.Errorf("events = %q, want %q", got, want)
		}
	})
	t.Run("canary", func(t *testing.T) {
		f := &fakeLinks{}
		canary := "hibcanary_0123456789abcdef0123456789abcdef"
		var cfg config
		e := fake.New(
			fake.WithTemplates(images.Template(images.Default)),
			fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
				return engine.ExecResult{}, nil
			}),
			fake.WithPauseHook(func(_ context.Context, id string) error {
				f.event("pause %s", id)
				writeLeakBuild(t, cfg.e2bStorage, leakBuildB, []byte("mem "+canary), []byte("disk "+canary))
				return nil
			}),
		)
		a, out, errOut, c := newLeakscanApp(t, e)
		cfg = c
		withLinks(a, f, true)
		a.NewCanary = func() (string, error) { return canary, nil }
		a.Query = oneBuildQuery(leakBuildB)
		if code := a.Run(t.Context(), []string{"leakscan", "-canary"}); code != 0 {
			t.Fatalf("exit %d; stdout %q stderr %q", code, out, errOut)
		}
		log := f.log()
		if len(log) != 4 || !strings.HasPrefix(log[0], "fence ") || !strings.HasSuffix(log[0], " leak scan") ||
			!strings.HasPrefix(log[1], "close ") || !strings.HasPrefix(log[2], "pause ") || !strings.HasPrefix(log[3], "release ") {
			t.Errorf("events = %q, want the canary's pause fenced", log)
		}
	})
}

func TestBridgeClearsPausedByHand(t *testing.T) {
	far := make(chan net.Conn, 1)
	e := newEngine(newKey(t), fake.WithDialer(func(context.Context, engine.Machine, int) (net.Conn, error) {
		a, b := net.Pipe()
		far <- b
		return a, nil
	}))
	a, _, errOut := newApp(t, e)
	if code := a.Run(t.Context(), []string{"new", "app"}); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	id := mustID(t, e, "app")
	f := &fakeLinks{}
	withLinks(a, f, true)
	if code := a.Run(t.Context(), []string{"pause", "app"}); code != 0 {
		t.Fatalf("pause: %s", errOut)
	}
	if entry, _ := f.entry(id); !entry.PausedByHand {
		t.Fatalf("pause didn't mark %s: %+v", id, entry)
	}

	stdinR, stdinW := io.Pipe()
	outR, outW := io.Pipe()
	defer stdinW.Close()
	a.Stdin, a.Stdout = stdinR, outW
	done := make(chan int, 1)
	go func() { done <- a.Run(t.Context(), []string{"bridge", "app"}); outW.Close() }()
	if _, err := hibapi.ReadBridgeHeader(bufio.NewReaderSize(outR, hibapi.MaxHeader)); err != nil {
		t.Fatalf("header: %v (stderr %s)", err, errOut)
	}
	if entry, _ := f.entry(id); entry.PausedByHand {
		t.Errorf("bridge left %s paused by hand: %+v", id, entry)
	}
	(<-far).Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("bridge exit %d: %s", code, errOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge didn't end")
	}
}

func TestLinksOffKeepsM11Pauses(t *testing.T) {
	f := &fakeLinks{}
	e := pauseLogged(f, nil)
	addApp(e, "app1", "app", engine.Running)
	addApp(e, "db1", "db", engine.Running)
	e.Add(engine.Machine{ID: "run1", State: engine.Running})
	a, _, errOut := newApp(t, e)
	withLinks(a, f, false)
	for _, args := range [][]string{{"pause", "app"}, {"rm", "db"}, {"pause-all"}} {
		if code := a.Run(t.Context(), args); code != 0 {
			t.Fatalf("%q exit %d: %s", args, code, errOut)
		}
	}
	if _, err := os.Stat(a.Getenv("HIBD_QUIESCING")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pause-all made the quiescing marker with links off: %v", err)
	}

	le := pauseLogged(f, nil)
	le.Add(engine.Machine{ID: "sandbox123", State: engine.Running, Metadata: map[string]string{"hib.name": "app", "hib.image": "dev"}})
	la, out, lerrOut, cfg := newLeakscanApp(t, le)
	withLinks(la, f, false)
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), []byte("clean disk"))
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
	la.Query = oneBuildQuery(leakBuildA)
	if code := la.Run(t.Context(), []string{"leakscan", "app"}); code != 0 {
		t.Fatalf("leakscan exit %d; stdout %q stderr %q", code, out, lerrOut)
	}

	for _, ev := range f.log() {
		if !strings.HasPrefix(ev, "pause ") {
			t.Errorf("links off reached the links pieces: %q", f.log())
			break
		}
	}
	if len(f.doc.Machines) != 0 {
		t.Errorf("links off wrote link state: %+v", f.doc)
	}
}

// getEngine is a fake engine whose Get finds machines its listing misses, as a machine E2B pauses
// while cleanup lists can be, and fails for others.
type getEngine struct {
	*fake.Engine
	unlisted map[string]engine.Machine
	getErr   map[string]error

	mu   sync.Mutex
	gets []string
}

func (g *getEngine) Get(ctx context.Context, id string) (engine.Machine, error) {
	g.mu.Lock()
	g.gets = append(g.gets, id)
	g.mu.Unlock()
	if m, ok := g.unlisted[id]; ok {
		return m, nil
	}
	if err, ok := g.getErr[id]; ok {
		return engine.Machine{}, err
	}
	return g.Engine.Get(ctx, id)
}

func TestCleanupPrunesForgottenMachinesOutsideTheLock(t *testing.T) {
	unavailable := errors.New("e2b: 503 service unavailable")
	for _, tc := range []struct {
		name     string
		on       bool
		dryRun   bool
		wantGets []string
		wantErr  string
	}{
		{
			name: "links on", on: true,
			wantGets: []string{"flaky1", "gone1", "gone2", "gone4", "moving1"},
			wantErr:  "kept the link entry and activity clock of flaky1: couldn't confirm E2B has forgotten it: " + unavailable.Error(),
		},
		{
			name: "links on, dry run", on: true, dryRun: true,
			wantGets: []string{"flaky1", "gone1", "gone2", "gone4", "moving1"},
			wantErr:  "kept the link entry and activity clock of flaky1: couldn't confirm E2B has forgotten it: " + unavailable.Error(),
		},
		{
			name:     "links off",
			wantGets: []string{"flaky1", "gone1", "gone2", "moving1"},
			wantErr:  "kept the activity clock of flaky1: couldn't confirm E2B has forgotten it: " + unavailable.Error(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fe := fake.New()
			fe.Add(engine.Machine{ID: "live1", State: engine.Running})
			fe.Add(engine.Machine{ID: "live2", State: engine.Paused})
			e := &getEngine{
				Engine:   fe,
				unlisted: map[string]engine.Machine{"moving1": {ID: "moving1", State: engine.Paused}},
				getErr:   map[string]error{"flaky1": unavailable},
			}
			a, _, _ := newApp(t, fe)
			a.Clean = nil
			a.Query = func(context.Context, string) ([][]string, error) { return nil, nil }
			f := &fakeLinks{doc: links.Doc{
				Machines: map[string]*links.Machine{
					"live1":   {Addr: netip.MustParseAddr("10.11.0.2"), Ports: []int{80}},
					"live2":   {PausedByHand: true},
					"gone1":   {Addr: netip.MustParseAddr("10.11.0.3"), Ports: []int{5432}},
					"moving1": {Addr: netip.MustParseAddr("10.11.0.4"), Ports: []int{8080}},
					"flaky1":  {Addr: netip.MustParseAddr("10.11.0.5"), Ports: []int{9000}},
					"gone4":   {Addr: netip.MustParseAddr("10.11.0.6")},
				},
				Next: netip.MustParseAddr("10.11.0.7"),
			}}
			withLinks(a, f, tc.on)
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			cfg.e2bStorage, cfg.trimScope = t.TempDir(), "off"
			f.onUpdate = func() {
				unlock, ok, err := tryLock(cfg.stateDir, "cleanup.lock", "cleanup")
				if err != nil || !ok {
					t.Errorf("link state updated under the cleanup lock: %v", err)
					return
				}
				unlock()
			}
			activity := filepath.Join(cfg.stateDir, filepath.FromSlash(links.ActivityDir))
			if err := os.MkdirAll(activity, 0o700); err != nil {
				t.Fatal(err)
			}
			allFiles := []string{".touch-gone3", "flaky1", "gone1", "gone2", "live1", "moving1"}
			for _, name := range allFiles {
				if err := os.WriteFile(filepath.Join(activity, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			stateFile := filepath.Join(cfg.stateDir, links.StateName)
			if err := os.WriteFile(stateFile, []byte(`{"v":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var logs []string
			err = a.clean(t.Context(), t.Context(), cfg, e, time.Now(), tc.dryRun, false, func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			})
			if err == nil || !errors.Is(err, unavailable) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("cleanup = %v, want it to say %q", err, tc.wantErr)
			}
			if got := slices.Sorted(slices.Values(e.gets)); !slices.Equal(got, tc.wantGets) {
				t.Errorf("Get asked about %q, want each unlisted machine once: %q", got, tc.wantGets)
			}

			wantFiles := allFiles
			if !tc.dryRun {
				wantFiles = []string{"flaky1", "live1", "moving1"}
			}
			if got := dirEntries(t, activity); !slices.Equal(got, wantFiles) {
				t.Errorf("activity = %q, want %q", got, wantFiles)
			}
			wantEntries := []string{"flaky1", "gone1", "gone4", "live1", "live2", "moving1"}
			if tc.on && !tc.dryRun {
				wantEntries = []string{"flaky1", "live1", "live2", "moving1"}
			}
			if got := slices.Sorted(maps.Keys(f.doc.Machines)); !slices.Equal(got, wantEntries) {
				t.Errorf("link entries = %q, want %q", got, wantEntries)
			}
			if f.doc.Next != netip.MustParseAddr("10.11.0.7") {
				t.Errorf("cleanup gave addresses back: %+v", f.doc)
			}
			if !tc.on && len(f.log()) != 0 {
				t.Errorf("links off reached the link state: %q", f.log())
			}
			if data, err := os.ReadFile(stateFile); err != nil || string(data) != `{"v":1}` {
				t.Errorf("links.json = %q, %v", data, err)
			}

			text := strings.Join(logs, "\n")
			verb := "removed"
			if tc.dryRun {
				verb = "would remove"
			}
			want := []string{
				verb + " the activity clock of gone1, which E2B has forgotten",
				verb + " the activity clock of gone2, which E2B has forgotten",
				verb + " .touch-gone3",
			}
			if tc.on {
				drop := "dropped"
				if tc.dryRun {
					drop = "would drop"
				}
				want = append(want, drop+" the link entry of gone1", drop+" the link entry of gone4")
			}
			for _, w := range want {
				if !strings.Contains(text, w) {
					t.Errorf("logs lack %q:\n%s", w, text)
				}
			}
			for _, kept := range []string{"live1", "live2", "moving1", "flaky1"} {
				if strings.Contains(text, kept) {
					t.Errorf("logs mention %s, which cleanup keeps:\n%s", kept, text)
				}
			}
		})
	}
}
