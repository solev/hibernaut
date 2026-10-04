package machines_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSweepForksOnlyDeletesUnusedForkTemplates(t *testing.T) {
	v := newEnv(t, fake.WithExec(newCopyGuest().exec))
	web := v.create(t, "web")
	m := imageManager(v, nil)
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.SaveTemplate(t.Context(), web.ID, "hibfork-unused"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.SaveTemplate(t.Context(), web.ID, "other-template"); err != nil {
		t.Fatal(err)
	}
	made, err := m.Fork(t.Context(), "web", 1)
	if err != nil {
		t.Fatal(err)
	}
	copy := v.get(t, made[0].ID)
	before, _ := v.e.SavedTemplates(t.Context())
	var used string
	for _, tmpl := range before {
		if tmpl.ID == copy.Template {
			used = tmpl.Alias
		}
	}
	if !images.IsForkTemplate(used) {
		t.Fatalf("copy template = %s", used)
	}
	if err := m.SweepForks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.Template(t.Context(), "hibfork-unused"); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("unused fork remains: %v", err)
	}
	for _, alias := range []string{used, "hibimg-myimg", "other-template", "hib-dev"} {
		if _, err := v.e.Template(t.Context(), alias); err != nil {
			t.Errorf("sweep deleted %s: %v", alias, err)
		}
	}
	if err := m.Remove(t.Context(), "web-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.SweepForks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.Template(t.Context(), used); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("fork remains after last copy removed: %v", err)
	}
}

func TestForkLockSerializesPauseAndRemoveAndSkipsSweep(t *testing.T) {
	for _, operation := range []string{"pause", "remove"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				v := newEnv(t, fake.WithExec(newCopyGuest().exec))
				web := v.create(t, "web")
				if _, err := v.e.SaveTemplate(t.Context(), web.ID, "hibfork-unused"); err != nil {
					t.Fatal(err)
				}
				inDurable, release := make(chan struct{}), make(chan struct{})
				m := imageManager(v, func(cfg *machines.Config) {
					cfg.Durable = func(context.Context, string) error {
						close(inDurable)
						<-release
						return nil
					}
				})
				forkDone := make(chan error, 1)
				go func() {
					_, err := m.Fork(t.Context(), "web", 1)
					forkDone <- err
				}()
				<-inDurable
				if err := m.SweepForks(t.Context()); !errors.Is(err, machines.ErrBusy) {
					t.Errorf("busy sweep = %v, want ErrBusy", err)
				}
				if _, err := v.e.Template(t.Context(), "hibfork-unused"); err != nil {
					t.Errorf("busy sweep touched templates: %v", err)
				}
				opDone := make(chan error, 1)
				go func() {
					if operation == "pause" {
						opDone <- v.m.Pause(t.Context(), "web")
					} else {
						opDone <- v.m.Remove(t.Context(), "web")
					}
				}()
				synctest.Wait()
				select {
				case err := <-opDone:
					t.Errorf("%s didn't wait for the fork: %v", operation, err)
				default:
				}
				close(release)
				if err := <-forkDone; err != nil {
					t.Fatal(err)
				}
				if err := <-opDone; err != nil {
					t.Fatal(err)
				}
				origin, err := v.e.Get(t.Context(), web.ID)
				if operation == "pause" && (err != nil || origin.State != engine.Paused) {
					t.Errorf("pause after fork = %+v, %v", origin, err)
				}
				if operation == "remove" && !errors.Is(err, engine.ErrNotFound) {
					t.Errorf("remove after fork = %+v, %v", origin, err)
				}
			})
		})
	}
}

func TestForkReportsCopyLeftBehind(t *testing.T) {
	v := newEnv(t, fake.WithExec(newCopyGuest().exec))
	v.create(t, "web")
	v.e.FailCreateAfterStart(map[string]string{"hib.name": "web-1"})
	e := &faultEngine{Engine: v.e, kill: func(context.Context, string) error { return errors.New("kill failed") }}
	m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
	made, err := m.Fork(t.Context(), "web", 1)
	if len(made) != 0 || err == nil || !strings.Contains(err.Error(), "couldn't remove web-1 (ID ") || !strings.Contains(err.Error(), "hib rm web-1") {
		t.Fatalf("Fork = %+v, %v", made, err)
	}
}
