package machines

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

type placementEngine struct {
	engine.Engine
	create func(context.Context, engine.CreateOptions) (engine.Machine, error)
	resume func(context.Context, string, time.Duration) (engine.Machine, error)
	kill   func(context.Context, string) error
	save   func(context.Context, string, string) (engine.TemplateInfo, error)
	exec   func(context.Context, string, engine.ExecRequest) (engine.ExecResult, error)
}

func (e *placementEngine) Create(ctx context.Context, o engine.CreateOptions) (engine.Machine, error) {
	if e.create != nil {
		return e.create(ctx, o)
	}
	return e.Engine.Create(ctx, o)
}

func (e *placementEngine) Resume(ctx context.Context, id string, d time.Duration) (engine.Machine, error) {
	if e.resume != nil {
		return e.resume(ctx, id, d)
	}
	return e.Engine.Resume(ctx, id, d)
}

func (e *placementEngine) Kill(ctx context.Context, id string) error {
	if e.kill != nil {
		return e.kill(ctx, id)
	}
	return e.Engine.Kill(ctx, id)
}

func (e *placementEngine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	if e.save != nil {
		return e.save(ctx, id, alias)
	}
	return e.Engine.SaveTemplate(ctx, id, alias)
}

func (e *placementEngine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if e.exec != nil {
		return e.exec(ctx, id, req)
	}
	return e.Engine.Exec(ctx, id, req)
}

func placementRun(id task.ID, size string) task.Run {
	return task.Run{ID: task.RunID{Task: id, N: 1}, Spec: task.Spec{Image: "dev", Size: size}}
}

func placementHost(freeMB int) room.Host {
	return room.Host{PageSize: 2 << 20, Pages: 8192, FreePages: int64(freeMB / 2), DiskFree: 100 << 30}
}

func TestTaskFitsUsesOneReading(t *testing.T) {
	m, _ := sharedStateManager(t)
	reads := 0
	m.readRoom = func() (room.Host, error) {
		reads++
		return placementHost(3072), nil
	}
	fits, err := m.TaskFits(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := fits(placementRun("t-aaaaaaaa", "small")); err != nil {
		t.Fatalf("small task should fit: %v", err)
	}
	if err := fits(placementRun("t-bbbbbbbb", "medium")); !errors.Is(err, room.ErrFull) {
		t.Fatalf("medium task = %v, want ErrFull", err)
	}
	if reads != 1 {
		t.Fatalf("Fits read room %d times, want one reading for the queue", reads)
	}
}

func TestCreateTaskDecidesAdmission(t *testing.T) {
	m, e := sharedStateManager(t)
	r := placementRun("t-aaaaaaaa", "small")
	m.readRoom = func() (room.Host, error) { return placementHost(3072), nil }
	fits, err := m.TaskFits(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := fits(r); err != nil {
		t.Fatal(err)
	}
	m.readRoom = func() (room.Host, error) { return placementHost(3070), nil }
	if _, err := m.CreateTask(t.Context(), r, time.Hour); !errors.Is(err, room.ErrFull) {
		t.Fatalf("CreateTask = %v, want fresh admission to refuse with ErrFull", err)
	}
	ems, err := e.List(t.Context(), engine.ListOptions{})
	if err != nil || len(ems) != 0 {
		t.Fatalf("refused task created machines: %v, %v", ems, err)
	}
}

func TestCreateTaskLabelsTimeoutAndSetup(t *testing.T) {
	m, e := sharedStateManager(t)
	r := placementRun("t-aaaaaaaa", "medium")
	wantTimeout := 25 * time.Hour
	m.e = &placementEngine{Engine: e, create: func(ctx context.Context, o engine.CreateOptions) (engine.Machine, error) {
		if o.Timeout != wantTimeout || o.Template != images.Template("dev") {
			t.Errorf("Create options = %+v", o)
		}
		for k, want := range map[string]string{
			labelName: string(r.ID.Task), task.Label: string(r.ID.Task), labelImage: "dev",
			labelSize: "medium", labelIdle: DefaultIdle.String(),
		} {
			if o.Metadata[k] != want {
				t.Errorf("label %s = %q, want %q", k, o.Metadata[k], want)
			}
		}
		return e.Create(ctx, o)
	}}
	em, err := m.CreateTask(t.Context(), r, wantTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if Name(em) != string(r.ID.Task) || em.Metadata[task.Label] != string(r.ID.Task) {
		t.Fatalf("created wrong task identity: %s %v", em.ID, em.Metadata)
	}
	if _, err := m.readPin(em.ID); err != nil {
		t.Fatalf("task was not set up: %v", err)
	}
	adopted, err := m.CreateTask(t.Context(), r, wantTimeout)
	if err != nil || adopted.ID != em.ID {
		t.Fatalf("repeated create = %s, %v, want %s", adopted.ID, err, em.ID)
	}
}

func TestFindTaskIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		label string
		found bool
		err   error
	}{
		{"missing", "", false, nil},
		{"task", "t-aaaaaaaa", true, nil},
		{"unlabelled", "", false, ErrNotTask},
		{"other task", "t-bbbbbbbb", false, ErrNotTask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, e := sharedStateManager(t)
			if tc.name != "missing" {
				e.Add(engine.Machine{ID: "original", Metadata: map[string]string{
					labelName: "t-aaaaaaaa", task.Label: tc.label,
				}})
			}
			em, found, err := m.FindTask(t.Context(), "t-aaaaaaaa")
			if found != tc.found || !errors.Is(err, tc.err) {
				t.Fatalf("FindTask = %s, %v, %v", em.ID, found, err)
			}
			if found && em.ID != "original" {
				t.Fatalf("found %s, want original", em.ID)
			}
		})
	}
}

