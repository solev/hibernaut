package busy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/busy"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

var t0 = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// world is a fake host: an engine, the CPU time each machine has used, and the command lines
// of the processes running in each machine.
type world struct {
	t *testing.T
	e *fake.Engine
	w *busy.Watcher

	mu     sync.Mutex
	now    time.Time
	cpu    map[string]time.Duration
	cpuErr map[string]error
	onCPU  func(id string)
	procs  map[string][]string
	execs  []string
	reads  []string
	logs   []string
}

var codex = []string{"/sbin/init", "sshd: dev [priv]", "-bash", "node /usr/local/bin/codex"}

func newWorld(t *testing.T, agents ...string) *world {
	t.Helper()
	v := &world{t: t, now: t0, cpu: map[string]time.Duration{}, cpuErr: map[string]error{}, procs: map[string][]string{}}
	v.e = fake.New(fake.WithClock(v.Now), fake.WithExec(v.exec))
	v.w = v.watcher(v.e, agents...)
	return v
}

// watcher returns a watcher of e on this world's clock, CPU counters and log.
func (v *world) watcher(e engine.Engine, agents ...string) *busy.Watcher {
	v.t.Helper()
	w, err := busy.New(busy.Config{Engine: e, CPU: v.readCPU, Agents: agents, Now: v.Now, Logf: v.logf})
	if err != nil {
		v.t.Fatal(err)
	}
	return w
}

func (v *world) Now() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.now
}

// later moves the clock on by d.
func (v *world) later(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.now = v.now.Add(d)
}

// use makes machine id use d more CPU time.
func (v *world) use(id string, d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cpu[id] += d
}

func (v *world) readCPU(id string) (time.Duration, error) {
	v.mu.Lock()
	v.reads = append(v.reads, id)
	hook := v.onCPU
	used, err := v.cpu[id], v.cpuErr[id]
	v.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return used, err
}

// exec plays pgrep -f in the machine: Go's regexps are close enough to POSIX extended ones.
func (v *world) exec(_ context.Context, m engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.execs = append(v.execs, m.ID)
	if req.Cmd != "pgrep" || req.User != "root" || len(req.Args) != 2 || req.Args[0] != "-f" {
		v.t.Errorf("exec %+v, want pgrep -f PATTERN as root", req)
		return engine.ExecResult{ExitCode: 2}, nil
	}
	re, err := regexp.Compile(req.Args[1])
	if err != nil {
		return engine.ExecResult{ExitCode: 2, Stderr: []byte(err.Error())}, nil
	}
	for _, p := range v.procs[m.ID] {
		if re.MatchString(p) {
			return engine.ExecResult{ExitCode: 0}, nil
		}
	}
	return engine.ExecResult{ExitCode: 1}, nil
}

func (v *world) logf(format string, args ...any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.logs = append(v.logs, fmt.Sprintf(format, args...))
}

// machine adds a running machine hibd made, called name, with idle time idle ("" for none
// kept), that pauses in left.
func (v *world) machine(name, idle string, left time.Duration) string {
	md := map[string]string{"hib.name": name, "hib.image": "dev"}
	if idle != "" {
		md["hib.idle"] = idle
	}
	id := "id" + name
	v.e.Add(engine.Machine{ID: id, State: engine.Running, StartedAt: v.Now(), EndAt: v.Now().Add(left), Metadata: md})
	return id
}

func (v *world) tick() {
	v.t.Helper()
	if err := v.w.Tick(v.t.Context()); err != nil {
		v.t.Fatalf("Tick: %v", err)
	}
}

func (v *world) get(id string) engine.Machine {
	v.t.Helper()
	m, err := v.e.Get(v.t.Context(), id)
	if err != nil {
		v.t.Fatal(err)
	}
	return m
}

func (v *world) endAt(id string) time.Time { return v.get(id).EndAt }

// count returns how often id is in ids.
func (v *world) count(ids *[]string, id string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, x := range *ids {
		if x == id {
			n++
		}
	}
	return n
}

