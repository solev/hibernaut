package machines_test

import (
	"errors"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestSaveRestoresOriginWhileAliasCleanupWaits(t *testing.T) {
	for _, state := range []engine.State{engine.Running, engine.Paused} {
		t.Run(string(state), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				v := newEnv(t, fake.WithAliasCache())
				web := v.create(t, "web")
				cause := engine.ErrCheckpoint
				if state == engine.Paused {
					if err := v.m.Pause(t.Context(), "web"); err != nil {
						t.Fatal(err)
					}
					cause = errors.New("save response lost")
				}
				v.e.FailNextSave(cause)
				m := imageManager(v, nil)
				done := make(chan error, 1)
				go func() {
					_, err := m.SaveImage(t.Context(), "web", "myimg")
					done <- err
				}()
				synctest.Wait()
				if !v.e.HasAlias(images.SavedTemplate("myimg")) {
					t.Error("cleanup didn't wait for the hidden alias")
				}
				if got := v.get(t, web.ID); got.State != state {
					t.Errorf("origin during cleanup = %s, want %s", got.State, state)
				}
				if err := <-done; !errors.Is(err, cause) {
					t.Fatalf("SaveImage = %v, want %v", err, cause)
				}
				if v.e.HasAlias(images.SavedTemplate("myimg")) {
					t.Error("cleanup left its alias behind")
				}
			})
		})
	}
}
