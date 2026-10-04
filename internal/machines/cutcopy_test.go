package machines_test

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSavedCreateAndForkRetryResetAndInit(t *testing.T) {
	for _, operation := range []string{"create", "fork"} {
		for _, step := range []string{"reset", "init"} {
			t.Run(operation+"/"+step, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					g := newCopyGuest()
					armed := false
					cuts := 0
					retained := ""
					v := newEnv(t, fake.WithExec(func(ctx context.Context, em engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
						result, err := g.exec(ctx, em, home, req)
						copy := em.Metadata["hib.name"] != "web"
						match := step == "init" && req.Cmd == images.MachineInit ||
							step == "reset" && req.Cmd == "sh" && req.Args[2] == "hibd-reset"
						if armed && copy && match && cuts == 0 {
							cuts++
							if step == "init" {
								retained = hibapi.FormatHostKey(g.keys[em.ID])
							}
							return engine.ExecResult{}, fmt.Errorf("copy stream cut: %w", engine.ErrConnectionLost)
						}
						return result, err
					}))
					v.create(t, "web")
					m := imageManager(v, nil)
					if operation == "create" {
						if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
							t.Fatal(err)
						}
					}
					armed = true
					var made hibapi.Machine
					var err error
					if operation == "create" {
						made, err = m.Create(t.Context(), "web2", "myimg", machines.DefaultIdle)
					} else {
						var copies []hibapi.Machine
						copies, err = m.Fork(t.Context(), "web", 1)
						if len(copies) == 1 {
							made = copies[0]
						}
					}
					if err != nil || made.ID == "" || cuts != 1 {
						t.Fatalf("copy error=%v cuts=%d", err, cuts)
					}
					pin := v.pin(t, made.ID)
					if pin != hibapi.FormatHostKey(g.keys[made.ID])+"\n" || retained != "" && pin != retained+"\n" {
						t.Error("retry reset deleted the key generated before the cut")
					}
					resets := 0
					for _, req := range g.requests[made.ID] {
						if req.Cmd == "sh" && req.Args[2] == "hibd-reset" {
							resets++
							if req.Args[3] != "/etc/hibernaut/ssh" || req.Args[4] != made.ID {
								t.Error("reset omitted the directory or stable machine ID")
							}
						}
					}
					if resets != 2 {
						t.Errorf("reset attempts=%d, want 2", resets)
					}
				})
			})
		}
	}
}
