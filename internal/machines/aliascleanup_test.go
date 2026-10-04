package machines_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestSaveImageWaitsForAliasCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t, fake.WithAliasCache())
		web := v.create(t, "web")
		m := imageManager(v, nil)
		v.e.FailNextSave(engine.ErrCheckpoint)
		start := time.Now()
		_, err := m.SaveImage(t.Context(), "web", "myimg")
		elapsed := time.Since(start)
		if !errors.Is(err, engine.ErrCheckpoint) {
			t.Fatalf("SaveImage = %v, want ErrCheckpoint", err)
		}

		if v.e.HasAlias(images.SavedTemplate("myimg")) {
			t.Error("failed save left its hidden alias behind")
		}
		if got := v.get(t, web.ID); got.State != engine.Running {
			t.Errorf("origin = %s, want running", got.State)
		}
		if elapsed != 70*time.Second {
			t.Errorf("cleanup took %s, want 70s", elapsed)
		}
		t.Logf("cleanup took %s", elapsed)
	})
}
