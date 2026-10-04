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
}

func (e *slowRoomEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	if slices.Contains(opts.States, engine.Running) {
		e.reads++
		if e.reads > 2 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return e.Engine.List(ctx, opts)
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
				if e.reads > 1 {
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
