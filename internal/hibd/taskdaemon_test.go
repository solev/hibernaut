package hibd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
	"github.com/solev/hibernaut/internal/tasks"
)

type taskSimEngine struct {
	*guestsim.Sim
	initReply  []byte
	created    atomic.Int32
	pgreps     atomic.Int32
	renewed    atomic.Int32
	person     *secretGuest
	beforeExec func(context.Context, string, engine.ExecRequest) error
}

func (e *taskSimEngine) Create(ctx context.Context, opts engine.CreateOptions) (engine.Machine, error) {
	em, err := e.Sim.Create(ctx, opts)
	if err == nil {
		e.created.Add(1)
	}
	return em, err
}

func (e *taskSimEngine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if e.beforeExec != nil {
		if err := e.beforeExec(ctx, id, req); err != nil {
			return engine.ExecResult{}, err
		}
	}
	switch req.Cmd {
	case images.MachineInit:
		return engine.ExecResult{Stdout: e.initReply}, nil
	case "pgrep":
		e.pgreps.Add(1)
		return engine.ExecResult{}, nil
	}
	m, err := e.Get(ctx, id)
	if err != nil {
		return engine.ExecResult{}, err
	}
	if m.Metadata[task.Label] == "" && e.person != nil {
		return e.person.exec(ctx, m, "", req)
	}
	return e.Sim.Exec(ctx, id, req)
}

func (e *taskSimEngine) SetTimeout(ctx context.Context, id string, d time.Duration) error {
	e.renewed.Add(1)
	return e.Sim.SetTimeout(ctx, id, d)
}

func taskDaemonApp(t *testing.T) (*App, *taskSimEngine) {
	t.Helper()
	a, _, _ := newApp(t, nil)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return at }
	e := &taskSimEngine{Sim: guestsim.New(a.now),
		initReply: []byte(hibapi.FormatHostKey(newKey(t)) + "\n")}
	a.NewEngine = func() (engine.Engine, error) { return e, nil }
	a.Stderr = &syncBuffer{}
	a.DaemonTicks = make(chan time.Time)
	a.TaskTicks = make(chan time.Time)
	a.taskPoll = time.Millisecond
	return a, e
}

func runTaskDaemon(t *testing.T, a *App) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"daemon"}) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("daemon exit %d; log %s", code, a.Stderr)
				}
			case <-time.After(5 * time.Second):
				t.Error("daemon did not stop within its deadline")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func waitTask(t *testing.T, a *App, id task.ID, what string, accept func(task.Run) bool) task.Run {
	t.Helper()
	st := savedTasks(t, a)
	var r task.Run
	waitFor(t, a.Stderr.(*syncBuffer), what, func() bool {
		got, err := st.Task(t.Context(), id)
		if err != nil || len(got.Runs) == 0 {
			return false
		}
		r = got.Runs[len(got.Runs)-1]
		return accept(r)
	})
	return r
}

func waitTaskEnded(t *testing.T, a *App, id task.ID) task.Run {
	t.Helper()
	return waitTask(t, a, id, "the task run ends", func(r task.Run) bool { return r.Ended() })
}

func sendTaskTick(t *testing.T, ticks chan<- time.Time, at time.Time) {
	t.Helper()
	select {
	case ticks <- at:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not accept its tick")
	}
}

func waitTaskSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not %s", what)
	}
}

func TestTaskDaemonRunsAndKeepsMachine(t *testing.T) {
	a, e := taskDaemonApp(t)
	req := hibapi.TaskStart{RequestID: "daemon-runs", Command: []string{"true"},
		Prompt: &hibapi.TaskFile{Size: 5}, Files: []hibapi.TaskFile{{Name: "note", Size: 4}}}
	code, reply := submitTaskJSON(t, a, req, strings.NewReader("hello\x00bye"))
	got := requireTaskReply(t, code, reply, req.RequestID, 1)
	runTaskDaemon(t, a)
	r := waitTaskEnded(t, a, got.ID)
	if out, _ := r.Outcome(); out != task.Succeeded {
		t.Fatalf("run outcome %s, want succeeded; log %s", out, a.Stderr)
	}
	m, err := e.Get(t.Context(), r.MachineID())
	if err != nil || m.State != engine.Paused || e.created.Load() != 1 {
		t.Fatalf("kept machine %+v, %v, creates %d", m, err, e.created.Load())
	}
	b, ok := e.File(m.ID, taskguest.RunDir(r.ID)+"/in/note")
	if !ok || string(b) != "\x00bye" || e.Starts(m.ID, r.ID, task.StageCommand) != 1 {
		t.Fatalf("guest file %q, present %v, command starts %d", b, ok, e.Starts(m.ID, r.ID, task.StageCommand))
	}
}

