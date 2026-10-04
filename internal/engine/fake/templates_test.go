package fake_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestSavedTemplateCopiesTheMachine(t *testing.T) {
	e := fake.New(fake.WithLocalExec(t.TempDir()))
	ctx := t.Context()
	origin, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	sh(t, e, origin.ID, "echo saved >note")
	info, err := e.SaveTemplate(ctx, origin.ID, "hibimg-app")
	if err != nil {
		t.Fatal(err)
	}
	sh(t, e, origin.ID, "echo later >note")

	copied, err := e.Create(ctx, engine.CreateOptions{Template: "hibimg-app"})
	if err != nil {
		t.Fatal(err)
	}
	if copied.Template != info.ID || copied.MemoryMB != origin.MemoryMB || info.MemoryMB != origin.MemoryMB {
		t.Errorf("copy = template %s with %d MiB, saved %+v, want template %s with the origin's %d MiB", copied.Template, copied.MemoryMB, info, info.ID, origin.MemoryMB)
	}
	if got := sh(t, e, copied.ID, "cat note"); got != "saved\n" {
		t.Errorf("copy's note = %q, want what the origin had when it was saved", got)
	}
}

func TestFailNextSaveLeavesTheAliasBehind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state engine.State
	}{
		{"checkpoint", fmt.Errorf("orchestrator: %w", engine.ErrCheckpoint), engine.Paused},
		{"unknown", errors.New("connection reset"), engine.Running},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fake.New()
			ctx := t.Context()
			origin, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
			if err != nil {
				t.Fatal(err)
			}
			e.FailNextSave(tc.err)
			if _, err := e.SaveTemplate(ctx, origin.ID, "hibimg-app"); !errors.Is(err, tc.err) {
				t.Fatalf("SaveTemplate = %v, want %v", err, tc.err)
			}
			if got, _ := e.Get(ctx, origin.ID); got.State != tc.state {
				t.Errorf("origin is %s, want %s", got.State, tc.state)
			}
			left, err := e.Template(ctx, "hibimg-app")
			if err != nil {
				t.Fatalf("Template = %v, want the alias left behind", err)
			}
			if saved, err := e.SavedTemplates(ctx); err != nil || len(saved) != 0 {
				t.Errorf("SavedTemplates = %v, %v, want none", saved, err)
			}
			if _, err := e.Create(ctx, engine.CreateOptions{Template: "hibimg-app"}); !errors.Is(err, engine.ErrNoTemplate) {
				t.Errorf("Create from the alias left behind = %v, want ErrNoTemplate", err)
			}
			if err := e.DeleteTemplate(ctx, left.ID); err != nil {
				t.Fatal(err)
			}

			if _, err := e.Resume(ctx, origin.ID, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := e.SaveTemplate(ctx, origin.ID, "hibimg-app"); err != nil {
				t.Errorf("second SaveTemplate = %v, want the failure used up", err)
			}
		})
	}
}

func TestFailCreateAfterStart(t *testing.T) {
	e := fake.New()
	ctx := t.Context()
	create := func(name string) error {
		_, err := e.Create(ctx, engine.CreateOptions{Template: "base", Metadata: map[string]string{"hib.name": name}})
		return err
	}
	e.FailCreateAfterStart(map[string]string{"hib.name": "web-2"})
	if err := create("web-1"); err != nil {
		t.Fatal(err)
	}
	if err := create("web-2"); err == nil {
		t.Fatal("Create(web-2) succeeded, want it to fail after the start")
	}
	started, err := e.List(ctx, engine.ListOptions{Metadata: map[string]string{"hib.name": "web-2"}})
	if err != nil || len(started) != 1 || started[0].State != engine.Running {
		t.Errorf("machines called web-2 = %v, %v, want one running", started, err)
	}
	if err := create("web-3"); err != nil {
		t.Errorf("Create(web-3) = %v, want only web-2 to fail", err)
	}
}

func sh(t *testing.T, e *fake.Engine, id, script string) string {
	t.Helper()
	res, err := e.Exec(t.Context(), id, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", script}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("%s: %v, exit %d, %s", script, err, res.ExitCode, res.Stderr)
	}
	return string(res.Stdout)
}
