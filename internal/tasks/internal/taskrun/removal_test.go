package taskrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

type removeMachines struct {
	Machines
	remove func(context.Context, task.Run) error
}

func (m removeMachines) Remove(ctx context.Context, r task.Run) error {
	return m.remove(ctx, r)
}

func waitRemoval(t *testing.T, message string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

func receiveRemoval[T any](t *testing.T, ch <-chan T, message string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal(message)
		var zero T
		return zero
	}
}

func tickRemoval(t *testing.T, h *harness) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- h.sup.Tick(ctx) }()
	return receiveRemoval(t, result, "Tick blocked on removal")
}

func (h *harness) removalDone(id task.RunID) bool {
	h.t.Helper()
	got, err := h.store.Task(context.Background(), id.Task)
	if err != nil {
		h.t.Fatal(err)
	}
	return got.Removal != nil && !got.Removal.Done.IsZero()
}

func (h *harness) checkRemoved(id task.RunID) {
	h.t.Helper()
	got, err := h.store.Task(context.Background(), id.Task)
	if err != nil || got.Removal == nil || got.Removal.Done.IsZero() ||
		got.Removal.Why != "rm" || len(got.Requests) == 0 ||
		got.Removal.Request != got.Requests[len(got.Requests)-1].ID {
		h.t.Fatalf("task removal = %+v, %v, want completed rm with its request ID", got.Removal, err)
	}
	listed, err := h.store.Tasks(context.Background())
	if err != nil || slices.ContainsFunc(listed, func(t task.Task) bool { return t.ID == id.Task }) {
		h.t.Fatalf("removed task still listed, %v", err)
	}
	ms, err := h.sim.List(context.Background(), engine.ListOptions{
		Metadata: map[string]string{task.Label: string(id.Task)},
	})
	if err != nil || len(ms) != 0 {
		h.t.Fatalf("removed task has %d machines, %v", len(ms), err)
	}
}

func keptRemoval(t *testing.T) (*harness, task.RunID) {
	t.Helper()
	h := newHarness(t, script(0, 0))
	h.start()
	id := h.submit(time.Hour)
	h.until(30, h.ended(id))
	h.stop()
	if h.run(id).Machine != task.MachinePaused {
		t.Fatal("the completed run did not keep a paused machine")
	}
	return h, id
}

func TestRemoveKeptTask(t *testing.T) {
	h, id := keptRemoval(t)
	h.request(id, task.RequestRemove)
	h.start()
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	waitRemoval(t, "one Tick did not complete the kept task's removal", func() bool {
		return h.removalDone(id)
	})
	h.checkRemoved(id)
}

func TestRemoveQueuedCancelledTask(t *testing.T) {
	h := newHarness(t, script(0, 0))
	h.m.Room = 0
	h.start()
	id := h.submit(time.Hour)
	h.request(id, task.RequestCancel)
	h.until(10, h.ended(id))
	h.stop()
	if h.run(id).MachineID() != "" || outcome(h.run(id)) != task.Cancelled {
		t.Fatal("the queued run was not cancelled without a machine")
	}
	h.request(id, task.RequestRemove)
	h.start()
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	waitRemoval(t, "one Tick did not remove the cancelled queued task", func() bool {
		return h.removalDone(id)
	})
	h.checkRemoved(id)
	if ms, err := h.sim.List(t.Context(), engine.ListOptions{}); err != nil || len(ms) != 0 {
		t.Fatalf("queued cancellation created a machine, %v", err)
	}
}

