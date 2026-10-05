package machines_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
)

func TestRepinRetriesInitButResumesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newCopyGuest()
		armed := false
		inits := 0
		v := newEnv(t, fake.WithExec(func(ctx context.Context, em engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
			result, err := g.exec(ctx, em, home, req)
			if armed && req.Cmd == images.MachineInit {
				inits++
				if inits == 1 {
					return engine.ExecResult{}, fmt.Errorf("init stream cut: %w", engine.ErrConnectionLost)
				}
			}
			return result, err
		}))
		web := v.create(t, "web")
		if err := v.m.Pause(t.Context(), "web"); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(v.dir, "host-keys", web.ID)); err != nil {
			t.Fatal(err)
		}
		resumes := 0
		e := &faultEngine{Engine: v.e, resume: func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
			resumes++
			return v.e.Resume(ctx, id, timeout)
		}}
		m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
		armed = true
		conn, err := m.Dial(t.Context(), "web")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		defer (<-v.dialed).Close()
		if resumes != 1 || inits != 2 {
			t.Errorf("resumes=%d inits=%d, want 1 and 2", resumes, inits)
		}
		if hibapi.FormatHostKey(conn.HostKey) != hibapi.FormatHostKey(g.keys[web.ID]) {
			t.Error("repin didn't retain the guest key")
		}
	})
}
