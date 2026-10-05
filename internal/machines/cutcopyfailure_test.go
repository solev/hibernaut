package machines_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestCopyCutExhaustionKeepsOriginButRemovesCopyAndPin(t *testing.T) {
	for _, operation := range []string{"create", "fork"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newCopyGuest()
				inits, writes := 0, 0
				v := newEnv(t, fake.WithExec(func(ctx context.Context, em engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
					result, err := g.exec(ctx, em, home, req)
					if em.Metadata["hib.name"] != "web" {
						if req.Cmd == images.MachineInit {
							inits++
						} else if req.Cmd == "cat" {
							writes++
							return engine.ExecResult{}, engine.ErrConnectionLost
						}
					}
					return result, err
				}))
				web := v.create(t, "web")
				m := imageManager(v, nil)
				var err error
				if operation == "create" {
					if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
						t.Fatal(err)
					}
					_, err = m.Create(t.Context(), "web2", "myimg", machines.DefaultIdle)
				} else {
					_, err = m.Fork(t.Context(), "web", 1)
				}
				if !errors.Is(err, engine.ErrConnectionLost) || inits != 3 || writes != 3 {
					t.Errorf("copy error=%v init tries=%d write tries=%d", err, inits, writes)
				}
				if all := v.machines(t); len(all) != 1 || all[0].ID != web.ID {
					t.Error("exhaustion killed the origin or retained the failed copy")
				}
				if pins, err := os.ReadDir(filepath.Join(v.dir, "host-keys")); err != nil || len(pins) != 1 || pins[0].Name() != web.ID {
					t.Error("exhaustion left the failed copy's host-key pin")
				}
			})
		})
	}
}
