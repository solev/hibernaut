package taskrun

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
)

type prepareMachines struct {
	Machines
	prepare func(context.Context, task.Run) error
}

func (m prepareMachines) Prepare(ctx context.Context, r task.Run) error {
	return m.prepare(ctx, r)
}

type prepareUpload struct {
	Engine
	events *[]string
}

func (e prepareUpload) Upload(ctx context.Context, id, user string, files []engine.File) error {
	*e.events = append(*e.events, "upload")
	return e.Engine.Upload(ctx, id, user, files)
}

func TestPrepareEveryUpload(t *testing.T) {
	for _, mode := range []string{"create", "adopt", "resume", "follow-up", "retry"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			id := h.submit(time.Hour)
			if mode == "adopt" || mode == "resume" {
				m, err := h.m.Create(t.Context(), h.run(id), time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "resume" {
					if err := h.sim.Pause(t.Context(), m.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			var events []string
			h.configure = func(cfg *Config) {
				cfg.Machines = prepareMachines{Machines: h.m, prepare: func(ctx context.Context, r task.Run) error {
					events = append(events, "prepare")
					m, err := h.sim.Get(ctx, r.MachineID())
					if err != nil || m.State != engine.Running {
						t.Errorf("Prepare has no running machine: %+v, %v", m, err)
					}
					if mode == "retry" && len(events) == 1 {
						return errors.New("prepare unavailable")
					}
					return nil
				}}
				cfg.Engine = prepareUpload{Engine: cfg.Engine, events: &events}
			}
			h.start()
			h.until(30, h.ended(id))
			h.stop()
			want := []string{"prepare", "upload"}
			if mode == "retry" {
				want = []string{"prepare", "prepare", "upload"}
			}
			if mode == "follow-up" {
				rc, err := h.store.Submit(t.Context(), task.Submission{RequestID: "prepare-follow",
					Task: id.Task, Spec: h.run(id).Spec, Actor: alice, At: h.clock.Now()}, nil)
				if err != nil {
					t.Fatal(err)
				}
				h.start()
				h.until(30, h.ended(rc.Run))
				h.stop()
				want = append(want, "prepare", "upload")
				if outcome(h.run(rc.Run)) != task.Succeeded {
					t.Fatal("prepared follow-up did not succeed")
				}
			}
			if !slices.Equal(events, want) || outcome(h.run(id)) != task.Succeeded {
				t.Fatalf("preparation and upload = %q, want %q; outcome=%s", events, want, outcome(h.run(id)))
			}
		})
	}
}
