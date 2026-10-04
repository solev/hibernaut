package fake_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

const imageAlias = "hibimg-app"

func TestAliasCacheHidesAMissForAMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, origin := cached(t)
		wantHidden(t, e)
		leaveBroken(t, e, origin.ID)
		if !e.HasAlias(imageAlias) {
			t.Fatal("HasAlias = false, want the alias the failed save left")
		}
		wantHidden(t, e)
		if saved, err := e.SavedTemplates(t.Context()); err != nil || len(saved) != 0 {
			t.Errorf("SavedTemplates = %v, %v; want none", saved, err)
		}

		time.Sleep(time.Minute)
		wantHidden(t, e)
		time.Sleep(time.Second)
		wantHidden(t, e)
		info := wantFound(t, e)
		if info.ID == "" || info.CPUs != 0 || info.MemoryMB != 0 || !info.Created.IsZero() {
			t.Errorf("Template = %+v, want only an ID and the alias: the failed save made no build", info)
		}
		if saved, err := e.SavedTemplates(t.Context()); err != nil || len(saved) != 0 {
			t.Errorf("SavedTemplates = %v, %v; want none", saved, err)
		}
	})
}

func TestAliasCacheKeepsLookingForAMissingAlias(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, origin := cached(t)
		wantHidden(t, e)
		for range 4 {
			time.Sleep(61 * time.Second)
			wantHidden(t, e)
		}
		leaveBroken(t, e, origin.ID)

		time.Sleep(time.Minute)
		wantHidden(t, e)
		time.Sleep(time.Second)
		wantHidden(t, e)
		wantFound(t, e)
	})
}

func TestAliasCacheForgetsAMissAfterFiveMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, origin := cached(t)
		wantHidden(t, e)
		leaveBroken(t, e, origin.ID)
		time.Sleep(5 * time.Minute)
		wantFound(t, e)
	})
}

func TestAliasCacheInvalidations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		create func(t *testing.T, e *fake.Engine, origin engine.Machine)
	}{
		{"save", func(t *testing.T, e *fake.Engine, origin engine.Machine) {
			if _, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias); err != nil {
				t.Fatal(err)
			}
		}},
		{"build", func(t *testing.T, e *fake.Engine, _ engine.Machine) {
			if _, err := e.BuildTemplate(t.Context(), engine.TemplateSpec{Name: imageAlias, FromImage: "ubuntu:24.04"}, nil); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e, origin := cached(t)
				wantHidden(t, e)
				tc.create(t, e, origin)
				if info := wantFound(t, e); info.MemoryMB != origin.MemoryMB {
					t.Errorf("Template = %+v, want the new template of %d MiB", info, origin.MemoryMB)
				}
				if _, err := e.Create(t.Context(), engine.CreateOptions{Template: imageAlias}); err != nil {
					t.Errorf("Create from the new template = %v", err)
				}
			})
		})
	}
}

func TestEveryAliasLookupRemembersAMiss(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lookUp func(t *testing.T, e *fake.Engine, origin engine.Machine)
	}{
		{"Template", func(t *testing.T, e *fake.Engine, _ engine.Machine) {
			wantHidden(t, e)
		}},
		{"Create", func(t *testing.T, e *fake.Engine, _ engine.Machine) {
			if _, err := e.Create(t.Context(), engine.CreateOptions{Template: imageAlias}); !errors.Is(err, engine.ErrNoTemplate) {
				t.Fatalf("Create from a missing alias = %v, want ErrNoTemplate", err)
			}
		}},
		{"SaveTemplate of a paused machine", func(t *testing.T, e *fake.Engine, origin engine.Machine) {
			paused, err := e.Create(t.Context(), engine.CreateOptions{Template: "base"})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Pause(t.Context(), paused.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.SaveTemplate(t.Context(), paused.ID, imageAlias); !errors.Is(err, engine.ErrNotRunning) {
				t.Fatalf("SaveTemplate(paused) = %v, want ErrNotRunning", err)
			}
		}},
		{"SaveTemplate of a missing machine", func(t *testing.T, e *fake.Engine, _ engine.Machine) {
			if _, err := e.SaveTemplate(t.Context(), fake.NewID(), imageAlias); !errors.Is(err, engine.ErrNotFound) {
				t.Fatalf("SaveTemplate(missing) = %v, want ErrNotFound", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e, origin := cached(t)
				tc.lookUp(t, e, origin)
				if e.HasAlias(imageAlias) {
					t.Fatal("HasAlias = true after a lookup, want it still missing")
				}
				time.Sleep(30 * time.Second)
				leaveBroken(t, e, origin.ID)
				time.Sleep(31 * time.Second)
				wantHidden(t, e)
				wantFound(t, e)
			})
		})
	}
}

