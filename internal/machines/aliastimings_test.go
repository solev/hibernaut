package machines_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSaveAliasCleanupTimingsAndMessages(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cause       error
		delay       time.Duration
		rejected    bool
		elapsed     time.Duration
		unconfirmed bool
		pinned      bool
		aliasLeft   bool
	}{
		{name: "unknown outcome", cause: errors.New("save response lost"), elapsed: 70 * time.Second},
		{name: "long save", cause: engine.ErrCheckpoint, delay: 2 * time.Minute, elapsed: 125 * time.Second},
		{name: "not running", cause: fmt.Errorf("early failure: %w", engine.ErrNotRunning)},
		{name: "not found", cause: fmt.Errorf("early failure: %w", engine.ErrNotFound)},
		{name: "rejected before alias", cause: errors.New("node is busy"), rejected: true, elapsed: 90 * time.Second, unconfirmed: true},
		{name: "hidden beyond window", cause: engine.ErrCheckpoint, pinned: true, aliasLeft: true, elapsed: 90 * time.Second, unconfirmed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := time.Now()
				var v *env
				if tc.pinned {
					v = newEnvAt(t, func() time.Time { return clock }, fake.WithAliasCache())
				} else {
					v = newEnv(t, fake.WithAliasCache())
				}
				web := v.create(t, "web")
				m := imageManager(v, func(cfg *machines.Config) {
					if tc.pinned {
						cfg.Now = func() time.Time { return clock }
					}
				})
				if tc.rejected {
					v.e.RejectNextSave(tc.cause)
				} else {
					v.e.FailNextSave(tc.cause)
				}
				v.e.DelayNextSave(tc.delay)
				start := time.Now()
				_, err := m.SaveImage(t.Context(), "web", "myimg")
				elapsed := time.Since(start)
				if !errors.Is(err, tc.cause) {
					t.Fatalf("SaveImage = %v, want %v", err, tc.cause)
				}
				if elapsed != tc.elapsed {
					t.Errorf("save and cleanup took %s, want %s", elapsed, tc.elapsed)
				}
				if got := v.e.HasAlias(images.SavedTemplate("myimg")); got != tc.aliasLeft {
					t.Errorf("alias left=%v, want %v", got, tc.aliasLeft)
				}
				if got := v.get(t, web.ID); got.State != engine.Running {
					t.Errorf("origin = %s, want running", got.State)
				}
				if tc.unconfirmed {
					for _, want := range []string{"unconfirmed", "hidden by E2B", "Wait a few minutes", "hib image rm myimg, then try again"} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("SaveImage = %v, want %q", err, want)
						}
					}
					if strings.Contains(err.Error(), "Nothing was saved") {
						t.Errorf("unconfirmed removal reported as nothing saved: %v", err)
					}
				}
				if errors.Is(tc.cause, engine.ErrNotRunning) || errors.Is(tc.cause, engine.ErrNotFound) {
					if !strings.Contains(err.Error(), "Nothing was saved") || strings.Contains(err.Error(), "Don't retry") {
						t.Errorf("early refusal message = %v", err)
					}
				}
				t.Logf("save=%s cleanup=%s message=%s", tc.delay, elapsed-tc.delay, err)
			})
		})
	}
}
