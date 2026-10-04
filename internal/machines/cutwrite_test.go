package machines_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestCreateRetriesEachSetupWriteStep(t *testing.T) {
	for _, step := range []string{"cat", "hibd-env", "hibd-git", "hibd-codex"} {
		t.Run(step, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newCopyGuest()
				gitWrites, codexWrites := 0, 0
				guest := &envFileGuest{
					content: []byte("PATH=/usr/bin\n"),
					git: func(context.Context, engine.ExecRequest) (engine.ExecResult, error) {
						gitWrites++
						return engine.ExecResult{}, nil
					},
					codex: func(context.Context, engine.ExecRequest) (engine.ExecResult, error) {
						codexWrites++
						return engine.ExecResult{}, nil
					},
				}
				inits, cuts := 0, 0
				v := newEnv(t, fake.WithExec(func(ctx context.Context, em engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
					if req.Cmd == images.MachineInit {
						inits++
						return g.exec(ctx, em, home, req)
					}
					result, err := guest.exec(ctx, em, home, req)
					if cuts == 0 && (req.Cmd == step || req.Cmd == "sh" && req.Args[2] == step) {
						cuts++
						return engine.ExecResult{}, fmt.Errorf("setup stream cut: %w", engine.ErrConnectionLost)
					}
					return result, err
				}))
				m := imageManager(v, func(cfg *machines.Config) {
					cfg.Setup = machines.Setup{
						EnvBlock: testBlock(t), Git: &machines.GitIdentity{Name: testName, Email: testEmail},
						CodexAuth: []byte(`{"access_token":"hib-placeholder"}`),
					}
				})
				if _, err := m.Create(t.Context(), "web", "dev", machines.DefaultIdle); err != nil {
					t.Fatal(err)
				}
				if cuts != 1 || inits != 2 {
					t.Errorf("cuts=%d init execs=%d, want 1 and 2", cuts, inits)
				}
				wantGit, wantCodex := 1, 1
				if step == "hibd-git" || step == "hibd-codex" {
					wantGit = 2
				}
				if step == "hibd-codex" {
					wantCodex = 2
				}
				if gitWrites != wantGit || codexWrites != wantCodex || !bytes.Contains(guest.content, testBlock(t)) {
					t.Error("setup replay didn't deliver the complete setup")
				}
			})
		})
	}
}
