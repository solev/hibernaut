package taskrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
)

type failureLogs struct {
	mu    sync.Mutex
	lines []string
}

func (l *failureLogs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *failureLogs) check(t *testing.T, want []string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.Equal(l.lines, want) {
		t.Fatalf("failure logs = %q, want %q", l.lines, want)
	}
}

func TestRemovalLogsFailuresByTime(t *testing.T) {
	for _, alternating := range []bool{false, true} {
		t.Run(fmt.Sprint("alternating=", alternating), func(t *testing.T) {
			h, id := keptRemoval(t)
			var phase atomic.Int32
			var logs failureLogs
			h.configure = func(cfg *Config) {
				cfg.Logf = logs.logf
				cfg.Machines = removeMachines{Machines: h.m, remove: func(context.Context, task.Run) error {
					n := phase.Load()
					if n < 0 {
						h.stopSup()
						return errors.New("failure after shutdown")
					}
					if alternating {
						n %= 2
					}
					return fmt.Errorf("failure %d", n)
				}}
			}
			h.request(id, task.RequestRemove)
			h.start()
			attempt := func(n int32) {
				t.Helper()
				phase.Store(n)
				if err := tickRemoval(t, h); err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() { h.sup.Wait(); close(done) }()
				receiveRemoval(t, done, "removal attempt did not return")
			}
			first := fmt.Sprintf("task %s removal: failure 0", id.Task)
			attempt(0)
			for n := int32(1); n < 4; n++ {
				h.clock.Advance(5 * time.Second)
				attempt(n)
			}
			logs.check(t, []string{first})
			h.clock.Advance(10*time.Minute - 15*time.Second)
			attempt(4)
			latest := "failure 4"
			if alternating {
				latest = "failure 0"
			}
			second := fmt.Sprintf("task %s removal: %s (3 more since the last line)", id.Task, latest)
			logs.check(t, []string{first, second})
			h.clock.Advance(10 * time.Minute)
			attempt(-1)
			h.stop()
			logs.check(t, []string{first, second})
		})
	}
}

