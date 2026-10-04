package machines_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSaveReadbackFailureRemovesVisibleAlias(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t, fake.WithAliasCache())
		v.create(t, "web")
		lost := errors.New("save read-back failed")
		e := &faultEngine{Engine: v.e, save: func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
			if _, err := v.e.SaveTemplate(ctx, id, alias); err != nil {
				return engine.TemplateInfo{}, err
			}
			return engine.TemplateInfo{}, lost
		}}
		m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
		start := time.Now()
		_, err := m.SaveImage(t.Context(), "web", "myimg")
		if !errors.Is(err, lost) || !strings.Contains(err.Error(), "couldn't confirm the save") {
			t.Fatalf("SaveImage = %v", err)
		}
		if v.e.HasAlias(images.SavedTemplate("myimg")) || time.Since(start) != 0 {
			t.Errorf("visible successful-save alias wasn't removed immediately: %s", time.Since(start))
		}
	})
}
