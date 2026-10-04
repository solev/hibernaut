package machines_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

func TestFailedResumeReportsBestEffortPauseOutcome(t *testing.T) {
	for _, lands := range []bool{false, true} {
		t.Run(map[bool]string{false: "already paused", true: "pause refused"}[lands], func(t *testing.T) {
			v := newEnv(t)
			web := v.create(t, "web")
			if err := v.m.Pause(t.Context(), "web"); err != nil {
				t.Fatal(err)
			}
			lost := errors.New("resume response lost")
			refused := errors.New("restoring pause refused")
			e := &faultEngine{Engine: v.e}
			e.resume = func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
				if lands {
					if _, err := v.e.Resume(ctx, id, timeout); err != nil {
						t.Fatal(err)
					}
				}
				return engine.Machine{}, lost
			}
			e.pause = func(context.Context, string) error {
				if lands {
					return refused
				}
				return engine.ErrNotRunning
			}
			m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
			_, err := m.SaveImage(t.Context(), "web", "myimg")
			if !errors.Is(err, lost) {
				t.Fatalf("SaveImage = %v, want resume failure", err)
			}
			if lands {
				if !errors.Is(err, refused) || !strings.Contains(err.Error(), "web is running") || strings.Contains(err.Error(), "web is paused") {
					t.Errorf("failed best-effort pause state = %v", err)
				}
			} else if errors.Is(err, engine.ErrNotRunning) || !strings.Contains(err.Error(), "web is paused at a safe snapshot") {
				t.Errorf("already-paused recovery = %v", err)
			}
			want := engine.Paused
			if lands {
				want = engine.Running
			}
			if got := v.get(t, web.ID); got.State != want {
				t.Errorf("origin=%s, want %s", got.State, want)
			}
		})
	}
}