func TestTaskDaemonRestartReusesMachine(t *testing.T) {
	a, e := taskDaemonApp(t)
	var at atomic.Int64
	at.Store(a.now().UnixNano())
	a.Now = func() time.Time { return time.Unix(0, at.Load()).UTC() }
	e.Script = func(_ task.RunID, st task.Stage) guestsim.Behavior {
		if st == task.StageCommand {
			return guestsim.Behavior{Runs: time.Hour}
		}
		return guestsim.Behavior{}
	}
	req := hibapi.TaskStart{RequestID: "restart", Command: []string{"true"}}
	code, reply := submitTaskJSON(t, a, req, strings.NewReader(""))
	got := requireTaskReply(t, code, reply, req.RequestID, 1)
	stop := runTaskDaemon(t, a)
	live := waitTask(t, a, got.ID, "the command starts", func(r task.Run) bool {
		c, ok := r.Last()
		return ok && c.Step() == task.StepCommand && c.Kind() == task.Result
	})
	code, replay := submitTaskJSON(t, a, req, strings.NewReader(""))
	if code != 0 || replay.Task == nil || len(replay.Task.Runs) != 1 ||
		replay.Task.Runs[0].Phase != task.PhaseRunning {
		t.Fatalf("live receipt exit %d, task %+v; want running, not recovering", code, replay.Task)
	}
	st := savedTasks(t, a)
	first, err := st.Instance(t.Context())
	if err != nil || first == "" {
		t.Fatalf("first daemon instance %q, %v", first, err)
	}
	stop()
	at.Add(int64(time.Hour))
	runTaskDaemon(t, a)
	r := waitTaskEnded(t, a, got.ID)
	second, err := st.Instance(t.Context())
	if out, _ := r.Outcome(); out != task.Succeeded || err != nil || first == second {
		t.Fatalf("restart outcome %s, instances %q %q, error %v", out, first, second, err)
	}
	m, err := e.Get(t.Context(), r.MachineID())
	if err != nil || m.State != engine.Paused || r.MachineID() != live.MachineID() ||
		e.created.Load() != 1 || e.Starts(m.ID, r.ID, task.StageCommand) != 1 {
		t.Fatalf("restart machine %+v, %v, creates %d; want one paused machine and one command", m, err, e.created.Load())
	}
}

func TestTaskDaemonFencesCrashDuplicates(t *testing.T) {
	a, e := taskDaemonApp(t)
	first := seedTask(t, a, hibapi.TaskStart{RequestID: "duplicates",
		Command: []string{"true"}}, strings.NewReader(""))
	for i, id := range []string{"oldest", "duplicate"} {
		e.Add(engine.Machine{ID: id, State: engine.Running, StartedAt: a.now().Add(time.Duration(i)),
			EndAt: a.now().Add(time.Hour), Metadata: map[string]string{
				"hib.name": string(first.Run.Task), task.Label: string(first.Run.Task)}})
	}
	f := &fakeLinks{}
	withLinks(a, f, true)
	runTaskDaemon(t, a)
	r := waitTaskEnded(t, a, first.Run.Task)
	if r.MachineID() != "oldest" || e.created.Load() != 0 {
		t.Fatalf("recovered machine %q, creates %d; want oldest adopted", r.MachineID(), e.created.Load())
	}
	if _, err := e.Get(t.Context(), "duplicate"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("crash duplicate remains: %v", err)
	}
	events := f.log()
	fence, closeLinks := slices.Index(events, "fence duplicate rm duplicate"), slices.Index(events, "close duplicate")
	if fence < 0 || closeLinks <= fence || slices.Index(events, "release duplicate") <= closeLinks {
		t.Fatalf("duplicate removed without its links fence: %v", events)
	}
}

