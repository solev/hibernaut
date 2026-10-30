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

func TestRemovalLogsChangedFailures(t *testing.T) {
	h, id := keptRemoval(t)
	var phase atomic.Int32
	var logs failureLogs
	h.configure = func(cfg *Config) {
		cfg.Logf = logs.logf
		cfg.Machines = removeMachines{Machines: h.m, remove: func(context.Context, task.Run) error {
			switch phase.Load() {
			case 0:
				return errors.New("first failure")
			case 1:
				return errors.New("new failure")
			default:
				h.stopSup()
				return context.Canceled
			}
		}}
	}
	h.request(id, task.RequestRemove)
	h.start()
	for _, p := range []int32{0, 0, 0, 0, 1, 1, 2} {
		phase.Store(p)
		if err := tickRemoval(t, h); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { h.sup.Wait(); close(done) }()
		receiveRemoval(t, done, "removal attempt did not return")
	}
	h.stop()
	logs.check(t, []string{
		fmt.Sprintf("task %s removal: first failure", id.Task),
		fmt.Sprintf("task %s removal: new failure", id.Task),
	})
}

func TestRemovalLoggingDoesNotBlockTick(t *testing.T) {
	h, id := keptRemoval(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.configure = func(cfg *Config) {
		cfg.Logf = func(string, ...any) {
			close(started)
			select {
			case <-release:
			case <-t.Context().Done():
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

type failingFind struct {
	Machines
	find func() error
}

func (m failingFind) Find(context.Context, task.Run) (engine.Machine, bool, error) {
	return engine.Machine{}, false, m.find()
}

func TestWorkflowLogsChangedFailures(t *testing.T) {
	h := newHarness(t, script(0, 0))
	var phase, calls atomic.Int32
	var logs failureLogs
	h.configure = func(cfg *Config) {
		cfg.Logf = logs.logf
		cfg.Machines = failingFind{Machines: h.m, find: func() error {
			calls.Add(1)
			switch phase.Load() {
			case 0:
				return errors.New("host is going to sleep")
			case 1:
				return errors.New("new failure")
			case 2:
				return engine.ErrNotRunning
			default:
				h.stopSup()
				return errors.New("failure after shutdown")
			}
		}}
	}
	h.start()
	id := h.submit(time.Hour)
	for _, p := range []int32{0, 0, 0, 0, 1, 1, 2, 2, 3} {
		phase.Store(p)
		h.tick()
	}
	h.stop()
	if calls.Load() < 9 {
		t.Fatalf("workflow made %d attempts, want at least 9", calls.Load())
	}
	logs.check(t, []string{
		fmt.Sprintf("task %s: host is going to sleep", id),
		fmt.Sprintf("task %s: new failure", id),
	})
}
