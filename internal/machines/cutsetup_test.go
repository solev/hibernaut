package machines_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
)

func TestCreateRetriesCutAfterInitSideEffects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newCopyGuest()
		inits := 0
		v := newEnv(t, fake.WithExec(func(ctx context.Context, em engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
			result, err := g.exec(ctx, em, home, req)
			if req.Cmd == images.MachineInit {
				inits++
				if inits == 1 {
					return engine.ExecResult{}, fmt.Errorf("init stream cut: %w", engine.ErrConnectionLost)
				}
			}
			return result, err
		}))
		m := imageManager(v, nil)
		made, err := m.Create(t.Context(), "web", "dev", machines.DefaultIdle)
		if err != nil {
			t.Fatalf("Create after completed init stream cut: %v", err)
		}
		if inits != 2 {
			t.Errorf("init execs=%d, want 2", inits)
		}
		pin, err := os.ReadFile(filepath.Join(v.dir, "host-keys", made.ID))
		if err != nil {
			t.Fatal(err)
		}
		if string(pin) != hibapi.FormatHostKey(g.keys[made.ID])+"\n" {
			t.Error("pin doesn't match the guest's retained key")
		}
	})
}
