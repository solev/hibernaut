package hibd

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/task"
)

type duplicateEngine struct {
	engine.Engine
	links *fakeLinks
}

func (e duplicateEngine) Kill(ctx context.Context, id string) error {
	e.links.event("kill %s", id)
	return e.Engine.Kill(ctx, id)
}

func (e duplicateEngine) Pause(ctx context.Context, id string) error {
	e.links.event("pause %s", id)
	return e.Engine.Pause(ctx, id)
}

func TestTaskKeepActsOnRecordedMachineThroughFence(t *testing.T) {
	for _, op := range []string{"pause", "remove"} {
		t.Run(op, func(t *testing.T) {
			e := fake.New()
			for i, id := range []string{"unused", "recorded"} {
				e.Add(engine.Machine{ID: id, State: engine.Running, StartedAt: time.Unix(int64(i), 0),
					Metadata: map[string]string{"hib.name": "t-aaaaaaaa", task.Label: "t-aaaaaaaa"}})
			}
			a, _, _ := newApp(t, e)
			f := &fakeLinks{}
			withLinks(a, f, true)
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			wrapped := duplicateEngine{Engine: e, links: f}
			m := machines.New(machines.Config{
				Engine: wrapped, StateDir: cfg.stateDir, Fenced: a.snapshotFence(cfg),
			})
			tm := taskMachines{a: a, cfg: cfg, e: wrapped, m: m}
			r := task.Run{ID: task.RunID{Task: "t-aaaaaaaa", N: 2}, Kept: "recorded"}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if op == "pause" {
				err = tm.Pause(ctx, r)
			} else {
				err = tm.Remove(ctx, r)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"fence unused rm duplicate", "close unused", "kill unused", "release unused"}
			if got := eventsOf(f, "unused"); !slices.Equal(got, want) {
				t.Fatalf("unused removal = %v, want %v", got, want)
			}
			if _, err := e.Get(ctx, "unused"); !errors.Is(err, engine.ErrNotFound) {
				t.Fatalf("unused duplicate still exists: %v", err)
			}
			em, err := e.Get(ctx, "recorded")
			want = []string{"fence recorded rm", "close recorded", "kill recorded", "release recorded"}
			if op == "pause" {
				if err != nil || em.State != engine.Paused {
					t.Fatalf("recorded machine = %s, %v, want paused", em.State, err)
				}
				want = []string{"fence recorded keep t-aaaaaaaa", "close recorded", "pause recorded", "release recorded"}
			} else if !errors.Is(err, engine.ErrNotFound) {
				t.Fatalf("recorded machine still exists after remove: %v", err)
			}
			if got := eventsOf(f, "recorded"); !slices.Equal(got, want) {
				t.Fatalf("recorded %s = %v, want %v", op, got, want)
			}
		})
	}
}

func TestTaskFindFencesDuplicateRemovalByID(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "fence refused"}[blocked], func(t *testing.T) {
			e := fake.New()
			for i, id := range []string{"oldest", "duplicate"} {
				e.Add(engine.Machine{ID: id, State: engine.Paused, StartedAt: time.Unix(int64(i), 0),
					Metadata: map[string]string{"hib.name": "t-aaaaaaaa", task.Label: "t-aaaaaaaa"}})
			}
			a, _, _ := newApp(t, e)
			f := &fakeLinks{}
			if blocked {
				f.fenceErr = errors.New("fence unavailable")
			}
			withLinks(a, f, true)
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			wrapped := duplicateEngine{Engine: e, links: f}
			m := machines.New(machines.Config{
				Engine: wrapped, StateDir: cfg.stateDir, Fenced: a.snapshotFence(cfg),
			})
			tm := taskMachines{a: a, cfg: cfg, e: wrapped, m: m}
			em, found, err := tm.Find(t.Context(), task.Run{ID: task.RunID{Task: "t-aaaaaaaa", N: 1}})
			if blocked {
				if err == nil {
					t.Fatal("Find removed a duplicate without its fence")
				}
				if _, err := e.Get(t.Context(), "duplicate"); err != nil {
					t.Fatalf("unfenced duplicate was removed: %v", err)
				}
				return
			}
			if err != nil || !found || em.ID != "oldest" {
				t.Fatalf("Find = %s, %v, %v", em.ID, found, err)
			}
			want := []string{"fence duplicate rm duplicate", "close duplicate", "kill duplicate", "release duplicate"}
			if got := eventsOf(f, "duplicate"); !slices.Equal(got, want) {
				t.Fatalf("duplicate removal = %v, want %v", got, want)
			}
			if _, err := e.Get(t.Context(), "duplicate"); !errors.Is(err, engine.ErrNotFound) {
				t.Fatalf("duplicate still exists: %v", err)
			}
		})
	}
}
