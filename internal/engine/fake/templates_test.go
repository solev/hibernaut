package fake_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
			pauseAndResume(t, e, origin.ID)
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

func TestFailedCheckpointFallsBackToTheLastSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before func(t *testing.T, e *fake.Engine, id string)
		lost   bool
	}{
		{"first checkpoint", func(*testing.T, *fake.Engine, string) {}, true},
		{"paused before", pauseAndResume, false},
		{"saved before", func(t *testing.T, e *fake.Engine, id string) {
			if _, err := e.SaveTemplate(t.Context(), id, "hibimg-earlier"); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fake.New(fake.WithLocalExec(t.TempDir()))
			ctx := t.Context()
			origin, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
			if err != nil {
				t.Fatal(err)
			}
			home := strings.TrimSpace(sh(t, e, origin.ID, "pwd"))
			tc.before(t, e, origin.ID)
			e.FailNextSave(engine.ErrCheckpoint)
			if _, err := e.SaveTemplate(ctx, origin.ID, "hibimg-app"); !errors.Is(err, engine.ErrCheckpoint) {
				t.Fatalf("SaveTemplate = %v, want ErrCheckpoint", err)
			}
			got, err := e.Get(ctx, origin.ID)
			_, homeErr := os.Stat(home)
			switch {
			case tc.lost && (!errors.Is(err, engine.ErrNotFound) || !errors.Is(homeErr, fs.ErrNotExist)):
				t.Errorf("Get = %v and home %v, want both gone: the machine had no snapshot to fall back on", err, homeErr)
			case !tc.lost && (err != nil || got.State != engine.Paused || homeErr != nil):
				t.Errorf("Get = %+v, %v and home %v, want the machine paused at its last snapshot", got, err, homeErr)
			}
		})
	}
}

func TestBuiltTemplateResourcesCarryOver(t *testing.T) {
	e := fake.New(fake.WithTemplates())
	ctx := t.Context()
	id, err := e.BuildTemplate(ctx, engine.TemplateSpec{Name: "hib-big", FromImage: "ubuntu:24.04", CPUs: 4, MemoryMB: 2048}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var origin engine.Machine
	for _, name := range []string{"hib-big", id} {
		m, err := e.Create(ctx, engine.CreateOptions{Template: name})
		if err != nil || m.Template != id || m.MemoryMB != 2048 {
			t.Fatalf("Create(%s) = template %s with %d MiB, %v; want %s with 2048", name, m.Template, m.MemoryMB, err, id)
		}
		origin = m
	}
	info, err := e.SaveTemplate(ctx, origin.ID, "hibimg-big")
	if err != nil || info.CPUs != 4 || info.MemoryMB != 2048 {
		t.Fatalf("SaveTemplate = %+v, %v; want 4 CPUs and 2048 MiB, the origin's", info, err)
	}
	copied, err := e.Create(ctx, engine.CreateOptions{Template: "hibimg-big"})
	if err != nil || copied.Template != info.ID || copied.MemoryMB != 2048 {
		t.Fatalf("copy = template %s with %d MiB, %v; want %s with 2048", copied.Template, copied.MemoryMB, err, info.ID)
	}
	if mt, err := e.Metrics(ctx, copied.ID); err != nil || mt.CPUCount != 4 || mt.MemTotal != 2048<<20 {
		t.Errorf("copy's Metrics = %+v, %v; want 4 CPUs and 2048 MiB", mt, err)
	}
}

func TestSavingAgainWhileCopying(t *testing.T) {
	root := t.TempDir()
	e := fake.New(fake.WithLocalExec(root))
	ctx := t.Context()
	origin, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	sh(t, e, origin.ID, "echo 0 >note")
	info, err := e.SaveTemplate(ctx, origin.ID, "hibimg-app")
	if err != nil {
		t.Fatal(err)
	}

	const saves = 8
	copies := make(chan string, saves)
	var wg sync.WaitGroup
	for range saves {
		wg.Go(func() {
			m, err := e.Create(ctx, engine.CreateOptions{Template: "hibimg-app"})
			if err != nil {
				t.Errorf("Create while saving = %v", err)
				return
			}
			copies <- m.ID
		})
	}
	for i := range saves {
		sh(t, e, origin.ID, fmt.Sprintf("echo %d >note", i+1))
		if _, err := e.SaveTemplate(ctx, origin.ID, "hibimg-app"); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(copies)

	whole := regexp.MustCompile("^[0-8]\n$")
	for id := range copies {
		if got := sh(t, e, id, "cat note"); !whole.MatchString(got) {
			t.Errorf("copy's note = %q, want one build's whole note", got)
		}
		if err := e.Kill(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Kill(ctx, origin.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteTemplate(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("%s is left after deleting every machine and template", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFailCreateAfterStart(t *testing.T) {
	e := fake.New()
	ctx := t.Context()
	create := func(name string) error {
		_, err := e.Create(ctx, engine.CreateOptions{Template: "base", Metadata: map[string]string{"hib.name": name}})
		return err
	}
	armed := map[string]string{"hib.name": "web-2"}
	e.FailCreateAfterStart(armed)
	armed["hib.name"] = "web-1"
	if err := create("web-1"); err != nil {
		t.Fatalf("Create(web-1) = %v, want the fake to keep its own copy of the metadata", err)
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

// pauseAndResume gives machine id a snapshot to fall back on.
func pauseAndResume(t *testing.T, e *fake.Engine, id string) {
	t.Helper()
	if err := e.Pause(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Resume(t.Context(), id, 0); err != nil {
		t.Fatal(err)
	}
}
