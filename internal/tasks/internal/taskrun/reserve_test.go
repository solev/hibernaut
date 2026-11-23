package taskrun

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
)

func TestStageRuntimeReserve(t *testing.T) {
	h := newHarness(t, script(0, 0))
	h.start()
	id := h.submit(10 * time.Minute)
	h.until(30, h.ended(id))
	h.stop()
	for _, s := range task.Stages() {
		want := "--property=RuntimeMaxSec=600"
		if s == task.StageCommand {
			want = "--property=RuntimeMaxSec=480"
		}
		found := false
		for _, argv := range h.sim.Calls {
			if slices.Contains(argv, "--unit="+taskguest.Unit(id, s)) {
				found = true
				if !slices.Contains(argv, want) {
					t.Errorf("%s launch lacks %s: %q", s, want, argv)
				}
			}
		}
		if !found {
			t.Errorf("%s was not launched", s)
		}
	}
}

type reserveRestart struct {
	Engine
	h *harness
}

func (e reserveRestart) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if len(req.Args) > 3 && req.Args[1] == "look" && req.Args[3] == "command" {
		e.h.stopSup()
		return engine.ExecResult{}, context.Canceled
	}
	return e.Engine.Exec(ctx, id, req)
}

func TestCommandWithoutTimeIsNotLaunched(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "setup", true: "restart"}[restart], func(t *testing.T) {
			h := newHarness(t, func(id task.RunID, s task.Stage) guestsim.Behavior {
				b := script(0, 0)(id, s)
				if s == task.StageSetup && !restart {
					b.Runs = 46 * time.Second
				}
				return b
			})
			if restart {
				h.configure = func(cfg *Config) { cfg.Engine = reserveRestart{Engine: cfg.Engine, h: h} }
			}
			h.start()
			id := h.submit(time.Minute)
			if restart {
				h.until(10, func() bool {
					r := h.run(id)
					for _, c := range r.Log {
						if b, ok := c.Body.(*task.StageIntent); ok && b.Stage == task.StageCommand {
							return true
						}
					}
					return false
				})
				h.stop()
				h.clock.Advance(46 * time.Second)
				h.configure = nil
				h.start()
			}
			h.until(80, h.ended(id))
			h.stop()
			r := h.run(id)
			o, stage := r.Outcome()
			starts := h.sim.Starts(r.MachineID(), id, task.StageCommand)
			if o != task.TimedOut || stage != task.StageCommand || starts != 0 {
				t.Fatalf("outcome=%s at %s, command starts=%d; want timed_out at command without launch",
					o, stage, starts)
			}
		})
	}
}