func TestTaskDaemonKeepsHostCPUCap(t *testing.T) {
	a, e := taskDaemonApp(t)
	e.Add(engine.Machine{ID: "person", State: engine.Running, StartedAt: a.now(),
		CPUs: runtime.NumCPU() * machines.DefaultCPUFactor})
	first := seedTask(t, a, hibapi.TaskStart{RequestID: "cpu-cap",
		Command: []string{"true"}}, strings.NewReader(""))
	ticks := make(chan time.Time)
	a.TaskTicks = ticks
	runTaskDaemon(t, a)
	sendTaskTick(t, ticks, a.now())
	sendTaskTick(t, ticks, a.now())
	got, err := savedTasks(t, a).Task(t.Context(), first.Run.Task)
	if err != nil || len(got.Runs[0].Log) != 0 || e.created.Load() != 0 {
		t.Fatalf("daemon exceeded host CPU cap: task %+v, creates %d, error %v", got, e.created.Load(), err)
	}
}

func addTaskPerson(a *App, e *taskSimEngine) {
	e.person = &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e.Add(engine.Machine{ID: "person", State: engine.Running, StartedAt: a.now(),
		EndAt: a.now().Add(time.Minute), Metadata: map[string]string{"hib.name": "person"}})
}

func TestTaskDaemonStandbyTakesOver(t *testing.T) {
	a, e := taskDaemonApp(t)
	st := savedTasks(t, a)
	if _, err := st.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopFirst := runTaskDaemon(t, a)
	waitFor(t, a.Stderr.(*syncBuffer), "the first daemon records its instance", func() bool {
		i, err := st.Instance(t.Context())
		return err == nil && i != ""
	})
	first, _ := st.Instance(t.Context())
	b := *a
	b.Stderr = &syncBuffer{}
	bt, tt := make(chan time.Time), make(chan time.Time)
	b.DaemonTicks, b.TaskTicks = bt, tt
	addTaskPerson(a, e)
	saveTestProfile(t, a, "Standby")
	var cleaned atomic.Int32
	b.Clean = func(context.Context, engine.Engine, time.Time, bool, func(string, ...any)) error {
		cleaned.Add(1)
		return nil
	}
	runTaskDaemon(t, &b)
	waitFor(t, b.Stderr.(*syncBuffer), "the standby reports another task daemon", func() bool {
		return strings.Contains(b.Stderr.(*syncBuffer).String(), tasks.ErrDaemonRunning.Error())
	})
	for range 3 {
		sendTaskTick(t, tt, a.now())
	}
	req := hibapi.TaskStart{RequestID: "standby", Command: []string{"true"}}
	code, reply := submitTaskJSON(t, &b, req, strings.NewReader(""))
	got := requireTaskReply(t, code, reply, req.RequestID, 1)
	waitFor(t, b.Stderr.(*syncBuffer), "standby busy, cleanup and setup finish", func() bool {
		return e.pgreps.Load() > 0 && cleaned.Load() > 0 && e.person.identity() != nil
	})
	r, err := st.Task(t.Context(), got.ID)
	if err != nil || len(r.Runs[0].Log) != 0 || e.created.Load() != 0 ||
		strings.Count(b.Stderr.(*syncBuffer).String(), tasks.ErrDaemonRunning.Error()) != 1 {
		t.Fatalf("standby started a workflow or repeated its warning: %+v, %v; log %s", r, err, b.Stderr)
	}
	stopFirst()
	sendTaskTick(t, tt, a.now())
	waitTaskEnded(t, &b, got.ID)
	second, err := st.Instance(t.Context())
	if err != nil || second == first {
		t.Fatalf("standby instance %q, %v; still first %q", second, err, first)
	}
}

func TestTaskDaemonRetriesUnreadableStoreWithoutStoppingOtherWork(t *testing.T) {
	a, e := taskDaemonApp(t)
	addTaskPerson(a, e)
	saveTestProfile(t, a, "Still delivered")
	db := filepath.Join(a.Getenv("HIBD_STATE_DIR"), "tasks.db")
	if err := os.Mkdir(db, 0o700); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	a.TaskTicks = ticks
	var cleaned atomic.Int32
	a.Clean = func(context.Context, engine.Engine, time.Time, bool, func(string, ...any)) error {
		cleaned.Add(1)
		return nil
	}
	runTaskDaemon(t, a)
	logs := a.Stderr.(*syncBuffer)
	waitFor(t, logs, "the tasks.db startup problem is logged", func() bool {
		return strings.Contains(logs.String(), "tasks: ") && strings.Contains(logs.String(), "regular file")
	})
	for range 3 {
		sendTaskTick(t, ticks, a.now())
	}
	waitFor(t, logs, "busy, cleanup and setup still finish", func() bool {
		return e.pgreps.Load() > 0 && cleaned.Load() > 0 && e.person.identity() != nil
	})
	if n := strings.Count(logs.String(), "regular file"); n != 1 {
		t.Fatalf("tasks.db failure logged %d times, want once; log %s", n, logs)
	}
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	sendTaskTick(t, ticks, a.now())
	sendTaskTick(t, ticks, a.now())
	st := savedTasks(t, a)
	waitFor(t, logs, "tasks startup retries after tasks.db is repaired", func() bool {
		inst, err := st.Instance(t.Context())
		return err == nil && inst != ""
	})
}

