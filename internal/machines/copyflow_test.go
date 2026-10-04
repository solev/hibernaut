package machines_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

func TestForkRoomChecksOnlyRemainingCopies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t, fake.WithExec(newCopyGuest().exec))
		v.create(t, "web")
		m := imageManager(v, func(cfg *machines.Config) {
			cfg.ReadRoom = func() (room.Host, error) {
				copies, err := v.e.List(t.Context(), engine.ListOptions{Metadata: map[string]string{"hib.forkof": "web"}})
				if err != nil {
					return room.Host{}, err
				}
				h, _ := roomy()
				h.FreePages = 512
				if len(copies) == 1 {
					h.FreePages = 256
				}
				return h, nil
			}
		})
		made, err := m.Fork(t.Context(), "web", 2)
		if err != nil || len(made) != 2 {
			t.Fatalf("Fork = %+v, %v, want two copies in exactly enough memory", made, err)
		}
	})
}

func TestCancelledSaveCleansAliasAndRestoresOrigin(t *testing.T) {
	v := newEnv(t)
	web := v.create(t, "web")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := &faultEngine{Engine: v.e}
	e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
		tmpl, err := v.e.SaveTemplate(ctx, id, alias)
		cancel()
		return tmpl, err
	}
	m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
	v.e.FailNextSave(engine.ErrCheckpoint)
	if _, err := m.SaveImage(ctx, "web", "myimg"); !errors.Is(err, engine.ErrCheckpoint) {
		t.Fatalf("SaveImage = %v", err)
	}
	noSavedAlias(t, v)
	if em := v.get(t, web.ID); em.State != engine.Running {
		t.Errorf("cancelled save stranded the origin %s", em.State)
	}
}

func TestBuiltInWinsOverShadowedSavedImage(t *testing.T) {
	v := newEnv(t, fake.WithExec(newCopyGuest().exec))
	web := v.create(t, "web")
	if _, err := v.e.SaveTemplate(t.Context(), web.ID, "hibimg-dev"); err != nil {
		t.Fatal(err)
	}
	copy, err := imageManager(v, nil).Create(t.Context(), "copy", "dev", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if em := v.get(t, copy.ID); em.Template != images.Template("dev") {
		t.Errorf("built-in lost to saved image: %+v", em)
	}
}
