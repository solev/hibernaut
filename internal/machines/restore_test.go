package machines_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSaveFinalRestoreFailureRemovesImageAndReadsState(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "unknown"}[unknown], func(t *testing.T) {
			v := newEnv(t)
			v.create(t, "web")
			if err := v.m.Pause(t.Context(), "web"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			e := &faultEngine{Engine: v.e, pause: func(context.Context, string) error {
				cancel()
				return errors.New("restoring pause failed")
			}}
			if unknown {
				e.get = func(ctx context.Context, id string) (engine.Machine, error) {
					if _, bounded := ctx.Deadline(); !bounded || ctx.Err() != nil {
						t.Error("restore state must be read with its own bounded, uncancelled context")
					}
					return engine.Machine{}, errors.New("reading state failed")
				}
			}
			m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
			img, err := m.SaveImage(ctx, "web", "myimg")
			want := "web is running"
			if unknown {
				want = "state is unknown"
			}
			if img.Name != "" || err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Nothing was saved") || strings.Contains(err.Error(), "is paused") {
				t.Fatalf("SaveImage = %+v, %v", img, err)
			}
			noSavedAlias(t, v)
		})
	}
}