// logged returns how many log lines contain s.
func (v *world) logged(s string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, l := range v.logs {
		if strings.Contains(l, s) {
			n++
		}
	}
	return n
}

func TestCPUKeepsMachineRunning(t *testing.T) {
	v := newWorld(t)
	id := v.machine("app", "10m0s", time.Minute)
	v.tick() // The first look only takes a reading.
	v.later(15 * time.Second)
	v.use(id, 3*time.Second) // 20% of a CPU
	v.tick()
	if got, want := v.endAt(id), v.Now().Add(10*time.Minute); !got.Equal(want) {
		t.Errorf("EndAt = %v, want the machine's idle time from now, %v", got, want)
	}
	if n := v.count(&v.execs, id); n != 1 {
		t.Errorf("looked for agents %d times, want once: not when the CPU says it's busy", n)
	}
}

func TestIdleMachineLeftAlone(t *testing.T) {
	v := newWorld(t)
	id := v.machine("app", "10m0s", time.Minute)
	v.procs[id] = []string{"/sbin/init", "sshd: dev [priv]", "tmux", "vim codex.md", "less notes/copilot.txt", "codexx"}
	end := v.endAt(id)
	for range 4 {
		v.tick()
		v.later(15 * time.Second)
		v.use(id, 50*time.Millisecond)
	}
	if got := v.endAt(id); !got.Equal(end) {
		t.Errorf("EndAt = %v, want an idle machine left to pause at %v", got, end)
	}
}

func TestAgentKeepsMachineRunning(t *testing.T) {
	for _, tc := range []struct {
		agents []string
		cmd    string
		busy   bool
	}{
		{nil, "node /usr/local/bin/codex", true},
		{nil, "/root/.npm/vendor/x86_64-unknown-linux-musl/codex/codex exec fix the tests", true},
		{nil, "node /usr/local/bin/copilot --banner", true},
		{nil, "claude", true},
		{nil, "codex 600", true},
		{nil, "bash", false},
		{[]string{"aider", "my.agent"}, "python3 -m aider", true},
		{[]string{"aider", "my.agent"}, "/opt/my.agent", true},
		{[]string{"aider", "my.agent"}, "/opt/myXagent", false},
		{[]string{"aider", "my.agent"}, "node /usr/local/bin/codex", false},
	} {
		v := newWorld(t, tc.agents...)
		id := v.machine("app", "10m0s", time.Minute)
		v.procs[id] = []string{"/sbin/init", tc.cmd}
		v.tick()
		want := v.Now().Add(time.Minute)
		if tc.busy {
			want = v.Now().Add(10 * time.Minute)
		}
		if got := v.endAt(id); !got.Equal(want) {
			t.Errorf("agents %q, running %q: EndAt = %v, want %v", tc.agents, tc.cmd, got, want)
		}
	}
}

func TestBadConfig(t *testing.T) {
	cpu := func(string) (time.Duration, error) { return 0, nil }
	for _, agents := range [][]string{{""}, {"a b"}, {"x|y"}, {"(.*)"}, {"codex", "c*"}} {
		if _, err := busy.New(busy.Config{Engine: fake.New(), CPU: cpu, Agents: agents}); err == nil {
			t.Errorf("New with agents %q = nil error, want one", agents)
		}
	}
	if _, err := busy.New(busy.Config{CPU: cpu}); err == nil {
		t.Error("New with no engine = nil error, want one")
	}
	if _, err := busy.New(busy.Config{Engine: fake.New()}); err == nil {
		t.Error("New with no CPU counter = nil error, want one")
	}
}