func TestCreateFromAHiddenAliasFails(t *testing.T) {
	e := fake.New(fake.WithAliasCache())
	origin, err := e.Create(t.Context(), engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	leaveBroken(t, e, origin.ID)
	if m, err := e.Create(t.Context(), engine.CreateOptions{Template: imageAlias}); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("Create from a hidden alias = %+v, %v; want ErrNoTemplate", m, err)
	}
}

func TestSaveLooksItsAliasUp(t *testing.T) {
	e, origin := cached(t)
	leaveBroken(t, e, origin.ID)
	wantHidden(t, e)
}

func TestSavingIntoAHiddenAliasFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, origin := cached(t)
		leaveBroken(t, e, origin.ID)
		boom := errors.New("boom")
		e.FailNextSave(boom)

		_, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias)
		for _, notWant := range []error{nil, boom, engine.ErrCheckpoint, engine.ErrNotRunning, engine.ErrNotFound} {
			if errors.Is(err, notWant) {
				t.Fatalf("SaveTemplate into a hidden alias = %v, want a plain error of its own", err)
			}
		}
		if got := get(t, e, origin.ID); got.State != engine.Running {
			t.Errorf("origin is %s, want still running", got.State)
		}

		time.Sleep(61 * time.Second)
		wantHidden(t, e)
		if info := wantFound(t, e); info.CPUs != 0 {
			t.Errorf("Template = %+v, want still no build", info)
		}
		if _, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias); !errors.Is(err, boom) {
			t.Errorf("SaveTemplate once the alias shows = %v, want the failure armed before", err)
		}
	})
}

func TestEarlySaveFailuresCreateNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(e *fake.Engine, err error)
		err  error
	}{
		{"not running", (*fake.Engine).FailNextSave, fmt.Errorf("sandbox is pausing: %w", engine.ErrNotRunning)},
		{"not found", (*fake.Engine).FailNextSave, fmt.Errorf("sandbox is gone: %w", engine.ErrNotFound)},
		{"rejected", (*fake.Engine).RejectNextSave, errors.New("node is busy")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, origin := cached(t)
			tc.arm(e, tc.err)
			if _, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias); !errors.Is(err, tc.err) {
				t.Fatalf("SaveTemplate = %v, want %v", err, tc.err)
			}
			if e.HasAlias(imageAlias) {
				t.Error("HasAlias = true, want the failed save to have made nothing")
			}
			if got := get(t, e, origin.ID); got.State != engine.Running {
				t.Errorf("origin is %s, want still running", got.State)
			}
			if _, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias); err != nil {
				t.Errorf("second SaveTemplate = %v, want the failure used up", err)
			}
		})
	}
}

func TestDelayedSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, origin := cached(t)
		wantHidden(t, e)
		reset := errors.New("connection reset")
		e.DelayNextSave(90 * time.Second)
		e.FailNextSave(reset)
		start := time.Now()
		done := make(chan error)
		go func() {
			_, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias)
			done <- err
		}()

		time.Sleep(30 * time.Second)
		wantHidden(t, e)
		if err := <-done; !errors.Is(err, reset) {
			t.Fatalf("SaveTemplate = %v, want %v", err, reset)
		}
		if took := time.Since(start); took != 90*time.Second {
			t.Errorf("SaveTemplate took %s, want 1m30s", took)
		}
		wantHidden(t, e)
		wantFound(t, e)

		e.DelayNextSave(time.Hour)
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()
		if _, err := e.SaveTemplate(ctx, origin.ID, "hibimg-other"); !errors.Is(err, context.Canceled) {
			t.Errorf("SaveTemplate cancelled during its delay = %v, want context.Canceled", err)
		}
		if e.HasAlias("hibimg-other") {
			t.Error("HasAlias = true, want a save cancelled during its delay to have made nothing")
		}
	})
}

