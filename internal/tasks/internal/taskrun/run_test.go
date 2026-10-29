package taskrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
	"github.com/solev/hibernaut/internal/tasks/internal/taskmachine"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

var t0 = time.Date(2025, 10, 28, 9, 0, 0, 0, time.UTC)

var alice = task.Actor{Owner: "solev", Name: "alice", Source: "cli"}

// clock is a fake clock shared by the Supervisor, its workflows, the fake engine and the Owner.
// Sleepers register until Advance passes their time, so a test can wait until every workflow
// is asleep before it moves time on.
type clock struct {
	mu       sync.Mutex
	now      time.Time
	sleepers []*sleeper
}

type sleeper struct {
	until time.Time
	ctx   context.Context
	done  chan struct{}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	s := &sleeper{until: c.now.Add(d), ctx: ctx, done: make(chan struct{})}
	c.sleepers = append(c.sleepers, s)
	c.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		c.sleepers = slices.DeleteFunc(c.sleepers, func(o *sleeper) bool { return o == s })
		c.mu.Unlock()
		return context.Cause(ctx)
	}
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleepers = slices.DeleteFunc(c.sleepers, func(s *sleeper) bool {
		if c.now.Before(s.until) {
			return false
		}
		close(s.done)
		return true
	})
}

// asleep counts sleepers whose context is live: a cancelled sleeper is about to wake.
func (c *clock) asleep() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.sleepers {
		if s.ctx.Err() == nil {
			n++
		}
	}
	return n
}

type memLocks struct {
	mu   sync.Mutex
	held map[string]*sync.Mutex
}

func (l *memLocks) WaitLock(ctx context.Context, name, what string) (func(), error) {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[string]*sync.Mutex{}
	}
	m := l.held[name]
	if m == nil {
		m = &sync.Mutex{}
		l.held[name] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock, nil
}

// counting records the most outside calls in flight at once on any one machine.
type counting struct {
	engine.Engine
	mu       sync.Mutex
	inFlight map[string]int
	most     int
}

func (c *counting) enter(id string) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inFlight == nil {
		c.inFlight = map[string]int{}
	}
	c.inFlight[id]++
	c.most = max(c.most, c.inFlight[id])
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.inFlight[id]--
	}
}

func (c *counting) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	defer c.enter(id)()
	return c.Engine.Exec(ctx, id, req)
}

func (c *counting) Upload(ctx context.Context, id, user string, files []engine.File) error {
	defer c.enter(id)()
	return c.Engine.Upload(ctx, id, user, files)
}

func (c *counting) Pause(ctx context.Context, id string) error {
	defer c.enter(id)()
	return c.Engine.Pause(ctx, id)
}

func (c *counting) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	defer c.enter(id)()
	return c.Engine.Resume(ctx, id, timeout)
}

// machines is Manager as taskrun sees it: room for Room running task machines, names and labels
// from the task, and the Owner underneath every pause and resume.
type machines struct {
	h *harness
	// Room is how many task machines may run at once.
	Room int
}

func (m *machines) running(ctx context.Context) (int, error) {
	ms, err := m.h.sim.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	n := 0
	for _, em := range ms {
		if em.Metadata[task.Label] != "" {
			n++
		}
	}
	return n, err
}

func (m *machines) Fits(ctx context.Context) (func(task.Run) error, error) {
	n, err := m.running(ctx)
	return func(task.Run) error {
		if n >= m.Room {
			return room.ErrFull
		}
		return nil
	}, err
}

func (m *machines) Find(ctx context.Context, id task.ID) (engine.Machine, bool, error) {
	ms, err := m.h.sim.List(ctx, engine.ListOptions{Metadata: map[string]string{task.Label: string(id)}})
	if err != nil || len(ms) == 0 {
		return engine.Machine{}, false, err
	}
	return ms[0], true, nil
}

func (m *machines) Create(ctx context.Context, r task.Run, timeout time.Duration) (engine.Machine, error) {
	if n, err := m.running(ctx); err != nil || n >= m.Room {
		return engine.Machine{}, errors.Join(err, fmt.Errorf("%w: %d running", room.ErrFull, n))
	}
	em, err := m.h.sim.Create(ctx, engine.CreateOptions{Template: r.Spec.Image, Timeout: timeout,
		Metadata: map[string]string{task.Label: string(r.ID.Task), "hib.name": string(r.ID.Task)}})
	if err == nil && m.h.crash("create") {
		return engine.Machine{}, engine.ErrConnectionLost
	}
	return em, err
}