func TestRemovalLoggingDoesNotBlockTick(t *testing.T) {
	h, id := keptRemoval(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.configure = func(cfg *Config) {
		cfg.Logf = func(string, ...any) {
			close(started)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				t.Error("test did not release the failure logger")
			}

		}
		cfg.Machines = removeMachines{Machines: h.m, remove: func(context.Context, task.Run) error {
			return errors.New("remove failed")
		}}
	}
	h.request(id, task.RequestRemove)
	h.start()
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	receiveRemoval(t, started, "removal failure was not logged")
	defer close(release)
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestRemovalLogsRecovery(t *testing.T) {
	h, id := keptRemoval(t)
	var recovered atomic.Bool
	var logs failureLogs
	h.configure = func(cfg *Config) {
		cfg.Logf = logs.logf
		cfg.Machines = removeMachines{Machines: h.m, remove: func(context.Context, task.Run) error {
			if recovered.Load() {
				return nil
			}
			return errors.New("remove failed")
		}}
	}
	h.request(id, task.RequestRemove)
	h.start()
	attempt := func() {
		t.Helper()
		if err := tickRemoval(t, h); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { h.sup.Wait(); close(done) }()
		receiveRemoval(t, done, "removal attempt did not return")
	}
	attempt()
	h.clock.Advance(5 * time.Second)
	attempt()
	h.clock.Advance(5 * time.Second)
	recovered.Store(true)
	attempt()
	want := []string{fmt.Sprintf("task %s removal: remove failed", id.Task),
		fmt.Sprintf("task %s removal: recovered (1 more since the last line)", id.Task)}
	logs.check(t, want)
	h.clock.Advance(10 * time.Minute)
	attempt()
	logs.check(t, want)
}

func TestWorkflowLogsEnd(t *testing.T) {
	h := newHarness(t, script(0, 0))
	var recovered atomic.Bool
	var logs failureLogs
	h.configure = func(cfg *Config) {
		cfg.Logf = logs.logf
		cfg.Machines = failingFind{Machines: h.m, find: func() error {
			if recovered.Load() {
				return nil
			}
			return errors.New("find failed")
		}}
	}
	h.start()
	id := h.submit(time.Hour)
	if err := h.store.Append(t.Context(), task.Checkpoint{
		Run: id, Seq: 1, At: h.clock.Now(), By: "d1", Body: &task.MachineIntent{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.clock.Advance(5 * time.Second)
	h.settle()
	recovered.Store(true)
	h.clock.Advance(5 * time.Second)
	h.until(30, h.ended(id))
	h.stop()
	logs.check(t, []string{fmt.Sprintf("task %s: find failed", id),
		fmt.Sprintf("task %s: the workflow ended (1 more since the last line)", id)})
}

type failingFind struct {
	Machines
	find func() error
}

func (m failingFind) Find(context.Context, task.Run) (engine.Machine, bool, error) {
	return engine.Machine{}, false, m.find()
}

func TestWorkflowLogsFailuresByTime(t *testing.T) {
	for _, alternating := range []bool{false, true} {
		t.Run(fmt.Sprint("alternating=", alternating), func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			var phase atomic.Int32
			var logs failureLogs
			h.configure = func(cfg *Config) {
				cfg.Poll = 5 * time.Second
				cfg.Logf = logs.logf
				cfg.Machines = failingFind{Machines: h.m, find: func() error {
					n := phase.Load()
					switch n {
					case -1:
						return engine.ErrNotRunning
					case -2:
						h.stopSup()
						return errors.New("failure after shutdown")
					}
					if alternating {
						n %= 2
					}
					return fmt.Errorf("failure %d", n)
				}}
			}
			h.start()
			id := h.submit(time.Hour)
			if err := h.store.Append(t.Context(), task.Checkpoint{
				Run: id, Seq: 1, At: h.clock.Now(), By: "d1", Body: &task.MachineIntent{},
			}); err != nil {
				t.Fatal(err)
			}
			if err := tickRemoval(t, h); err != nil {
				t.Fatal(err)
			}
			h.settle()
			first := fmt.Sprintf("task %s: failure 0", id)
			for n := int32(1); n < 4; n++ {
				phase.Store(n)
				h.clock.Advance(5 * time.Second)
				h.settle()
			}
			logs.check(t, []string{first})
			phase.Store(4)
			h.clock.Advance(10*time.Minute - 15*time.Second)
			h.settle()
			latest := "failure 4"
			if alternating {
				latest = "failure 0"
			}
			second := fmt.Sprintf("task %s: %s (3 more since the last line)", id, latest)
			logs.check(t, []string{first, second})
			phase.Store(-1)
			h.clock.Advance(10 * time.Minute)
			h.settle()
			logs.check(t, []string{first, second})
			phase.Store(-2)
			h.clock.Advance(5 * time.Second)
			h.settle()
			h.stop()
			logs.check(t, []string{first, second})
		})
	}
}

type progressFailureEngine struct {
	Engine
	progress *atomic.Bool
}

func (e progressFailureEngine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if !e.progress.Load() || (req.Cmd == "sh" && len(req.Args) > 3 &&
		req.Args[1] == "look" && req.Args[3] == string(task.StageCommand)) {
		return engine.ExecResult{}, errors.New("e2b: 503 service unavailable")
	}
	return e.Engine.Exec(ctx, id, req)
}

func TestWorkflowLogsFailureAfterProgress(t *testing.T) {
	h := newHarness(t, script(0, 0))
	var progress atomic.Bool
	var logs failureLogs
	h.configure = func(cfg *Config) {
		cfg.Poll = 5 * time.Second
		cfg.Logf = logs.logf
		cfg.Engine = progressFailureEngine{Engine: cfg.Engine, progress: &progress}
	}
	h.start()
	id := h.submit(time.Hour)
	if err := h.store.Append(t.Context(), task.Checkpoint{
		Run: id, Seq: 1, At: h.clock.Now(), By: "d1", Body: &task.MachineIntent{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	h.settle()
	first := fmt.Sprintf("task %s: e2b: 503 service unavailable", id)
	logs.check(t, []string{first})
	before, err := h.store.Run(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	progress.Store(true)
	h.clock.Advance(5 * time.Second)
	h.settle()
	after, err := h.store.Run(t.Context(), id)
	if err != nil || len(after.Log) <= len(before.Log) {
		t.Fatalf("workflow did not make durable progress: %d -> %d checkpoints, %v", len(before.Log), len(after.Log), err)
	}
	logs.check(t, []string{first})
	h.clock.Advance(10*time.Minute - 5*time.Second)
	h.settle()
	logs.check(t, []string{first, first + " (1 more since the last line)"})
	h.stop()
	if h.sim.Starts(after.MachineID(), id, task.StageCommand) != 0 {
		t.Fatal("the failing look started the command")
	}
}
