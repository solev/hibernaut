package taskrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
)

func TestUnreadExcludesExitedStages(t *testing.T) {
	for _, reason := range []string{"unproven", "paused", "gone", "budget", "look"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				mode := "collect-hang"
				if reason == "unproven" {
					mode = "garbage"
				} else if reason == "look" {
					mode = "collect-garbage"
				}
				h := bubbleHarness(t, mode)
				id := h.submit(2 * time.Hour)
				bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
				h.request(id, task.RequestCancel)
				if reason != "unproven" && reason != "look" {
					bubbleUntil(t, h, 5*time.Second, func() bool {
						last, _ := h.run(id).Last()
						return last.Step() == task.StepCollect && last.Kind() == task.Intent
					})
					h.stop()
					mid := h.machineID(id)
					if reason == "paused" {
						if err := h.owner.Pause(context.Background(), mid); err != nil {
							t.Fatal(err)
						}
					} else if reason == "gone" {
						if err := h.sim.Kill(context.Background(), mid); err != nil {
							t.Fatal(err)
						}
					}
					h.configure = func(cfg *Config) { cfg.Clock = RealClock{} }
					h.start()
					if reason == "budget" {
						time.Sleep(61 * time.Second)
						h.clock.Advance(61 * time.Second)
					}
				}
				bubbleUntil(t, h, 75*time.Second, h.ended(id))
				h.stop()
				r := h.run(id)
				unread := unreadResult(t, r)
				if len(unread) != 1 || !strings.HasPrefix(unread[0], "command: ") {
					t.Fatalf("unread=%q, want only the command tail; setup already exited", unread)
				}
				if o, stage := r.Outcome(); o != task.Cancelled || stage != task.StageCommand ||
					r.Status("").Outcome != task.Cancelled {
					t.Fatalf("unread wrap-up outcome=%s at %s, status=%+v", o, stage, r.Status(""))
				}
				if reason == "gone" {
					if _, err := h.sim.Get(context.Background(), r.MachineID()); !errors.Is(err, engine.ErrNotFound) {
						t.Fatalf("removed machine lookup=%v, want not found", err)
					}
				} else if r.Machine != task.MachinePaused || !r.Frozen() {
					t.Fatalf("unread wrap-up machine=%s frozen=%v, want paused and frozen", r.Machine, r.Frozen())
				}
				h.logs.check(t, nil)
				h.checkLog(id)
			})
		})
	}
}
