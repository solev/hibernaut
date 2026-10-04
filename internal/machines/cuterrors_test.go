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

func TestCreateDoesNotRetryOtherExecFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		exit int
	}{
		{name: "nonzero exit", exit: 17},
		{name: "generic", err: errors.New("exec failed")},
		{name: "not running", err: engine.ErrNotRunning},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "cancelled", err: context.Canceled},
		{name: "cut with deadline", err: errors.Join(engine.ErrConnectionLost, context.DeadlineExceeded)},
		{name: "cut with cancellation", err: errors.Join(engine.ErrConnectionLost, context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tries := 0
			v := newEnv(t, fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
				tries++
				return engine.ExecResult{ExitCode: tc.exit}, tc.err
			}))
			_, err := v.m.Create(t.Context(), "web", "dev", machines.DefaultIdle)
			if err == nil || tc.err != nil && !errors.Is(err, tc.err) || tries != 1 {
				t.Errorf("Create error=%v tries=%d, want failure and one try", err, tries)
			}
		})
	}
}

func TestCreateCutExhaustionKillsMachineAndRemovesPins(t *testing.T) {
	for _, step := range []string{"init", "write", "init then write"} {
		t.Run(step, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newCopyGuest()
				inits, writes := 0, 0
				v := newEnv(t, fake.WithExec(func(ctx context.Context, em engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
					result, err := g.exec(ctx, em, home, req)
					if req.Cmd == images.MachineInit {
						inits++
						if step == "init" || step == "init then write" && inits == 1 {
							return engine.ExecResult{}, engine.ErrConnectionLost
						}
					} else {
						writes++
						return engine.ExecResult{}, engine.ErrConnectionLost
					}
					return result, err
				}))
				m := imageManager(v, func(cfg *machines.Config) { cfg.Setup.EnvBlock = testBlock(t) })
				_, err := m.Create(t.Context(), "web", "dev", machines.DefaultIdle)
				if !errors.Is(err, engine.ErrConnectionLost) || inits != 3 {
					t.Errorf("Create error=%v init tries=%d, want cut exhaustion after 3", err, inits)
				}
				if step == "write" && writes != 3 || step == "init then write" && writes != 2 {
					t.Errorf("setup write tries=%d", writes)
				}
				if all := v.machines(t); len(all) != 0 {
					t.Error("exhaustion left the machine alive")
				}
				if pins, err := os.ReadDir(filepath.Join(v.dir, "host-keys")); err != nil || len(pins) != 0 {
					t.Error("exhaustion left a host-key pin")
				}
			})
		})
	}
}

func TestCreateKeepsASetupThatFinishedAsItsContextEnded(t *testing.T) {
	g := newCopyGuest()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	v := newEnv(t, fake.WithExec(func(c context.Context, em engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
		res, err := g.exec(c, em, home, req)
		cancel()
		return res, err
	}))
	if _, err := v.m.Create(ctx, "web", "dev", machines.DefaultIdle); err != nil {
		t.Fatalf("Create = %v, want the finished setup kept", err)
	}
	if all := v.machines(t); len(all) != 1 {
		t.Error("Create removed a machine whose setup finished")
	}
}
