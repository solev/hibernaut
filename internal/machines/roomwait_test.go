package machines_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

type slowRoomEngine struct {
	engine.Engine
	reads int
	saved bool
}

func (e *slowRoomEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	if e.saved && slices.Contains(opts.States, engine.Running) {
		e.reads++
		if e.reads > 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return e.Engine.List(ctx, opts)
}

func (e *slowRoomEngine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	tmpl, err := e.Engine.SaveTemplate(ctx, id, alias)
	e.saved = err == nil
	return tmpl, err
}

func TestForkRoomWaitBoundsEngineReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t, fake.WithExec(newCopyGuest().exec))
		v.create(t, "web")
		e := &slowRoomEngine{Engine: v.e}
		m := imageManager(v, func(cfg *machines.Config) {
			cfg.Engine = e
			cfg.ReadRoom = func() (room.Host, error) {
				h, _ := roomy()
				if e.saved {
					h.FreePages = 0
				}
				return h, nil
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		at := time.Now()
		_, err := m.Fork(ctx, "web", 1)
		if !errors.Is(err, room.ErrFull) || time.Since(at) != 15*time.Second {
			t.Fatalf("Fork waited %v and returned %v, want 15s and ErrFull", time.Since(at), err)
		}
	})
}
