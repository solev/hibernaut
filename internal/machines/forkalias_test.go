package machines_test

import (
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestForkWaitsForFailedSaveAlias(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "cache ages", true: "cache pinned"}[pinned], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				var v *env
				if pinned {
					v = newEnvAt(t, func() time.Time { return start }, fake.WithAliasCache())
				} else {
					v = newEnv(t, fake.WithAliasCache())
				}
				web := v.create(t, "web")
				m := imageManager(v, nil)
				alias := images.ForkTemplate(web.ID, time.Now())
				v.e.FailNextSave(engine.ErrCheckpoint)
				begin := time.Now()
				made, err := m.Fork(t.Context(), "web", 2)
				elapsed := time.Since(begin)
				if len(made) != 0 || !errors.Is(err, engine.ErrCheckpoint) {
					t.Fatalf("Fork = %+v, %v", made, err)
				}
				want := 70 * time.Second
				if pinned {
					want = 90 * time.Second
					for _, text := range []string{"unconfirmed", "Fork template " + alias + " may be left"} {
						if !strings.Contains(err.Error(), text) {
							t.Errorf("Fork = %v, want %q", err, text)
						}
					}
					if sweep := m.SweepForks(t.Context()); sweep != nil {
						t.Fatal(sweep)
					}
				}
				if elapsed != want || v.e.HasAlias(alias) != pinned {
					t.Errorf("cleanup elapsed=%s alias left=%v, want %s and %v", elapsed, v.e.HasAlias(alias), want, pinned)
				}
				if got := v.get(t, web.ID); got.State != engine.Running {
					t.Errorf("origin = %s, want running", got.State)
				}
				t.Logf("cleanup=%s message=%s", elapsed, err)
			})
		})
	}
}
