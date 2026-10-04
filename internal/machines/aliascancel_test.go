package machines_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestAliasCleanupKeepsLockAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t, fake.WithAliasCache())
		v.create(t, "web")
		m := imageManager(v, nil)
		v.e.FailNextSave(engine.ErrCheckpoint)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := m.SaveImage(ctx, "web", "myimg")
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := m.SweepForks(t.Context()); !errors.Is(err, machines.ErrBusy) {
			t.Errorf("sweep during alias cleanup = %v, want ErrBusy", err)
		}
		if all, err := m.List(t.Context(), ""); err != nil || len(all) != 1 || all[0].State != string(engine.Running) {
			t.Errorf("List while cleanup holds lock = %+v, %v", all, err)
		}
		if err := <-done; !errors.Is(err, engine.ErrCheckpoint) {
			t.Fatal(err)
		}
		if v.e.HasAlias(images.SavedTemplate("myimg")) {
			t.Error("request cancellation interrupted alias cleanup")
		}
	})
}
