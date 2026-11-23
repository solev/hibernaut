package taskrun

import (
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func TestStopAfterReboot(t *testing.T) {
	for _, down := range []time.Duration{10 * time.Second, 3 * time.Minute, 8 * time.Hour} {
		t.Run(down.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := bubbleHarness(t, "hang")
				id := h.submit(2 * time.Hour)
				bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
				h.request(id, task.RequestCancel)
				bubbleUntil(t, h, 5*time.Second, func() bool { _, ok := h.run(id).Stop(); return ok })
				h.stop()
				if err := h.owner.Pause(context.Background(), h.machineID(id)); err != nil {
					t.Fatal(err)
				}
				if r := h.run(id); r.Machine != task.MachinePaused || !r.Frozen() {
					t.Fatalf("shutdown did not leave the machine paused and frozen: %+v", r.Freeze)
				}
				time.Sleep(down)
				h.clock.Advance(down)
				h.configure = func(cfg *Config) { cfg.Clock = RealClock{} }
				h.start()
				bubbleUntil(t, h, 75*time.Second, h.ended(id))
				h.stop()
				if how := stopResult(t, h.run(id)).How; how != "kill" {
					t.Fatalf("stop after %s down ended how=%q, want kill", down, how)
				}
				checkHonestWrapUp(t, h, id)
			})
		})
	}
}

func TestCollectAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := bubbleHarness(t, "collect-hang")
		id := h.submit(2 * time.Hour)
		bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
		h.request(id, task.RequestCancel)
		bubbleUntil(t, h, 5*time.Second, func() bool {
			last, _ := h.run(id).Last()
			return last.Step() == task.StepCollect && last.Kind() == task.Intent
		})
		h.stop()
		if h.run(id).Machine != task.MachineRunning {
			t.Fatal("collect restart test needs the machine still running")
		}
		time.Sleep(8 * time.Hour)
		h.clock.Advance(8 * time.Hour)
		h.configure = func(cfg *Config) { cfg.Clock = RealClock{} }
		h.start()
		bubbleUntil(t, h, 65*time.Second, h.ended(id))
		h.stop()
		checkHonestWrapUp(t, h, id)
	})
}

func TestStopLongAfterDaemonStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := bubbleHarness(t, "")
		time.Sleep(8 * time.Hour)
		h.clock.Advance(8 * time.Hour)
		id := h.submit(2 * time.Hour)
		bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
		h.request(id, task.RequestCancel)
		bubbleUntil(t, h, 75*time.Second, h.ended(id))
		h.stop()
		if how := stopResult(t, h.run(id)).How; how != "term" {
			t.Fatalf("healthy stop long after daemon start ended how=%q, want term", how)
		}
		checkHonestWrapUp(t, h, id)
	})
}

func checkHonestWrapUp(t *testing.T, h *harness, id task.RunID) {
	t.Helper()
	r := h.run(id)
	if st := r.Status(""); st.Outcome != task.Cancelled || st.Stage != task.StageCommand ||
		st.Detail != "hib task cancel by alice" {
		t.Fatalf("honest wrap-up status=%+v, want cancelled command without a wake warning", st)
	}
	if o, stage := r.Outcome(); o != task.Cancelled || stage != task.StageCommand {
		t.Fatalf("honest wrap-up outcome=%s at %s, want cancelled command", o, stage)
	}
	if unread := unreadResult(t, r); len(unread) != 0 {
		t.Fatalf("honest wrap-up unread=%q, want no unread tails", unread)
	}
	if r.Machine != task.MachinePaused || !r.Frozen() {
		t.Fatalf("honest wrap-up machine=%s frozen=%v, want paused and frozen", r.Machine, r.Frozen())
	}
	rc, err := (taskstore.MemoryLogs{M: h.store}).Open(id, task.StageCommand, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || string(data) != "did the work\n" {
		t.Fatalf("command tail=%q read=%v close=%v, want complete output", data, readErr, closeErr)
	}
	h.logs.check(t, nil)
	h.checkLog(id)
}
