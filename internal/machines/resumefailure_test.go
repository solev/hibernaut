package machines_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

func TestFailedResumeRestoresOnlyPausedOrigins(t *testing.T) {
	for _, original := range []engine.State{engine.Paused, engine.Running} {
		t.Run(string(original), func(t *testing.T) {
			v := newEnv(t)
			web := v.create(t, "web")
			if original == engine.Paused {
				if err := v.m.Pause(t.Context(), "web"); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			lost := errors.New("resume read-back failed")
			resumes, pauses := 0, 0
			e := &faultEngine{Engine: v.e}
			e.resume = func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
				resumes++
				if _, err := v.e.Resume(ctx, id, timeout); err != nil {
					t.Fatal(err)
				}
				cancel()
				return engine.Machine{}, lost
			}
			e.pause = func(ctx context.Context, id string) error {
				pauses++
				if resumes != 0 {
					if _, bounded := ctx.Deadline(); !bounded || ctx.Err() != nil {
						t.Error("restoring pause needs its own bounded, uncancelled context")
					}
				}
				return v.e.Pause(ctx, id)
			}
			m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
			_, err := m.SaveImage(ctx, "web", "myimg")
			if !errors.Is(err, lost) || !strings.Contains(err.Error(), "Nothing was saved") {
				t.Fatalf("SaveImage = %v", err)
			}
			if resumes != 1 || pauses != 1 {
				t.Errorf("resume calls=%d pause calls=%d, want one each", resumes, pauses)
			}
			if got := v.get(t, web.ID); got.State != original {
				t.Errorf("origin=%s, want %s", got.State, original)
			}
			want := "web is running"
			if original == engine.Paused {
				want = "web is paused at a safe snapshot. Wake it with: ssh web.hib."
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("SaveImage = %v, want %q", err, want)
			}
			if v.e.HasAlias(images.SavedTemplate("myimg")) {
				t.Error("failed resume saved an image")
			}
		})
	}
}