func TestFindTaskRemovesOnlyFencedDuplicateIDs(t *testing.T) {
	m, e := sharedStateManager(t)
	for i, id := range []string{"oldest", "newer", "newest"} {
		e.Add(engine.Machine{ID: id, StartedAt: time.Unix(int64(i), 0), Metadata: map[string]string{
			labelName: "t-aaaaaaaa", task.Label: "t-aaaaaaaa",
		}})
	}
	var fenced string
	var killed []string
	m.fenced = func(ctx context.Context, id, name, why string, op func(context.Context) error) error {
		fenced = id
		defer func() { fenced = "" }()
		return op(ctx)
	}
	m.e = &placementEngine{Engine: e, kill: func(ctx context.Context, id string) error {
		if fenced != id || id == "oldest" {
			return fmt.Errorf("unfenced or wrong ID removed: %s behind %s", id, fenced)
		}
		killed = append(killed, id)
		return e.Kill(ctx, id)
	}}
	em, found, err := m.FindTask(t.Context(), "t-aaaaaaaa")
	if err != nil || !found || em.ID != "oldest" {
		t.Fatalf("FindTask = %s, %v, %v, want oldest", em.ID, found, err)
	}
	slices.Sort(killed)
	if !slices.Equal(killed, []string{"newer", "newest"}) {
		t.Fatalf("removed %v, want only duplicate IDs", killed)
	}
}

func TestResumeTaskRunningIsUnchanged(t *testing.T) {
	m, e := sharedStateManager(t)
	e.Add(engine.Machine{ID: "running", State: engine.Running, Metadata: map[string]string{
		labelName: "t-aaaaaaaa", task.Label: "t-aaaaaaaa",
	}})
	m.readRoom = func() (room.Host, error) { return room.Host{}, errors.New("must not admit a running machine") }
	m.e = &placementEngine{Engine: e, resume: func(context.Context, string, time.Duration) (engine.Machine, error) {
		return engine.Machine{}, errors.New("must not resume a running machine")
	}}
	em, err := m.ResumeTask(t.Context(), placementRun("t-aaaaaaaa", "small"), "running", time.Hour)
	if err != nil || em.ID != "running" {
		t.Fatalf("ResumeTask = %s, %v, want running unchanged", em.ID, err)
	}
}

func TestResumeTaskAdmitsActualSizeThroughOwner(t *testing.T) {
	for _, tc := range []struct {
		name string
		free int
		cpus int
		want error
	}{
		{"memory", 4096, 2, room.ErrFull},
		{"vCPUs", 8192, 5, room.ErrFull},
		{"owner guard", 8192, 2, ErrNotTask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, e := sharedStateManager(t)
			e.Add(engine.Machine{ID: "paused", State: engine.Paused, MemoryMB: 4096, CPUs: tc.cpus,
				Metadata: map[string]string{labelName: "t-aaaaaaaa", task.Label: "t-aaaaaaaa"}})
			m.taskLim = TaskLimits{HostCPUs: 2}
			m.readRoom = func() (room.Host, error) { return placementHost(tc.free), nil }
			called := false
			m.e = &placementEngine{Engine: e, resume: func(_ context.Context, id string, d time.Duration) (engine.Machine, error) {
				called = true
				if id != "paused" || d != 25*time.Hour {
					t.Errorf("Resume %s, %v", id, d)
				}
				return engine.Machine{}, ErrNotTask
			}}
			_, err := m.ResumeTask(t.Context(), placementRun("t-aaaaaaaa", "small"), "paused", 25*time.Hour)
			if !errors.Is(err, tc.want) || called != (tc.name == "owner guard") {
				t.Fatalf("ResumeTask = %v, Owner called %v, want %v", err, called, tc.want)
			}
		})
	}
}

func TestAdmitChargesUnknownCPUs(t *testing.T) {
	running := []engine.Machine{{ID: "unknown", MemoryMB: 1024, CPUs: 0}}
	err := Admit(placementHost(8192), running, TaskLimits{HostCPUs: 1}, "task", 1024, 1)
	if !errors.Is(err, room.ErrFull) {
		t.Fatalf("Admit = %v, want unknown machine charged the default's two vCPUs", err)
	}
}

func assertPlacementLocked(t *testing.T, dir string) {
	t.Helper()
	unlock, ok, err := (statefile.Dir{Path: dir}).TryLock(RoomLock, "test admission")
	if unlock != nil {
		unlock()
	}
	if err != nil || ok {
		t.Errorf("engine placement ran without room.lock: acquired %v, error %v", ok, err)
	}
}
