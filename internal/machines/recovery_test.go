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

type deletionEngine struct {
	engine.Engine
	delete func(context.Context, string) error
}

func (e deletionEngine) DeleteTemplate(ctx context.Context, id string) error {
	return e.delete(ctx, id)
}

func TestSaveRecoveryHasItsOwnDeadlineAfterCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		web := v.create(t, "web")
		e := deletionEngine{Engine: v.e, delete: func(ctx context.Context, id string) error {
			<-ctx.Done()
			return ctx.Err()
		}}
		m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
		v.e.FailNextSave(engine.ErrCheckpoint)
		_, err := m.SaveImage(t.Context(), "web", "myimg")
		if err == nil || !strings.Contains(err.Error(), "couldn't remove template hibimg-myimg") {
			t.Fatalf("SaveImage = %v", err)
		}
		if strings.Contains(err.Error(), "Nothing was saved") {
			t.Errorf("claimed nothing saved despite an alias left behind: %v", err)
		}
		if em := v.get(t, web.ID); em.State != engine.Running {
			t.Errorf("cleanup timeout stranded the origin %s", em.State)
		}
	})
}

func TestSaveUnconfirmedSnapshotIsNotCalledSafe(t *testing.T) {
	v := newEnv(t)
	v.create(t, "web")
	e := &faultEngine{Engine: v.e, resume: func(context.Context, string, time.Duration) (engine.Machine, error) {
		return engine.Machine{}, errors.New("resume failed")
	}}
	m := imageManager(v, func(cfg *machines.Config) {
		cfg.Engine = e
		cfg.Durable = func(context.Context, string) error { return errors.New("snapshot not on disk") }
	})
	_, err := m.SaveImage(t.Context(), "web", "myimg")
	if err == nil || !strings.Contains(err.Error(), "snapshot reached disk") || strings.Contains(err.Error(), "safe snapshot") {
		t.Fatalf("unconfirmed snapshot state claim = %v", err)
	}
}

func TestSaveRestoresPausedOriginAfterCheckpointWakeRace(t *testing.T) {
	v := newEnv(t)
	web := v.create(t, "web")
	if err := v.m.Pause(t.Context(), "web"); err != nil {
		t.Fatal(err)
	}
	e := &faultEngine{Engine: v.e}
	e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
		info, err := v.e.SaveTemplate(ctx, id, alias)
		if _, rerr := v.e.Resume(ctx, id, time.Minute); rerr != nil {
			t.Fatal(rerr)
		}
		return info, err
	}
	m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
	v.e.FailNextSave(engine.ErrCheckpoint)
	_, err := m.SaveImage(t.Context(), "web", "myimg")
	if !errors.Is(err, engine.ErrCheckpoint) {
		t.Fatalf("SaveImage = %v", err)
	}
	if em := v.get(t, web.ID); em.State != engine.Paused {
		t.Errorf("origin = %s, want paused again", em.State)
	}
}

func TestForkKillsCopiesFailingEachSetupStep(t *testing.T) {
	for _, step := range []string{"reset", images.MachineInit, "cat", "env CAS"} {
		t.Run(step, func(t *testing.T) {
			g := newCopyGuest()
			v := newEnv(t, fake.WithExec(g.exec))
			v.create(t, "web")
			g.fail = func(req engine.ExecRequest) error {
				if req.Cmd == step || req.Cmd == "sh" && len(req.Args) >= 3 &&
					(step == "reset" && req.Args[2] == "hibd-reset" || step == "env CAS" && req.Args[2] == "hibd-env") {
					return errors.New("setup failed")
				}
				return nil
			}
			m := imageManager(v, nil)
			made, err := m.Fork(t.Context(), "web", 1)
			if err == nil || len(made) != 0 {
				t.Fatalf("Fork = %+v, %v", made, err)
			}
			all, err := m.List(t.Context(), "")
			if err != nil || len(all) != 1 || all[0].Name != "web" {
				t.Fatalf("failed copy wasn't killed: %+v, %v", all, err)
			}
		})
	}
}

func TestListEscapesEngineValuesAndRejectsInvalidForkNames(t *testing.T) {
	v := newEnv(t)
	v.e.Add(engine.Machine{ID: "bad\x1b]52;c;value\x07", State: "running\x1b]52;c;value\x07", Metadata: map[string]string{"hib.name": "web", "hib.forkof": "bad\x1b[31m"}})
	all, err := v.m.List(t.Context(), "")
	if err != nil || len(all) != 1 {
		t.Fatalf("List = %+v, %v", all, err)
	}
	if strings.ContainsRune(all[0].ID+all[0].State, 0x1b) || all[0].ForkOf != "" {
		t.Errorf("unsafe machine output: %+v", all[0])
	}
}