func TestTaskDaemonLogsFailuresByTime(t *testing.T) {
	for _, mode := range []string{"changing", "alternating", "successes", "recovering"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := taskDaemonApp(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			base := a.now()
			var at atomic.Int64
			at.Store(base.UnixNano())
			a.Now = func() time.Time { return time.Unix(0, at.Load()).UTC() }
			ticks := make(chan time.Time)
			a.TaskTicks = ticks
			type reading struct {
				at  time.Time
				err error
			}
			readings := make(chan reading)
			waiting := make(chan struct{})
			a.ReadRoom = func() (room.Host, error) {
				select {
				case waiting <- struct{}{}:
				case <-ctx.Done():
					return room.Host{}, ctx.Err()
				}
				select {
				case r := <-readings:
					at.Store(r.at.UnixNano())
					return roomy, r.err
				case <-ctx.Done():
					return room.Host{}, ctx.Err()
				}
			}
			done := make(chan int, 1)
			go func() { done <- a.Run(ctx, []string{"daemon"}) }()
			var once sync.Once
			stop := func() {
				t.Helper()
				once.Do(func() {
					cancel()
					select {
					case code := <-done:
						if code != 0 {
							t.Errorf("daemon exit %d; log %s", code, a.Stderr)
						}
					case <-time.After(5 * time.Second):
						t.Error("daemon did not stop within its deadline")
					}
				})
			}
			t.Cleanup(stop)
			logs := a.Stderr.(*syncBuffer)
			check := func(want []string) {
				t.Helper()
				var got []string
				for _, line := range strings.Split(logs.String(), "\n") {
					if i := strings.Index(line, "tasks: "); i >= 0 {
						got = append(got, line[i:])
					}
				}
				if !slices.Equal(got, want) {
					t.Fatalf("task tick logs = %q, want %q", got, want)
				}
			}
			first := "tasks: checking the room on the host: failure 0"
			initial := []string{first}
			for n, elapsed := range []time.Duration{0, 5 * time.Second, 10 * time.Second,
				15 * time.Second, 10 * time.Minute} {
				text := fmt.Sprintf("failure %d", n)
				if mode == "alternating" {
					text = fmt.Sprintf("failure %d", n%2)
				}
				err := errors.New(text)
				if mode == "successes" && n%2 != 0 || mode == "recovering" && n == 4 {
					err = nil
				}
				waitTaskSignal(t, waiting, "start reading admission")
				select {
				case readings <- reading{at: base.Add(elapsed), err: err}:
				case <-ctx.Done():
					t.Fatal("daemon did not read admission before its deadline")
				}
				sendTaskTick(t, ticks, base.Add(elapsed))
				if mode == "successes" && n == 1 {
					initial = append(initial, "tasks: recovered")
				}
				if n < 4 {
					check(initial)
				}
			}
			latest, count := "failure 4", 3
			if mode == "alternating" {
				latest = "failure 0"
			}
			if mode == "successes" {
				count = 1
			}
			want := append(initial,
				fmt.Sprintf("tasks: checking the room on the host: %s (%d more since the last line)", latest, count))
			if mode == "recovering" {
				want = append(initial, "tasks: recovered (3 more since the last line)")
			}
			check(want)
			waitTaskSignal(t, waiting, "start the final admission read")
			at.Store(base.Add(30 * time.Minute).UnixNano())
			stop()
			check(want)
		})
	}
}

