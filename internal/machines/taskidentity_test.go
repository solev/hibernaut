package machines

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
)

func TestFindTaskKeepsRecordedMachineAfterResume(t *testing.T) {
	for _, record := range []string{"result", "kept"} {
		t.Run(record, func(t *testing.T) {
			m, _ := sharedStateManager(t)
			now := time.Unix(100, 0)
			e := fake.New(fake.WithClock(func() time.Time { return now }))
			key := mustReadPlacementKey(t, m)
			m.e = &placementEngine{Engine: e, exec: func(context.Context, string, engine.ExecRequest) (engine.ExecResult, error) {
				return engine.ExecResult{Stdout: ssh.MarshalAuthorizedKey(key)}, nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			r := placementRun("t-aaaaaaaa", "medium")
			kept, err := m.CreateTask(ctx, r, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			duplicate := engine.Machine{ID: "abandoned", State: engine.Paused,
				StartedAt: now, Metadata: kept.Metadata}
			e.Add(duplicate)
			if err := e.Pause(ctx, kept.ID); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if _, err := m.ResumeTask(ctx, r, kept.ID, time.Hour); err != nil {
				t.Fatal(err)
			}
			r.Kept = kept.ID
			if record == "result" {
				r.Kept = duplicate.ID
				r.Log = []task.Checkpoint{{Body: &task.MachineResult{ID: kept.ID}}}
			}
			var fenced []string
			m.fenced = func(ctx context.Context, id, name, why string, op func(context.Context) error) error {
				fenced = append(fenced, id)
				return op(ctx)
			}
			em, found, err := m.FindTask(ctx, r)
			if err != nil || !found || em.ID != kept.ID {
				t.Fatalf("FindTask = %s, %v, %v, want the recorded machine %s", em.ID, found, err, kept.ID)
			}
			if _, err := e.Get(ctx, kept.ID); err != nil {
				t.Fatalf("recorded machine was removed: %v", err)
			}
			if _, err := e.Get(ctx, duplicate.ID); !errors.Is(err, engine.ErrNotFound) {
				t.Fatalf("unused duplicate still exists: %v", err)
			}
			if !slices.Equal(fenced, []string{duplicate.ID}) {
				t.Fatalf("fenced %v, want only the unused duplicate", fenced)
			}
		})
	}
}

func TestFindTaskDoesNotReplaceMissingRecordedMachine(t *testing.T) {
	m, e := sharedStateManager(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	r := placementRun("t-aaaaaaaa", "small")
	r.Kept = "gone"
	for _, id := range []string{"unuseda", "unusedb"} {
		e.Add(engine.Machine{ID: id, State: engine.Paused,
			Metadata: map[string]string{labelName: string(r.ID.Task), task.Label: string(r.ID.Task)}})
	}
	var fenced []string
	m.fenced = func(ctx context.Context, id, name, why string, op func(context.Context) error) error {
		fenced = append(fenced, id)
		return op(ctx)
	}
	em, found, err := m.FindTask(ctx, r)
	if err != nil || found || em.ID != "" {
		t.Fatalf("FindTask = %s, %v, %v, want missing recorded machine", em.ID, found, err)
	}
	remaining, err := e.List(ctx, engine.ListOptions{})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("unused machines remain: %v, %v", remaining, err)
	}
	slices.Sort(fenced)
	if !slices.Equal(fenced, []string{"unuseda", "unusedb"}) {
		t.Fatalf("fenced %v, want every unused duplicate", fenced)
	}
}

func TestFindTaskReadsRecordedMachineByID(t *testing.T) {
	m, e := sharedStateManager(t)
	r := placementRun("t-aaaaaaaa", "small")
	r.Kept = "recorded"
	e.Add(engine.Machine{ID: r.Kept, State: engine.Running,
		Metadata: map[string]string{labelName: string(r.ID.Task), task.Label: string(r.ID.Task)}})
	m.e = &placementEngine{Engine: e, list: func(context.Context, engine.ListOptions) ([]engine.Machine, error) {
		return nil, nil
	}}
	em, found, err := m.FindTask(t.Context(), r)
	if err != nil || !found || em.ID != r.Kept {
		t.Fatalf("FindTask = %s, %v, %v, want recorded machine despite omitted List entry", em.ID, found, err)
	}
}

func TestFindTaskDoesNotRemoveMachinesAfterLookupFailure(t *testing.T) {
	for _, lookupErr := range []error{engine.ErrConnectionLost, context.DeadlineExceeded} {
		t.Run(lookupErr.Error(), func(t *testing.T) {
			m, e := sharedStateManager(t)
			r := placementRun("t-aaaaaaaa", "small")
			r.Kept = "recorded"
			e.Add(engine.Machine{ID: "unused", Metadata: map[string]string{
				labelName: string(r.ID.Task), task.Label: string(r.ID.Task),
			}})
			m.e = &placementEngine{Engine: e, get: func(context.Context, string) (engine.Machine, error) {
				return engine.Machine{}, lookupErr
			}}
			if _, _, err := m.FindTask(t.Context(), r); !errors.Is(err, lookupErr) {
				t.Fatalf("FindTask = %v, want lookup failure", err)
			}
			if _, err := e.Get(t.Context(), "unused"); err != nil {
				t.Fatalf("removed machine without knowing the recorded machine's state: %v", err)
			}
		})
	}
}

func TestResumeTaskChargesUnknownCPUs(t *testing.T) {
	m, e := sharedStateManager(t)
	r := placementRun("t-aaaaaaaa", "small")
	e.Add(engine.Machine{ID: "paused", State: engine.Paused, MemoryMB: 1024,
		Metadata: map[string]string{labelName: string(r.ID.Task), task.Label: string(r.ID.Task)}})
	e.Add(engine.Machine{ID: "running", State: engine.Running, MemoryMB: 1024, CPUs: 1})
	m.taskLim = TaskLimits{HostCPUs: 1}
	m.readRoom = func() (room.Host, error) { return placementHost(8192), nil }
	m.e = &placementEngine{Engine: e, get: func(ctx context.Context, id string) (engine.Machine, error) {
		em, err := e.Get(ctx, id)
		em.CPUs = 0
		return em, err
	}}
	if _, err := m.ResumeTask(t.Context(), r, "paused", time.Hour); !errors.Is(err, room.ErrFull) {
		t.Fatalf("ResumeTask = %v, want ErrFull charging the default's two vCPUs beside one running", err)
	}
	if em, err := e.Get(t.Context(), "paused"); err != nil || em.State != engine.Paused {
		t.Fatalf("refused machine = %s, %v, want still paused", em.State, err)
	}
}