func (m *machines) Resume(ctx context.Context, r task.Run, id string, timeout time.Duration) (engine.Machine, error) {
	if n, err := m.running(ctx); err != nil || n >= m.Room {
		return engine.Machine{}, errors.Join(err, fmt.Errorf("%w: %d running", room.ErrFull, n))
	}
	return m.h.owner.Resume(ctx, id, timeout)
}

func (m *machines) Pause(ctx context.Context, id task.ID) error {
	em, found, err := m.Find(ctx, id)
	if err != nil || !found {
		return err
	}
	return m.h.owner.Pause(ctx, em.ID)
}

func (m *machines) Remove(ctx context.Context, id task.ID) error {
	em, found, err := m.Find(ctx, id)
	if err != nil || !found {
		return err
	}
	return m.h.sim.Kill(ctx, em.ID)
}

// harness is one host: a guestsim engine, a Memory store, the Owner and a daemon whose
// Supervisor the test drives tick by tick.
type harness struct {
	t     *testing.T
	clock *clock
	sim   *guestsim.Sim
	count *counting
	owner *taskmachine.Owner
	store *taskstore.Memory
	m     *machines

	sup     *Supervisor
	stopSup context.CancelFunc
	daemons int

	mu sync.Mutex
	// crashAt crashes the daemon at its crashAt'th outside action, counting from 1. The
	// action happens and its reply is lost. 0 never crashes.
	crashAt, actions int
	crashed          bool
}

func newHarness(t *testing.T, script func(task.RunID, task.Stage) guestsim.Behavior) *harness {
	h := &harness{t: t, clock: &clock{now: t0}, store: taskstore.NewMemory()}
	h.sim = guestsim.New(h.clock.Now)
	h.sim.Script = script
	h.sim.Lose = func(string, []string) bool { return h.crash("exec") }
	h.count = &counting{Engine: h.sim}
	h.owner = taskmachine.New(h.count, h.store, &memLocks{}, h.clock.Now, "test")
	h.m = &machines{h: h, Room: 4}
	t.Cleanup(h.stop)
	return h
}

// crash counts an outside action and reports whether the daemon crashed at it.
func (h *harness) crash(string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.crashed || h.sup == nil {
		return false
	}
	h.actions++
	if h.actions == h.crashAt {
		h.crashed = true
		h.stopSup()
		return true
	}
	return false
}

// start starts a daemon with a new instance, as hibd's start does.
func (h *harness) start() {
	h.t.Helper()
	h.daemons++
	inst := task.Instance(fmt.Sprintf("d%d", h.daemons))
	if err := h.store.StartDaemon(context.Background(), inst, h.clock.Now()); err != nil {
		h.t.Fatal(err)
	}
	life, cancel := context.WithCancel(context.Background())
	sup := NewSupervisor(life, Config{
		Store: h.store, Logs: taskstore.MemoryLogs{M: h.store}, Engine: h.owner,
		Machines: h.m, Clock: h.clock, Instance: inst, Poll: time.Second,
		Logf: func(string, ...any) {},
	})
	h.mu.Lock()
	h.sup, h.stopSup = sup, cancel
	h.mu.Unlock()
}

// stop stops the daemon and waits for its workflows.
func (h *harness) stop() {
	h.mu.Lock()
	sup, cancel := h.sup, h.stopSup
	h.sup = nil
	h.mu.Unlock()
	if sup != nil {
		cancel()
		sup.Wait()
	}
}

// settle waits until every workflow sleeps on the clock.
func (h *harness) settle() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.sup.mu.Lock()
		live := len(h.sup.runs)
		h.sup.mu.Unlock()
		if h.clock.asleep() >= live {
			return
		}
		time.Sleep(100 * time.Microsecond)
	}
	h.t.Fatal("the workflows never settled")
}

// tick is one daemon pass and one poll of time.
func (h *harness) tick() {
	h.t.Helper()
	if err := h.sup.Tick(context.Background()); err != nil && !h.crashed {
		h.t.Fatalf("Tick: %v", err)
	}
	h.settle()
	h.clock.Advance(time.Second)
	h.settle()
}

// until ticks until done holds, at most n times.
func (h *harness) until(n int, done func() bool) {
	h.t.Helper()
	for range n {
		if done() {
			return
		}
		h.tick()
	}
	h.t.Fatalf("not done after %d ticks", n)
}