func TestHasAlias(t *testing.T) {
	e, origin := cached(t)
	if !e.HasAlias("base") || e.HasAlias(imageAlias) {
		t.Errorf("HasAlias(base, %s) = %t, %t; want true, false", imageAlias, e.HasAlias("base"), e.HasAlias(imageAlias))
	}
	info, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Kill(t.Context(), origin.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteTemplate(t.Context(), info.ID); err != nil {
		t.Fatal(err)
	}
	if e.HasAlias(imageAlias) {
		t.Error("HasAlias = true after DeleteTemplate, want false")
	}
}

func TestWithoutAliasCacheEveryLookupLooks(t *testing.T) {
	e := fake.New(fake.WithTemplates("base"))
	origin, err := e.Create(t.Context(), engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	wantHidden(t, e)
	leaveBroken(t, e, origin.ID)
	if info := wantFound(t, e); info.CPUs != 0 || info.MemoryMB != 0 {
		t.Errorf("Template = %+v, want only an ID and the alias", info)
	}
	if _, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias); err != nil {
		t.Errorf("SaveTemplate into the alias a failed save left = %v, want a build added", err)
	}
}

func TestFailedSaveKeepsTheLastBuild(t *testing.T) {
	e, origin := cached(t)
	info, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias)
	if err != nil {
		t.Fatal(err)
	}
	e.FailNextSave(errors.New("connection reset"))
	if _, err := e.SaveTemplate(t.Context(), origin.ID, imageAlias); err == nil {
		t.Fatal("SaveTemplate succeeded, want the armed failure")
	}
	if got := wantFound(t, e); got != info {
		t.Errorf("Template = %+v, want the last good build's %+v", got, info)
	}
	if saved, err := e.SavedTemplates(t.Context()); err != nil || len(saved) != 1 || saved[0] != info {
		t.Errorf("SavedTemplates = %v, %v; want only %+v", saved, err, info)
	}
	if _, err := e.Create(t.Context(), engine.CreateOptions{Template: imageAlias}); err != nil {
		t.Errorf("Create from the alias = %v, want the last good build", err)
	}
}

func cached(t *testing.T) (*fake.Engine, engine.Machine) {
	t.Helper()
	e := fake.New(fake.WithAliasCache(), fake.WithTemplates("base"))
	origin, err := e.Create(t.Context(), engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	return e, origin
}

func leaveBroken(t *testing.T, e *fake.Engine, id string) {
	t.Helper()
	reset := errors.New("connection reset")
	e.FailNextSave(reset)
	if _, err := e.SaveTemplate(t.Context(), id, imageAlias); !errors.Is(err, reset) {
		t.Fatalf("SaveTemplate = %v, want %v", err, reset)
	}
}

func wantHidden(t *testing.T, e *fake.Engine) {
	t.Helper()
	if info, err := e.Template(t.Context(), imageAlias); !errors.Is(err, engine.ErrNoTemplate) {
		t.Fatalf("Template = %+v, %v; want ErrNoTemplate", info, err)
	}
}

func wantFound(t *testing.T, e *fake.Engine) engine.TemplateInfo {
	t.Helper()
	info, err := e.Template(t.Context(), imageAlias)
	if err != nil || info.Alias != imageAlias {
		t.Fatalf("Template = %+v, %v; want %s", info, err, imageAlias)
	}
	return info
}

func get(t *testing.T, e *fake.Engine, id string) engine.Machine {
	t.Helper()
	m, err := e.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