func TestLeavesOtherMachinesAlone(t *testing.T) {
	v := newWorld(t)
	never := v.machine("forever", "0s", time.Minute)
	v.e.Add(engine.Machine{ID: "e2etest", State: engine.Running, StartedAt: t0, EndAt: t0.Add(time.Minute), Metadata: map[string]string{"hib-test": "e2e"}})
	v.e.Add(engine.Machine{ID: "badname", State: engine.Running, StartedAt: t0, EndAt: t0.Add(time.Minute), Metadata: map[string]string{"hib.name": "Bad Name"}})
	others := []string{never, "e2etest", "badname"}
	for _, id := range others {
		v.procs[id] = codex
	}
	for range 3 {
		v.tick()
		v.later(15 * time.Second)
		for _, id := range others {
			v.use(id, 15*time.Second)
		}
	}
	for _, id := range others {
		if n, m := v.count(&v.execs, id), v.count(&v.reads, id); n+m != 0 {
			t.Errorf("%s: looked for agents %d times and read its CPU %d times, want neither", id, n, m)
		}
		if got := v.endAt(id); !got.Equal(t0.Add(time.Minute)) {
			t.Errorf("%s: EndAt = %v, want it left alone", id, got)
		}
	}
}

func TestOldMachinesGetDefaultIdle(t *testing.T) {
	v := newWorld(t)
	id := v.machine("old", "", time.Minute)
	v.procs[id] = codex
	v.tick()
	if got, want := v.endAt(id), v.Now().Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("EndAt = %v, want the default idle time from now, %v", got, want)
	}
}

func TestNeverShortens(t *testing.T) {
	v := newWorld(t)
	id := v.machine("app", "10m0s", 5*time.Hour)
	v.procs[id] = codex
	v.tick()
	if got := v.endAt(id); !got.Equal(t0.Add(5 * time.Hour)) {
		t.Errorf("EndAt = %v, want it left at %v", got, t0.Add(5*time.Hour))
	}
}

func TestCPUCounterRestarts(t *testing.T) {
	for name, restart := range map[string]func(v *world, id string){
		"resumed": func(v *world, id string) {
			// A resume starts a new run, whose counter starts again.
			m := v.get(id)
			m.StartedAt = v.Now()
			v.e.Add(m)
			v.mu.Lock()
			v.cpu[id] = 200 * time.Millisecond
			v.mu.Unlock()
		},
		"resumed, new counter higher": func(v *world, id string) {
			// Differences between two runs' counters mean nothing, even if they look busy.
			m := v.get(id)
			m.StartedAt = v.Now()
			v.e.Add(m)
			v.use(id, 2*time.Second)
		},
		"went down": func(v *world, id string) {
			v.mu.Lock()
			v.cpu[id] = 50 * time.Second
			v.mu.Unlock()
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := newWorld(t)
			id := v.machine("app", "10m0s", time.Minute)
			v.use(id, 100*time.Second)
			v.tick()
			v.later(15 * time.Second)
			restart(v, id)
			end := v.endAt(id)
			v.tick()
			if got := v.endAt(id); !got.Equal(end) {
				t.Errorf("EndAt = %v, want it left at %v: a new counter is no reading", got, end)
			}
			v.later(15 * time.Second)
			v.use(id, 3*time.Second)
			v.tick()
			if got, want := v.endAt(id), v.Now().Add(10*time.Minute); !got.Equal(want) {
				t.Errorf("EndAt = %v, want %v once the new counter has two readings", got, want)
			}
		})
	}
}

func TestLogsChangesOnly(t *testing.T) {
	v := newWorld(t)
	id := v.machine("app", "10m0s", time.Minute)
	v.procs[id] = codex
	for range 3 {
		v.tick()
		v.later(15 * time.Second)
	}
	v.procs[id] = nil
	for range 3 {
		v.tick()
		v.later(15 * time.Second)
	}
	if len(v.logs) != 2 || !strings.Contains(v.logs[0], "app is busy") || !strings.Contains(v.logs[1], "app is idle") {
		t.Errorf("logs = %q, want app busy, then idle", v.logs)
	}
}