func (h *harness) submit(command time.Duration) task.RunID {
	h.t.Helper()
	sp := task.Spec{Repo: task.Repo{Owner: "solev", Name: "demo"},
		Command: []string{"codex", "exec"}, Image: "dev", Size: "dev", Timeout: command,
		Publish: task.PublishDraftPR}
	rc, err := h.store.Submit(context.Background(), task.Submission{
		RequestID: fmt.Sprintf("req-%d", time.Now().UnixNano()), Spec: sp, Actor: alice,
		At: h.clock.Now()}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return rc.Run
}

func (h *harness) run(id task.RunID) task.Run {
	h.t.Helper()
	r, err := h.store.Run(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	if m, found, _ := h.m.Find(context.Background(), id.Task); found {
		r.Machine = task.MachineRunning
		if m.State == engine.Paused {
			r.Machine = task.MachinePaused
		}
	}
	return r
}

func (h *harness) ended(id task.RunID) func() bool {
	return func() bool { return h.run(id).Ended() }
}

func (h *harness) launched(id task.RunID, s task.Stage) func() bool {
	return func() bool {
		for _, c := range h.run(id).Log {
			if b, ok := c.Body.(*task.StageLaunched); ok && b.Stage == s {
				return true
			}
		}
		return false
	}
}

func (h *harness) machineID(id task.RunID) string {
	h.t.Helper()
	m, found, err := h.m.Find(context.Background(), id.Task)
	if err != nil || !found {
		h.t.Fatalf("no machine for %s: %v", id, err)
	}
	return m.ID
}

func (h *harness) request(id task.RunID, kind task.RequestKind) {
	h.t.Helper()
	_, err := h.store.Request(context.Background(), taskstore.NewRequest{
		RequestID: fmt.Sprintf("q-%s-%s", id, kind), Task: id.Task, Kind: kind, Actor: alice,
		At: h.clock.Now()})
	if err != nil {
		h.t.Fatal(err)
	}
}

// script makes the command run for d with some output and exit with status, setup succeed at
// once and finish publish a change.
func script(d time.Duration, status int) func(task.RunID, task.Stage) guestsim.Behavior {
	return func(_ task.RunID, s task.Stage) guestsim.Behavior {
		switch s {
		case task.StageCommand:
			return guestsim.Behavior{Output: []byte("did the work\n"), Status: status, Runs: d}
		case task.StageFinish:
			return guestsim.Behavior{Publish: &task.Publication{Changes: true, Commit: "c0ffee",
				Pushed: true}}
		}
		return guestsim.Behavior{}
	}
}

// checkLog checks what every run must hold: steps in table order and each stage started at
// most once.
func (h *harness) checkLog(id task.RunID) {
	h.t.Helper()
	r := h.run(id)
	last := -1
	for _, c := range r.Log {
		if i := c.Step().Index(); i < last {
			h.t.Errorf("checkpoint %d is %s after step %d", c.Seq, c.Step(), last)
		} else {
			last = i
		}
	}
	if mid := r.MachineID(); mid != "" {
		for _, s := range task.Stages() {
			if n := h.sim.Starts(mid, id, s); n > 1 {
				h.t.Errorf("stage %s started %d times", s, n)
			}
		}
	}
	if h.count.most > 1 {
		h.t.Errorf("%d outside calls were in flight on one machine at once", h.count.most)
	}
}

func outcome(r task.Run) task.Outcome {
	o, _ := r.Outcome()
	return o
}

func TestRunSucceeds(t *testing.T) {
	h := newHarness(t, script(3*time.Second, 0))
	h.start()
	id := h.submit(time.Hour)
	h.until(30, h.ended(id))
	h.checkLog(id)
	r := h.run(id)
	if o := outcome(r); o != task.Succeeded {
		t.Fatalf("outcome %s, want %s", o, task.Succeeded)
	}
	if p := r.Publication(); p == nil || p.Commit != "c0ffee" {
		t.Fatalf("publication %+v, want finish's", p)
	}
	if r.Machine != task.MachinePaused || !r.Frozen() {
		t.Fatalf("machine %s frozen %v, want it kept paused and frozen", r.Machine, r.Frozen())
	}
	got, err := taskstore.MemoryLogs{M: h.store}.Open(id, task.StageCommand, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	var b bytes.Buffer
	b.ReadFrom(got)
	if b.String() != "did the work\n" {
		t.Fatalf("command log %q", b.String())
	}
}

func TestStageFails(t *testing.T) {
	h := newHarness(t, script(time.Second, 3))
	h.start()
	id := h.submit(time.Hour)
	h.until(30, h.ended(id))
	h.checkLog(id)
	if o, s := h.run(id).Outcome(); o != task.Failed || s != task.StageCommand {
		t.Fatalf("outcome %s at %s, want failed at command", o, s)
	}
}

// TestCrashAtEveryAction crashes the daemon right after each outside action in turn, with the
// action done and its reply lost, then starts a new daemon. Every run must end as one that
// never crashed did: one machine, each stage started once, the same outcome.
func TestCrashAtEveryAction(t *testing.T) {
	clean := newHarness(t, script(3*time.Second, 0))
	clean.start()
	id := clean.submit(time.Hour)
	clean.until(30, clean.ended(id))
	total := clean.actions
	if total < 8 {
		t.Fatalf("a clean run took only %d outside actions", total)
	}
	for n := 1; n <= total; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			h := newHarness(t, script(3*time.Second, 0))
			h.crashAt = n
			h.start()
			id := h.submit(time.Hour)
			for i := 0; i < 30 && !h.crashed; i++ {
				h.tick()
			}
			if !h.crashed {
				t.Fatalf("never reached action %d", n)
			}
			h.stop()
			h.start()
			h.until(30, h.ended(id))
			h.checkLog(id)
			if o := outcome(h.run(id)); o != task.Succeeded {
				t.Fatalf("outcome %s, want %s", o, task.Succeeded)
			}
			ms, _ := h.sim.List(context.Background(), engine.ListOptions{})
			if len(ms) != 1 {
				t.Fatalf("%d machines, want 1", len(ms))
			}
			if n := h.sim.Starts(ms[0].ID, id, task.StageCommand); n != 1 {
				t.Fatalf("command started %d times", n)
			}
		})
	}
}

