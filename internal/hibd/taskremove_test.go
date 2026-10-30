package hibd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/task"
)

type taskRemovalEngine struct {
	*fake.Engine
	links   *fakeLinks
	lists   int
	omit    string
	late    *engine.Machine
	failID  string
	killErr error
}

func (e *taskRemovalEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	e.lists++
	ems, err := e.Engine.List(ctx, opts)
	return slices.DeleteFunc(ems, func(em engine.Machine) bool {
		return em.ID == e.omit || e.lists == 1 && e.late != nil && em.ID == e.late.ID
	}), err
}

func (e *taskRemovalEngine) Kill(ctx context.Context, id string) error {
	e.links.event("kill %s", id)
	if id == e.failID {
		if errors.Is(e.killErr, engine.ErrNotFound) {
			if err := e.Engine.Kill(ctx, id); err != nil {
				return err
			}
		}
		return e.killErr
	}
	return e.Engine.Kill(ctx, id)
}

func TestTaskRemoveLeavesNoTaskMachine(t *testing.T) {
	killFailure := errors.New("kill refused")
	machine := func(id string, state engine.State) engine.Machine {
		return engine.Machine{ID: id, State: state,
			Metadata: map[string]string{"hib.name": "t-aaaaaaaa", task.Label: "t-aaaaaaaa"}}
	}
	running := machine("recorded", engine.Running)
	paused := machine("recorded", engine.Paused)
	duplicate := machine("unused", engine.Running)
	late := machine("late", engine.Running)
	wrongName := machine("recorded", engine.Running)
	wrongName.Metadata["hib.name"] = "web"
	wrongLabel := machine("recorded", engine.Running)
	wrongLabel.Metadata[task.Label] = "t-bbbbbbbb"
	unlabelled := machine("person", engine.Running)
	delete(unlabelled.Metadata, task.Label)
	for _, tc := range []struct {
		name      string
		recorded  string
		initial   []engine.Machine
		omit      string
		late      *engine.Machine
		failID    string
		killErr   error
		wantErr   error
		survivors []string
	}{
		{name: "running", recorded: "recorded", initial: []engine.Machine{running}},
		{name: "paused", recorded: "recorded", initial: []engine.Machine{paused}},
		{name: "first list duplicate", recorded: "recorded", initial: []engine.Machine{running, duplicate}},
		{name: "late duplicate", recorded: "recorded", initial: []engine.Machine{running}, late: &late},
		{name: "omitted recorded", recorded: "recorded", initial: []engine.Machine{running}, omit: "recorded"},
		{name: "omitted recorded and late duplicate", recorded: "recorded",
			initial: []engine.Machine{running}, omit: "recorded", late: &late},
		{name: "missing recorded with duplicate", recorded: "recorded", initial: []engine.Machine{duplicate}},
		{name: "unrecorded duplicates", initial: []engine.Machine{running, duplicate}},
		{name: "unrecorded late duplicate", initial: []engine.Machine{running}, late: &late},
		{name: "unrecorded late only", late: &late},
		{name: "nothing recorded or listed"},
		{name: "kill recorded fails", recorded: "recorded", initial: []engine.Machine{running},
			failID: "recorded", killErr: killFailure, wantErr: killFailure, survivors: []string{"recorded"}},
		{name: "kill duplicate fails", recorded: "recorded", initial: []engine.Machine{running, duplicate},
			failID: "unused", killErr: killFailure, wantErr: killFailure, survivors: []string{"recorded", "unused"}},
		{name: "kill recorded not found", recorded: "recorded", initial: []engine.Machine{running},
			failID: "recorded", killErr: engine.ErrNotFound},
		{name: "kill duplicate not found", recorded: "recorded", initial: []engine.Machine{running, duplicate},
			failID: "unused", killErr: engine.ErrNotFound},
		{name: "unlabelled named machine", recorded: "recorded", initial: []engine.Machine{running, unlabelled},
			wantErr: machines.ErrNotTask, survivors: []string{"person", "recorded"}},
		{name: "recorded has another name", recorded: "recorded", initial: []engine.Machine{wrongName, duplicate},
			wantErr: machines.ErrNotTask, survivors: []string{"recorded", "unused"}},
		{name: "recorded has another label", recorded: "recorded", initial: []engine.Machine{wrongLabel, duplicate},
			wantErr: machines.ErrNotTask, survivors: []string{"recorded", "unused"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &taskRemovalEngine{Engine: fake.New(), links: &fakeLinks{},
				omit: tc.omit, late: tc.late, failID: tc.failID, killErr: tc.killErr}
			for _, em := range tc.initial {
				e.Add(em)
			}
			if tc.late != nil {
				e.Add(*tc.late)
			}
			a, _, _ := newApp(t, e)
			withLinks(a, e.links, true)
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			pins := filepath.Join(cfg.stateDir, "host-keys")
			if err := os.MkdirAll(pins, 0o700); err != nil {
				t.Fatal(err)
			}
			pinned := slices.Clone(tc.initial)
			if tc.late != nil {
				pinned = append(pinned, *tc.late)
			}
			for _, em := range pinned {
				if err := os.WriteFile(filepath.Join(pins, em.ID), []byte("pin\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			m := machines.New(machines.Config{
				Engine: e, StateDir: cfg.stateDir, Fenced: a.snapshotFence(cfg),
			})
			tm := taskMachines{a: a, cfg: cfg, e: e, m: m}
			r := task.Run{ID: task.RunID{Task: "t-aaaaaaaa", N: 2}, Kept: tc.recorded}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			err = tm.Remove(ctx, r)
			if tc.recorded == "" && len(tc.initial) == 0 && tc.late != nil {
				if err == nil || !strings.Contains(err.Error(), "appeared during removal: try again") {
					t.Errorf("first removal = %v, want appeared during removal: try again", err)
				}
				if err != nil {
					t.Logf("first removal = %v; retrying", err)
					err = tm.Remove(ctx, r)
				}
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Remove = %v, want %v", err, tc.wantErr)
			}
			remaining, err := e.Engine.List(ctx, engine.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, em := range remaining {
				ids = append(ids, em.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.survivors) {
				t.Errorf("machines after Remove = %v, want %v; events %v", ids, tc.survivors, e.links.log())
			}
			for _, em := range pinned {
				path := filepath.Join(pins, em.ID)
				if slices.Contains(tc.survivors, em.ID) {
					if data, err := os.ReadFile(path); err != nil || string(data) != "pin\n" {
						t.Errorf("survivor %s pin = %q, %v, want unchanged", em.ID, data, err)
					}
					got, err := e.Get(ctx, em.ID)
					if err != nil || got.State != em.State {
						t.Errorf("survivor %s = %s, %v, want %s", em.ID, got.State, err, em.State)
					}
				} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("removed %s pin still exists: %v", em.ID, err)
				}
			}
			held := map[string]bool{}
			closed := map[string]bool{}
			for _, event := range e.links.log() {
				parts := strings.Fields(event)
				switch parts[0] {
				case "fence":
					if held[parts[1]] {
						t.Errorf("nested fence on %s", parts[1])
					}
					held[parts[1]] = true
				case "release":
					delete(held, parts[1])
					delete(closed, parts[1])
				case "close":
					closed[parts[1]] = true
				case "kill":
					if !held[parts[1]] || !closed[parts[1]] {
						t.Errorf("killed %s without its fence and closed links", parts[1])
					}
					if errors.Is(tc.wantErr, machines.ErrNotTask) {
						t.Errorf("killed %s despite ErrNotTask", parts[1])
					}
				}
			}
			if len(held) != 0 {
				t.Errorf("unreleased fences: %v", held)
			}
			if tc.wantErr == nil && len(ids) == 0 {
				if err := tm.Remove(ctx, r); err != nil {
					t.Errorf("repeated Remove = %v", err)
				}
			}
		})
	}
}