func TestCPUProblemLoggedOnce(t *testing.T) {
	v := newWorld(t)
	id := v.machine("app", "10m0s", time.Minute)
	v.cpuErr[id] = errors.New("no cgroup for app")
	v.procs[id] = codex
	for range 3 {
		v.tick()
		v.later(15 * time.Second)
	}
	if n := v.logged("no cgroup for app"); n != 1 {
		t.Errorf("logs = %q, want the CPU problem logged once", v.logs)
	}
	if got, want := v.endAt(id), v.Now().Add(10*time.Minute-15*time.Second); !got.Equal(want) {
		t.Errorf("EndAt = %v, want %v: agents still count without a CPU reading", got, want)
	}
}

type failingExec struct{ engine.Engine }

func (f *failingExec) Exec(context.Context, string, engine.ExecRequest) (engine.ExecResult, error) {
	return engine.ExecResult{}, errors.New("envd is down")
}

func TestAgentProblemLoggedOnce(t *testing.T) {
	v := newWorld(t)
	w := v.watcher(&failingExec{Engine: v.e})
	id := v.machine("app", "10m0s", time.Minute)
	for range 3 {
		if err := w.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		v.later(15 * time.Second)
	}
	if n := v.logged("envd is down"); n != 1 {
		t.Errorf("logs = %q, want the exec problem logged once", v.logs)
	}
	if got := v.endAt(id); !got.Equal(t0.Add(time.Minute)) {
		t.Errorf("EndAt = %v, want it left alone", got)
	}
}

// hanging is an engine whose Exec in machine id hangs until release is closed.
type hanging struct {
	engine.Engine
	id      string
	release chan struct{}
}

func (h hanging) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if id == h.id {
		select {
		case <-h.release:
		case <-ctx.Done():
			return engine.ExecResult{}, ctx.Err()
		}
	}
	return h.Engine.Exec(ctx, id, req)
}

func TestHungMachineHoldsUpNoOther(t *testing.T) {
	v := newWorld(t)
	hung := v.machine("hung", "1m0s", time.Minute)
	web := v.machine("web", "1m0s", time.Minute)
	v.procs[web] = codex
	release := make(chan struct{})
	w := v.watcher(hanging{Engine: v.e, id: hung, release: release})
	v.later(30 * time.Second)
	done := make(chan error, 1)
	go func() { done <- w.Tick(t.Context()) }()
	want := v.Now().Add(time.Minute)
	for deadline := time.Now().Add(5 * time.Second); !v.endAt(web).Equal(want); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Error("web wasn't kept running while the look at hung was stuck")
			break
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// guard fails the test on any call about a machine that was paused when last listed.
type guard struct {
	engine.Engine
	t      *testing.T
	mu     sync.Mutex
	paused map[string]bool
}

func (g *guard) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	all, err := g.Engine.List(ctx, engine.ListOptions{})
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.paused = map[string]bool{}
	for _, m := range all {
		if m.State == engine.Paused {
			g.paused[m.ID] = true
		}
	}
	g.mu.Unlock()
	return g.Engine.List(ctx, opts)
}

func (g *guard) check(op, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused[id] {
		g.t.Errorf("%s on paused machine %s", op, id)
	}
}

func (g *guard) Get(ctx context.Context, id string) (engine.Machine, error) {
	g.check("Get", id)
	return g.Engine.Get(ctx, id)
}

func (g *guard) Pause(ctx context.Context, id string) error {
	g.check("Pause", id)
	return g.Engine.Pause(ctx, id)
}

func (g *guard) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	g.check("Resume", id)
	return g.Engine.Resume(ctx, id, timeout)
}

func (g *guard) Kill(ctx context.Context, id string) error {
	g.check("Kill", id)
	return g.Engine.Kill(ctx, id)
}

func (g *guard) SetTimeout(ctx context.Context, id string, timeout time.Duration) error {
	g.check("SetTimeout", id)
	return g.Engine.SetTimeout(ctx, id, timeout)
}