func TestRemoveDoesNotBlockRuns(t *testing.T) {
	h, id := keptRemoval(t)
	h.sim.Script = script(time.Hour, 0)
	started, release := make(chan task.RunID, 4), make(chan struct{})
	var calls atomic.Int32
	h.configure = func(cfg *Config) {
		cfg.Machines = removeMachines{Machines: h.m, remove: func(ctx context.Context, r task.Run) error {
			calls.Add(1)
			select {
			case started <- r.ID:
			default:
			}
			select {
			case <-release:
				return h.m.Remove(ctx, r)
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}}
	}
	h.start()
	live := h.submit(2 * time.Hour)
	h.until(30, h.launched(live, task.StageCommand))
	h.request(id, task.RequestRemove)
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	if got := receiveRemoval(t, started, "Remove never started"); got != id {
		t.Fatalf("Remove got run %s, want %s", got, id)
	}
	queued := h.submit(2 * time.Hour)
	h.request(live, task.RequestCancel)
	h.until(30, func() bool { return h.run(live).Ended() && h.launched(queued, task.StageCommand)() })
	if outcome(h.run(live)) != task.Cancelled {
		t.Fatal("the live run was not nudged while Remove hung")
	}
	for range 3 {
		if err := tickRemoval(t, h); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("hung removal started %d Removes, want 1", got)
	}
	close(release)
	waitRemoval(t, "released Remove did not record done", func() bool { return h.removalDone(id) })
	h.checkRemoved(id)
}

func TestRemoveRetriesFailure(t *testing.T) {
	h, id := keptRemoval(t)
	failure := errors.New("remove failed")
	messages := make(chan string, 8)
	var calls atomic.Int32
	h.configure = func(cfg *Config) {
		cfg.Logf = func(format string, args ...any) { messages <- fmt.Sprintf(format, args...) }
		cfg.Machines = removeMachines{Machines: h.m, remove: func(ctx context.Context, r task.Run) error {
			if calls.Add(1) == 1 {
				return failure
			}
			return h.m.Remove(ctx, r)
		}}
	}
	h.request(id, task.RequestRemove)
	h.start()
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	if got := receiveRemoval(t, messages, "the failed Remove was not logged"); !strings.Contains(got, failure.Error()) {
		t.Fatalf("failure log = %q", got)
	}
	view, err := h.store.Task(t.Context(), id.Task)
	if err != nil || view.Removal == nil || !view.Removal.Done.IsZero() {
		t.Fatalf("failed removal = %+v, %v, want open", view.Removal, err)
	}
	if _, err := h.sim.Get(t.Context(), h.run(id).MachineID()); err != nil {
		t.Fatal("failed Remove lost the machine")
	}
	waitRemoval(t, "later Tick did not retry Remove", func() bool {
		if err := tickRemoval(t, h); err != nil {
			t.Fatal(err)
		}
		return h.removalDone(id)
	})
	if got := calls.Load(); got != 2 {
		t.Fatalf("Remove called %d times, want one failure and one success", got)
	}
	h.checkRemoved(id)
}

func TestRemoveReentersAfterRestart(t *testing.T) {
	for _, gone := range []bool{false, true} {
		t.Run(fmt.Sprint("machine-already-gone=", gone), func(t *testing.T) {
			h, id := keptRemoval(t)
			h.request(id, task.RequestRemove)
			view, err := h.store.Task(t.Context(), id.Task)
			if err != nil {
				t.Fatal(err)
			}
			begun := task.Removal{Why: "rm", Request: view.Requests[0].ID, At: h.clock.Now()}
			if err := h.store.Retire(t.Context(), id.Task, begun); err != nil {
				t.Fatal(err)
			}
			if gone {
				if err := h.m.Remove(t.Context(), h.run(id)); err != nil {
					t.Fatal(err)
				}
			}
			h.clock.Advance(time.Hour)
			h.start()
			if err := tickRemoval(t, h); err != nil {
				t.Fatal(err)
			}
			waitRemoval(t, "first Tick after restart left the removal open", func() bool {
				return h.removalDone(id)
			})
			h.checkRemoved(id)
			view, err = h.store.Task(t.Context(), id.Task)
			if err != nil || view.Removal.At != begun.At {
				t.Fatalf("restart replaced the begun removal = %+v, %v", view.Removal, err)
			}
		})
	}
}

func TestRemoveWaitsForCancelledAction(t *testing.T) {
	h, id := keptRemoval(t)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.configure = func(cfg *Config) {
		cfg.Machines = removeMachines{Machines: h.m, remove: func(ctx context.Context, r task.Run) error {
			close(started)
			<-ctx.Done()
			close(cancelled)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				return errors.New("test did not release cancelled Remove")
			}
			return context.Cause(ctx)
		}}
	}
	h.request(id, task.RequestRemove)
	h.start()
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	receiveRemoval(t, started, "Remove never started")
	h.stopSup()
	receiveRemoval(t, cancelled, "Remove did not receive the daemon's cancelled context")
	waited := make(chan struct{})
	go func() { h.sup.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned before Remove")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	receiveRemoval(t, waited, "Wait did not return after Remove")
	view, err := h.store.Task(t.Context(), id.Task)
	if err != nil || view.Removal == nil || !view.Removal.Done.IsZero() {
		t.Fatalf("cancelled removal = %+v, %v, want open", view.Removal, err)
	}
}

func TestNoTaskRetentionYet(t *testing.T) {
	h, id := keptRemoval(t)
	h.clock.Advance(task.KeepFor + time.Hour)
	h.start()
	for range 3 {
		h.tick()
	}
	h.stop()
	view, err := h.store.Task(t.Context(), id.Task)
	if err != nil || view.Removal != nil {
		t.Fatalf("age alone removed the task = %+v, %v", view.Removal, err)
	}
	listed, err := h.store.Tasks(t.Context())
	if err != nil || len(listed) != 1 || listed[0].ID != id.Task {
		t.Fatalf("retained task missing from Tasks = %v, %v", listed, err)
	}
	if m, err := h.sim.Get(t.Context(), h.run(id).MachineID()); err != nil || m.State != engine.Paused {
		t.Fatalf("retained machine = %v, %v, want paused", m, err)
	}
}

func TestRemoveCancelledAfterSuccessStaysOpen(t *testing.T) {
	h, id := keptRemoval(t)
	h.configure = func(cfg *Config) {
		cfg.Machines = removeMachines{Machines: h.m, remove: func(ctx context.Context, r task.Run) error {
			if err := h.m.Remove(ctx, r); err != nil {
				return err
			}
			h.stopSup()
			return nil
		}}
	}
	h.request(id, task.RequestRemove)
	h.start()
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	waitRemoval(t, "successful Remove never cancelled the daemon", func() bool {
		return h.sup.life.Err() != nil
	})
	h.stop()
	view, err := h.store.Task(t.Context(), id.Task)
	if err != nil || view.Removal == nil || !view.Removal.Done.IsZero() {
		t.Fatalf("removal after cancelled success = %+v, %v, want open", view.Removal, err)
	}
	h.configure = nil
	h.start()
	if err := tickRemoval(t, h); err != nil {
		t.Fatal(err)
	}
	waitRemoval(t, "restart did not complete the cancelled success", func() bool {
		return h.removalDone(id)
	})
	h.checkRemoved(id)
}

type heldGrantMachines struct{ Machines }

func (m heldGrantMachines) Create(ctx context.Context, r task.Run, d time.Duration) (engine.Machine, error) {
	<-ctx.Done()
	return engine.Machine{}, context.Cause(ctx)
}

func TestRemoveOnEveryTick(t *testing.T) {
	for _, mode := range []string{"grant-open", "nothing-fits"} {
		t.Run(mode, func(t *testing.T) {
			h, id := keptRemoval(t)
			h.m.Room = 0
			other := h.submit(time.Hour)
			if mode == "grant-open" {
				if err := h.store.Append(t.Context(), task.Checkpoint{Run: other, Seq: 1,
					At: h.clock.Now(), By: "old", Body: &task.MachineIntent{}}); err != nil {
					t.Fatal(err)
				}
				h.configure = func(cfg *Config) { cfg.Machines = heldGrantMachines{Machines: h.m} }
			}
			h.request(id, task.RequestRemove)
			h.start()
			if err := tickRemoval(t, h); err != nil {
				t.Fatal(err)
			}
			waitRemoval(t, "Tick skipped removal with "+mode, func() bool { return h.removalDone(id) })
			h.checkRemoved(id)
		})
	}
}

type failedRemovalRead struct {
	taskstore.Store
	err error
}

func (s failedRemovalRead) Removing(context.Context) ([]task.Task, error) { return nil, s.err }

type failedGrant struct {
	Machines
	err error
}

func (m failedGrant) Fits(context.Context) (func(task.Run) error, error) { return nil, m.err }

func TestRemovalReadErrorDoesNotStopGrant(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("grant-fails=", fail), func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			readErr, grantErr := errors.New("removal read failed"), errors.New("grant failed")
			h.configure = func(cfg *Config) {
				cfg.Store = failedRemovalRead{Store: h.store, err: readErr}
				if fail {
					cfg.Machines = failedGrant{Machines: h.m, err: grantErr}
				}
			}
			id := h.submit(time.Hour)
			h.start()
			err := tickRemoval(t, h)
			if !errors.Is(err, readErr) || errors.Is(err, grantErr) != fail {
				t.Fatalf("Tick = %v, want removal error and grant error=%v", err, fail)
			}
			if !fail {
				waitRemoval(t, "failed Removing read stopped the grant", func() bool {
					_, granted := h.run(id).Deadline()
					return granted
				})
			}
		})
	}
}