// TestCrashAtEveryAppend fails each checkpoint's append in turn, as a crash before the commit
// would, then restarts the daemon.
func TestCrashAtEveryAppend(t *testing.T) {
	clean := newHarness(t, script(3*time.Second, 0))
	clean.start()
	cid := clean.submit(time.Hour)
	clean.until(30, clean.ended(cid))
	for n := 1; n <= len(clean.run(cid).Log); n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			h := newHarness(t, script(3*time.Second, 0))
			seen := 0
			h.store.FailAppends(func(c task.Checkpoint) error {
				seen++
				if seen == n {
					h.mu.Lock()
					h.crashed = true
					h.mu.Unlock()
					go h.stopSup()
					return errors.New("crashed")
				}
				return nil
			})
			h.start()
			id := h.submit(time.Hour)
			for i := 0; i < 30 && !h.crashed; i++ {
				h.tick()
			}
			if !h.crashed {
				t.Fatalf("never reached append %d", n)
			}
			h.stop()
			h.store.FailAppends(nil)
			h.start()
			h.until(30, h.ended(id))
			h.checkLog(id)
			if o := outcome(h.run(id)); o != task.Succeeded {
				t.Fatalf("outcome %s, want %s", o, task.Succeeded)
			}
		})
	}
}

// TestLostReplies loses the first reply of every kind of call. Launches are judged by looks,
// log chunks by the host copy's length and uploads by the in-machine check.
func TestLostReplies(t *testing.T) {
	h := newHarness(t, script(3*time.Second, 0))
	lost := map[string]bool{}
	h.sim.Lose = func(_ string, argv []string) bool {
		k := strings.Join(argv[:min(len(argv), 3)], " ")
		if lost[k] {
			return false
		}
		lost[k] = true
		return true
	}
	h.start()
	id := h.submit(time.Hour)
	h.until(40, h.ended(id))
	h.checkLog(id)
	if o := outcome(h.run(id)); o != task.Succeeded {
		t.Fatalf("outcome %s", o)
	}
	launches := 0
	for k := range lost {
		if strings.HasPrefix(k, "systemd-run") {
			launches++
		}
	}
	if launches != 3 {
		t.Fatalf("lost %d launch replies, want one per stage", launches)
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t, script(time.Hour, 0))
	h.start()
	id := h.submit(2 * time.Hour)
	h.until(30, h.launched(id, task.StageCommand))
	h.request(id, task.RequestCancel)
	h.until(10, h.ended(id))
	h.checkLog(id)
	r := h.run(id)
	if o := outcome(r); o != task.Cancelled {
		t.Fatalf("outcome %s, want cancelled", o)
	}
	if !slices.ContainsFunc(r.Log, func(c task.Checkpoint) bool {
		b, ok := c.Body.(*task.StopResult)
		return ok && b.How == "term"
	}) {
		t.Fatal("the stop didn't end the command with TERM")
	}
}

func TestDeadline(t *testing.T) {
	h := newHarness(t, script(time.Hour, 0))
	h.start()
	id := h.submit(20 * time.Second)
	h.until(60, h.ended(id))
	h.checkLog(id)
	if o := outcome(h.run(id)); o != task.TimedOut {
		t.Fatalf("outcome %s, want timed_out", o)
	}
}