func (g *guard) Metrics(ctx context.Context, id string) (engine.Metrics, error) {
	g.check("Metrics", id)
	return g.Engine.Metrics(ctx, id)
}

func (g *guard) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	g.check("Exec", id)
	return g.Engine.Exec(ctx, id, req)
}

func (g *guard) DialWebSocket(ctx context.Context, id string, port int, timeout time.Duration) (net.Conn, error) {
	g.check("DialWebSocket", id)
	return g.Engine.DialWebSocket(ctx, id, port, timeout)
}

func (g *guard) BuildTemplate(ctx context.Context, spec engine.TemplateSpec, logs io.Writer) (string, error) {
	g.t.Errorf("BuildTemplate %s", spec.Name)
	return g.Engine.BuildTemplate(ctx, spec, logs)
}

func TestNeverTouchesPausedMachines(t *testing.T) {
	v := newWorld(t)
	w := v.watcher(&guard{Engine: v.e, t: t})
	app := v.machine("app", "10m0s", time.Minute)
	sleepy := v.machine("sleepy", "10m0s", time.Minute)
	if err := v.e.Pause(t.Context(), sleepy); err != nil {
		t.Fatal(err)
	}
	// late is paused between being listed and being looked at, while it's busy.
	late := v.machine("late", "10m0s", time.Minute)
	for _, id := range []string{app, sleepy, late} {
		v.procs[id] = codex
	}
	tick := func() {
		t.Helper()
		if err := w.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	v.later(15 * time.Second)
	for _, id := range []string{app, sleepy, late} {
		v.use(id, 15*time.Second)
	}
	v.mu.Lock()
	v.onCPU = func(id string) {
		if id == late {
			if err := v.e.Pause(t.Context(), late); err != nil {
				t.Error(err)
			}
		}
	}
	v.mu.Unlock()
	tick()
	v.mu.Lock()
	v.onCPU = nil
	v.mu.Unlock()
	v.later(15 * time.Second)
	tick()

	if got, want := v.endAt(app), v.Now().Add(10*time.Minute); !got.Equal(want) {
		t.Errorf("app: EndAt = %v, want %v", got, want)
	}
	for _, id := range []string{sleepy, late} {
		if m := v.get(id); m.State != engine.Paused {
			t.Errorf("%s: %s, want it left paused", id, m.State)
		}
	}
	if n := v.count(&v.reads, sleepy); n != 0 {
		t.Errorf("read the CPU counter of a paused machine %d times", n)
	}
	for _, l := range v.logs {
		if strings.Contains(l, "late") && !strings.Contains(l, "late is busy") {
			t.Errorf("logged %q: a machine pausing under the watcher is no problem", l)
		}
	}
}

func TestCgroupCPU(t *testing.T) {
	root := t.TempDir()
	write := func(dir, stat string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "cpu.stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sbx-abc123-run1", "usage_usec 1500000\nuser_usec 1000000\nsystem_usec 500000\n")
	write("sbx-abc1234-run1", "usage_usec 99\n")
	write("sbx-bad-run1", "usage_usec many\n")
	write("sbx-nousage-run1", "user_usec 5\n")
	cpu := busy.CgroupCPU(root)

	if got, err := cpu("abc123"); err != nil || got != 1500*time.Millisecond {
		t.Errorf(`cpu("abc123") = %v, %v; want 1.5s`, got, err)
	}
	// A finished run's cgroup may linger: it doesn't change, so it can be added.
	write("sbx-abc123-run0", "usage_usec 250\n")
	if got, err := cpu("abc123"); err != nil || got != 1500*time.Millisecond+250*time.Microsecond {
		t.Errorf(`cpu("abc123") = %v, %v; want 1.50025s`, got, err)
	}
	for _, id := range []string{"nope", "bad", "nousage", "*", "", "../x", "a/b", "ABC"} {
		if got, err := cpu(id); err == nil {
			t.Errorf("cpu(%q) = %v, want an error", id, got)
		}
	}
}