func TestTaskDaemonDoesNotTickWhileQuiescing(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			a, e := taskDaemonApp(t)
			req := hibapi.TaskStart{RequestID: "quiescing", Command: []string{"true"}}
			first := seedTask(t, a, req, strings.NewReader(""))
			marker := a.Getenv("HIBD_QUIESCING")
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if broken {
				getenv := a.Getenv
				a.Getenv = func(k string) string {
					if k == "HIBD_QUIESCING" {
						return filepath.Join(marker, "child")
					}
					return getenv(k)
				}
			}
			ticks := make(chan time.Time)
			a.TaskTicks = ticks
			runTaskDaemon(t, a)
			for range 3 {
				sendTaskTick(t, ticks, a.now())
			}
			st := savedTasks(t, a)
			inst, err := st.Instance(t.Context())
			got, readErr := st.Task(t.Context(), first.Run.Task)
			if err != nil || readErr != nil || inst != "" || len(got.Runs[0].Log) != 0 || e.created.Load() != 0 {
				t.Fatalf("quiescing daemon granted work: instance %q, task %+v, errors %v %v", inst, got, err, readErr)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			sendTaskTick(t, ticks, a.now())
			waitTaskEnded(t, a, first.Run.Task)
		})
	}
}

func TestTaskDaemonBlockedTickDoesNotBlockBusyOrSweep(t *testing.T) {
	a, e := taskDaemonApp(t)
	addTaskPerson(a, e)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	a.ReadRoom = func() (room.Host, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return roomy, nil
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	cleaned := make(chan struct{}, 10)
	a.Clean = func(context.Context, engine.Engine, time.Time, bool, func(string, ...any)) error {
		cleaned <- struct{}{}
		return nil
	}
	busyTicks, taskTicks := make(chan time.Time), make(chan time.Time, 3)
	a.DaemonTicks, a.TaskTicks = busyTicks, taskTicks
	stop := runTaskDaemon(t, a)
	defer stop()
	defer once.Do(func() { close(release) })
	waitTaskSignal(t, entered, "enter task admission")
	for range 3 {
		taskTicks <- a.now()
	}
	waitTaskSignal(t, cleaned, "sweep during blocked admission")
	waitFor(t, a.Stderr.(*syncBuffer), "busy ticks during blocked admission", func() bool { return e.pgreps.Load() >= 1 })
	sendTaskTick(t, busyTicks, a.now().Add(time.Hour))
	waitTaskSignal(t, cleaned, "sweep again during blocked admission")
	waitFor(t, a.Stderr.(*syncBuffer), "busy ticks again during blocked admission", func() bool { return e.pgreps.Load() >= 2 })
	<-time.After(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("overlapping Fits calls %d, want one", calls.Load())
	}
}

func TestTaskDaemonClosesWorkflowsBeforeStopping(t *testing.T) {
	a, e := taskDaemonApp(t)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var block sync.Once
	e.beforeExec = func(ctx context.Context, _ string, req engine.ExecRequest) error {
		if req.Cmd == "sh" && len(req.Args) > 1 && req.Args[1] == "look" {
			block.Do(func() {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
			})
			return ctx.Err()
		}
		return nil
	}
	seedTask(t, a, hibapi.TaskStart{RequestID: "close", Command: []string{"true"}}, strings.NewReader(""))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"daemon"}) }()
	var freed sync.Once
	t.Cleanup(func() {
		cancel()
		freed.Do(func() { close(release) })
	})
	waitTaskSignal(t, entered, "start a workflow")
	cancel()
	waitTaskSignal(t, cancelled, "cancel the workflow")
	dir := statefile.Dir{Path: a.Getenv("HIBD_STATE_DIR"), EUID: a.EUID}
	unlock, acquired, err := dir.TryLock(tasks.DaemonLock, "test shutdown")
	if acquired {
		unlock()
	}
	if err != nil || acquired || strings.Contains(a.Stderr.(*syncBuffer).String(), "stopping") {
		t.Fatalf("daemon released ownership or logged stopping before workflow returned: acquired %v, error %v; log %s", acquired, err, a.Stderr)
	}
	freed.Do(func() { close(release) })
	select {
	case code := <-done:
		if code != 0 || !strings.HasSuffix(a.Stderr.(*syncBuffer).String(), "stopping\n") {
			t.Fatalf("daemon shutdown exit %d, log %s", code, a.Stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not wait for workflows and stop")
	}
	unlock, acquired, err = dir.TryLock(tasks.DaemonLock, "test next daemon")
	if err != nil || !acquired {
		t.Fatalf("task daemon lock not released after stopping: %v, %v", acquired, err)
	}
	unlock()
}

func TestTaskDaemonBusyExemptsLiveButRenewsKeptMachine(t *testing.T) {
	a, e := taskDaemonApp(t)
	var at atomic.Int64
	at.Store(a.now().UnixNano())
	a.Now = func() time.Time { return time.Unix(0, at.Load()).UTC() }
	e.Script = func(_ task.RunID, st task.Stage) guestsim.Behavior {
		if st == task.StageCommand {
			return guestsim.Behavior{Runs: time.Hour}
		}
		return guestsim.Behavior{}
	}
	req := hibapi.TaskStart{RequestID: "busy-exempt", Command: []string{"true"}}
	first := seedTask(t, a, req, strings.NewReader(""))
	ticks := make(chan time.Time)
	a.DaemonTicks = ticks
	stop := runTaskDaemon(t, a)
	live := waitTask(t, a, first.Run.Task, "the command starts", func(r task.Run) bool {
		c, ok := r.Last()
		return ok && c.Step() == task.StepCommand && c.Kind() == task.Result
	})
	activity := links.NewClock(a.Getenv("HIBD_STATE_DIR"))
	before := activity.Active(live.MachineID())
	renewed := e.renewed.Load()
	at.Add(int64(time.Minute))
	sendTaskTick(t, ticks, a.now())
	sendTaskTick(t, ticks, a.now())
	if e.pgreps.Load() != 0 || e.renewed.Load() != renewed || !activity.Active(live.MachineID()).Equal(before) {
		t.Fatalf("busy observed, renewed or recorded a held task: pgrep %d, renewals %d -> %d, activity %s -> %s", e.pgreps.Load(), renewed, e.renewed.Load(), before, activity.Active(live.MachineID()))
	}
	at.Add(int64(time.Hour))
	waitTaskEnded(t, a, first.Run.Task)
	stop()
	if _, err := e.Sim.Resume(t.Context(), live.MachineID(), time.Minute); err != nil {
		t.Fatal(err)
	}
	b := *a
	b.Stderr = &syncBuffer{}
	runTaskDaemon(t, &b)
	waitFor(t, b.Stderr.(*syncBuffer), "busy renews the kept task machine", func() bool {
		return e.pgreps.Load() > 0 && e.renewed.Load() > renewed && activity.Active(live.MachineID()).Equal(a.now())
	})
	m, err := e.Get(t.Context(), live.MachineID())
	if err != nil || !m.EndAt.Equal(a.now().Add(15*time.Minute)) {
		t.Fatalf("kept machine timeout %s, %v; want App.Now plus idle", m.EndAt, err)
	}
}

func TestTaskDaemonBusyFailsClosedWhenStoreCannotSay(t *testing.T) {
	a, e := taskDaemonApp(t)
	e.Add(engine.Machine{ID: "unknown", State: engine.Running, StartedAt: a.now(), EndAt: a.now().Add(time.Minute),
		Metadata: map[string]string{"hib.name": "t-aaaaaaaa", task.Label: "t-aaaaaaaa"}})
	if err := os.Mkdir(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "tasks.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	a.DaemonTicks = ticks
	runTaskDaemon(t, a)
	sendTaskTick(t, ticks, a.now())
	sendTaskTick(t, ticks, a.now())
	logs := a.Stderr.(*syncBuffer).String()
	if e.pgreps.Load() != 0 || e.renewed.Load() != 0 ||
		!links.NewClock(a.Getenv("HIBD_STATE_DIR")).Active("unknown").IsZero() ||
		strings.Contains(logs, "reading its CPU time") || strings.Contains(logs, "looking for coding agents") {
		t.Fatalf("busy touched a task whose store cannot say: pgrep %d, renewals %d; log %s", e.pgreps.Load(), e.renewed.Load(), logs)
	}
}

func TestTaskStartReceiptTracksRecordedDaemon(t *testing.T) {
	a, _ := taskDaemonApp(t)
	req := hibapi.TaskStart{RequestID: "old-receipt", Command: []string{"true"}}
	first := seedTask(t, a, req, strings.NewReader(""))
	stop := runTaskDaemon(t, a)
	waitTaskEnded(t, a, first.Run.Task)
	stop()
	follow := req
	follow.RequestID, follow.Task = "new-receipt", first.Run.Task
	seedTask(t, a, follow, strings.NewReader(""))
	code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
	if code != 0 || r.Task == nil || len(r.Task.Runs) != 1 || r.Task.Runs[0].N != 1 ||
		r.Task.Runs[0].Outcome != task.Succeeded {
		t.Fatalf("old receipt exit %d, reply %+v; want only completed run 1", code, r)
	}
	inst, err := savedTasks(t, a).Instance(t.Context())
	if err != nil || inst == "" {
		t.Fatalf("recorded daemon instance %q, %v", inst, err)
	}
}