// TestQueue gives room for one task machine. The second run waits until the first is kept
// paused, and no two runs ever hold the grant.
func TestQueue(t *testing.T) {
	h := newHarness(t, script(3*time.Second, 0))
	h.m.Room = 1
	h.start()
	a, b := h.submit(time.Hour), h.submit(time.Hour)
	for range 40 {
		if h.run(a).Ended() && h.run(b).Ended() {
			break
		}
		if n, _ := h.m.running(context.Background()); n > 1 {
			t.Fatalf("%d task machines run with room for one", n)
		}
		h.tick()
	}
	if !h.run(a).Ended() || !h.run(b).Ended() {
		t.Fatal("the queue didn't drain")
	}
	h.checkLog(a)
	h.checkLog(b)
}

// TestRestartResumesFrozen pauses a running stage through the Owner and stops the daemon, as a
// host shutdown does. The next daemon resumes the machine, thaws the stage and finishes.
func TestRestartResumesFrozen(t *testing.T) {
	h := newHarness(t, script(5*time.Second, 0))
	h.start()
	id := h.submit(time.Hour)
	h.until(30, h.launched(id, task.StageCommand))
	h.stop()
	if err := h.owner.Pause(context.Background(), h.machineID(id)); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(10 * time.Minute)
	h.start()
	h.until(30, h.ended(id))
	h.checkLog(id)
	if o := outcome(h.run(id)); o != task.Succeeded {
		t.Fatalf("outcome %s", o)
	}
}

// TestStrayPauseResumes pauses a running stage through the Owner while the daemon runs, as a
// host shutdown that never came leaves it. Only the host pauses a live run's machine, so the
// daemon resumes it at its next look, thaws the stage and finishes.
func TestStrayPauseResumes(t *testing.T) {
	h := newHarness(t, script(5*time.Second, 0))
	h.start()
	id := h.submit(time.Hour)
	h.until(30, h.launched(id, task.StageCommand))
	h.tick()
	if err := h.owner.Pause(context.Background(), h.machineID(id)); err != nil {
		t.Fatal(err)
	}
	h.until(30, h.ended(id))
	h.checkLog(id)
	if o := outcome(h.run(id)); o != task.Succeeded {
		t.Fatalf("outcome %s", o)
	}
}

// TestHeldUntilRescue pauses a running stage without the freeze, as E2B's timeout would. No
// one may wake it, the daemon included, until hib task rescue, which wakes it only to kill it.
func TestHeldUntilRescue(t *testing.T) {
	h := newHarness(t, script(time.Hour, 0))
	h.start()
	id := h.submit(2 * time.Hour)
	h.until(30, h.launched(id, task.StageCommand))
	h.tick()
	mid := h.machineID(id)
	if err := h.sim.Pause(context.Background(), mid); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		h.tick()
	}
	if st := h.run(id).Status(""); st.Phase != task.PhasePaused || st.Held == "" {
		t.Fatalf("status %+v, want held", st)
	}
	if _, err := h.owner.Resume(context.Background(), mid, time.Minute); !errors.Is(err, task.ErrHeld) {
		t.Fatalf("Resume of a held machine = %v, want ErrHeld", err)
	}
	if _, err := h.owner.DialWebSocket(context.Background(), mid, 22, time.Minute); !errors.Is(err, task.ErrHeld) {
		t.Fatalf("a dial of a held machine = %v, want ErrHeld", err)
	}
	h.request(id, task.RequestRescue)
	h.until(10, h.ended(id))
	h.checkLog(id)
	r := h.run(id)
	if o := outcome(r); o != task.Cancelled {
		t.Fatalf("outcome %s, want cancelled", o)
	}
	if h.sim.Starts(mid, id, task.StageFinish) != 0 {
		t.Fatal("finish ran after a rescue")
	}
	if !slices.ContainsFunc(r.Log, func(c task.Checkpoint) bool {
		b, ok := c.Body.(*task.StopResult)
		return ok && b.How == "kill"
	}) {
		t.Fatal("a rescue must kill the stage, never let it act")
	}
}

// TestRemoveWhileRunning removes a task whose stage runs: the stop ends the stage and keep
// removes the machine.
func TestRemoveWhileRunning(t *testing.T) {
	h := newHarness(t, script(time.Hour, 0))
	h.start()
	id := h.submit(2 * time.Hour)
	h.until(30, h.launched(id, task.StageCommand))
	h.request(id, task.RequestRemove)
	h.until(10, h.ended(id))
	h.checkLog(id)
	if _, found, _ := h.m.Find(context.Background(), id.Task); found {
		t.Fatal("the machine outlived hib task rm")
	}
}
