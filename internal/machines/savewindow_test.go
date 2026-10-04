package machines_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

func TestGuardProtectsLongSaveWithShortIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		web := v.createIdle(t, "web", time.Minute)
		e := &faultEngine{Engine: v.e}
		e.resume = func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
			if timeout < 10*time.Minute {
				t.Errorf("resume timeout=%s, want at least 10m", timeout)
			}
			return v.e.Resume(ctx, id, timeout)
		}
		e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
			em, err := v.e.Get(ctx, id)
			if err != nil {
				return engine.TemplateInfo{}, err
			}
			if em.EndAt.Before(time.Now().Add(10 * time.Minute)) {
				t.Errorf("renewed timeout ends at %s, want at least 10m from now", em.EndAt)
			}
			if _, bounded := ctx.Deadline(); bounded {
				t.Error("SaveTemplate must not acquire a guard deadline")
			}
			return v.e.SaveTemplate(ctx, id, alias)
		}
		m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
		v.e.DelayNextSave(3 * time.Minute)
		if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
			t.Fatal(err)
		}
		if got := v.get(t, web.ID); got.State != engine.Running {
			t.Errorf("origin after long save=%s, want running", got.State)
		}
	})
}
