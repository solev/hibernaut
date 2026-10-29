// Package storetest is the contract every taskstore.Store keeps, so the daemon's tests on
// Memory hold on tasks.db too.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

var t0 = time.Date(2025, 10, 28, 9, 0, 0, 0, time.UTC)

var alice = task.Actor{Owner: "solev", Name: "alice", Source: "ssh"}

// Run runs the contract against stores open makes, one fresh store per subtest.
func Run(t *testing.T, open func(t *testing.T) taskstore.Store) {
	for _, c := range []struct {
		name string
		f    func(*testing.T, taskstore.Store)
	}{
		{"SubmitRetry", submitRetry},
		{"SubmitInputs", submitInputs},
		{"InputLimit", inputLimit},
		{"InputLimitNew", inputLimitNew},
		{"QueueFull", queueFull},
		{"AppendOrder", appendOrder},
		{"AppendRetry", appendRetry},
		{"FollowUp", followUp},
		{"Requests", requests},
		{"FreezeRows", freezeRows},
		{"Unfinished", unfinished},
		{"TasksAndRetire", tasksAndRetire},
		{"Daemon", daemon},
		{"Missing", missing},
		{"Views", views},
	} {
		t.Run(c.name, func(t *testing.T) { c.f(t, open(t)) })
	}
}

func spec(files ...task.File) task.Spec {
	return task.Spec{Command: []string{"codex", "exec"}, Image: "dev", Size: "dev",
		Timeout: time.Hour, Inputs: files}
}

func submit(t *testing.T, s taskstore.Store, id string, sp task.Spec, data []byte) task.Receipt {
	t.Helper()
	r, err := s.Submit(context.Background(), task.Submission{RequestID: id, Spec: sp,
		Actor: alice, At: t0}, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Submit(%s): %v", id, err)
	}
	return r
}

// appendAll appends bodies at the next sequence numbers.
func appendAll(t *testing.T, s taskstore.Store, id task.RunID, bodies ...task.Body) {
	t.Helper()
	ctx := context.Background()
	r, err := s.Run(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range bodies {
		c := task.Checkpoint{Run: id, Seq: len(r.Log) + i + 1, At: t0, By: "d1", Body: b}
		if err := s.Append(ctx, c); err != nil {
			t.Fatalf("Append(%T): %v", b, err)
		}
	}
}

func submitRetry(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	sub := task.Submission{RequestID: "req-1", Spec: spec(), Actor: alice,
		At: time.Date(2025, 10, 28, 12, 0, 0, 123456789, time.FixedZone("east", 3*60*60))}
	a, err := s.Submit(ctx, sub, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub.At = sub.At.Add(time.Minute)
	b, err := s.Submit(ctx, sub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("retry got %v, first got %v", b, a)
	}
	other := spec()
	other.Command = []string{"claude"}
	_, err = s.Submit(context.Background(), task.Submission{RequestID: "req-1", Spec: other,
		Actor: alice, At: t0}, nil)
	if !errors.Is(err, task.ErrConflict) {
		t.Fatalf("reused request ID with another spec: got %v, want ErrConflict", err)
	}
	got, err := s.Receipt(context.Background(), "req-1")
	if err != nil || got != a {
		t.Fatalf("Receipt = %v, %v", got, err)
	}
	run, err := s.Run(ctx, a.Run)
	if err != nil || run.Submitted != a.At ||
		a.At != time.Date(2025, 10, 28, 9, 0, 0, 123000000, time.UTC) {
		t.Fatalf("submitted = %v, receipt = %v, err = %v", run.Submitted, a.At, err)
	}
}

func submitInputs(t *testing.T, s taskstore.Store) {
	sp := spec(task.File{Name: task.PromptName, Size: 6}, task.File{Name: "notes.md", Size: 3})
	rec := submit(t, s, "req-1", sp, []byte("fix itabc"))
	r, err := s.Run(context.Background(), rec.Run)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]byte{[]byte("fix it"), []byte("abc")} {
		rc, err := s.Blob(r.Spec.Inputs[i].Hash)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		got.ReadFrom(rc)
		rc.Close()
		if !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("input %d = %q, want %q", i, got.Bytes(), want)
		}
	}
	bad := spec(task.File{Name: ".manifest", Size: 1})
	if _, err := s.Submit(context.Background(), task.Submission{RequestID: "req-2", Spec: bad,
		Actor: alice, At: t0}, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Submit accepted an input named .manifest")
	}
}

func queueFull(t *testing.T, s taskstore.Store) {
	for i := range task.MaxQueuedRuns {
		submit(t, s, "req-"+string(rune('a'+i)), spec(), nil)
	}
	_, err := s.Submit(context.Background(), task.Submission{RequestID: "one-more", Spec: spec(),
		Actor: alice, At: t0}, nil)
	if !errors.Is(err, task.ErrQueueFull) {
		t.Fatalf("got %v, want ErrQueueFull", err)
	}
}

func appendOrder(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	id := submit(t, s, "req-1", spec(), nil).Run
	skip := task.Checkpoint{Run: id, Seq: 1, At: t0, By: "d1", Body: &task.UploadIntent{}}
	if err := s.Append(ctx, skip); err == nil {
		t.Fatal("Append let a run start with an upload")
	}
	appendAll(t, s, id, &task.MachineIntent{})
	gap := task.Checkpoint{Run: id, Seq: 3, At: t0, By: "d1", Body: &task.MachineResult{ID: "x"}}
	if err := s.Append(ctx, gap); !errors.Is(err, taskstore.ErrStale) {
		t.Fatalf("Append past the end: got %v, want ErrStale", err)
	}
	other := task.Checkpoint{Run: id, Seq: 1, At: t0, By: "d2",
		Body: &task.StopIntent{Outcome: task.Cancelled}}
	if err := s.Append(ctx, other); !errors.Is(err, taskstore.ErrStale) {
		t.Fatalf("a second writer at seq 1: got %v, want ErrStale", err)
	}
}

func appendRetry(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	id := submit(t, s, "req-1", spec(), nil).Run
	c := task.Checkpoint{Run: id, Seq: 1, At: t0, By: "d1", Body: &task.MachineIntent{}}
	for range 2 {
		if err := s.Append(ctx, c); err != nil {
			t.Fatalf("Append of the same checkpoint twice: %v", err)
		}
	}
	r, _ := s.Run(ctx, id)
	if len(r.Log) != 1 {
		t.Fatalf("log has %d checkpoints, want 1", len(r.Log))
	}
}

func finish(t *testing.T, s taskstore.Store, id task.RunID, sandbox string) {
	appendAll(t, s, id, &task.MachineIntent{}, &task.MachineResult{ID: sandbox},
		&task.UploadIntent{}, &task.UploadResult{},
		&task.StageIntent{Stage: task.StageCommand}, &task.StageLaunched{Stage: task.StageCommand},
		&task.StageExit{Stage: task.StageCommand, Exit: task.ExitRecord{Result: "success"}},
		&task.CollectIntent{}, &task.CollectResult{}, &task.KeepIntent{}, &task.KeepResult{})
}

func followUp(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	first := submit(t, s, "req-1", spec(), nil).Run
	sub := task.Submission{RequestID: "req-2", Task: first.Task, Spec: spec(), Actor: alice, At: t0}
	if _, err := s.Submit(ctx, sub, nil); !errors.Is(err, task.ErrRunLive) {
		t.Fatalf("follow-up while run 1 is live: got %v, want ErrRunLive", err)
	}
	finish(t, s, first, "sbx1")
	rec, err := s.Submit(ctx, sub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Run != (task.RunID{Task: first.Task, N: 2}) {
		t.Fatalf("follow-up is %v", rec.Run)
	}
	r, _ := s.Run(ctx, rec.Run)
	if r.Kept != "sbx1" {
		t.Fatalf("run 2 Kept = %q, want the machine run 1 kept", r.Kept)
	}
	sub.RequestID, sub.Task = "req-3", "t-zzzzzzzz"
	if _, err := s.Submit(ctx, sub, nil); !errors.Is(err, task.ErrNoTask) {
		t.Fatalf("follow-up of no task: got %v, want ErrNoTask", err)
	}
}

func requests(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	id := submit(t, s, "req-1", spec(), nil).Run
	q := taskstore.NewRequest{RequestID: "c-1", Task: id.Task, Kind: task.RequestCancel,
		Actor: alice, At: t0}
	a, err := s.Request(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Request(ctx, q)
	if err != nil || a != b {
		t.Fatalf("retried request = %v, %v, want %v", b, err, a)
	}
	r, _ := s.Run(ctx, id)
	if len(r.Requests) != 1 || r.Requests[0].Run != 1 {
		t.Fatalf("run requests = %v", r.Requests)
	}
	appendAll(t, s, id, &task.StopIntent{Outcome: task.Cancelled, Request: a.ID},
		&task.StopResult{How: "not-started"}, &task.CollectIntent{}, &task.CollectResult{},
		&task.KeepIntent{}, &task.KeepResult{Gone: true})
	q.RequestID = "c-2"
	if _, err := s.Request(ctx, q); !errors.Is(err, taskstore.ErrNotLive) {
		t.Fatalf("cancel with no live run: got %v, want ErrNotLive", err)
	}
	q.RequestID, q.Kind = "rm-1", task.RequestRemove
	if _, err := s.Request(ctx, q); err != nil {
		t.Fatalf("rm with no live run: %v", err)
	}
}

func freezeRows(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	id := submit(t, s, "req-1", spec(), nil).Run
	for _, f := range []task.Freeze{
		{At: t0, Frozen: true, By: "hibd pause"},
		{At: t0.Add(time.Minute), Frozen: false, By: "hibd daemon"},
		{At: t0.Add(2 * time.Minute), Frozen: true, By: "hibd proxy"},
	} {
		if err := s.RecordFreeze(ctx, id.Task, f); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := s.Run(ctx, id)
	if !r.Frozen() || r.Freeze.By != "hibd proxy" {
		t.Fatalf("Freeze = %+v, want the latest row", r.Freeze)
	}
}

func unfinished(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	done := submit(t, s, "req-1", spec(), nil).Run
	live := submit(t, s, "req-2", spec(), nil).Run
	finish(t, s, done, "sbx1")
	runs, err := s.Unfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(runs) != 1 || runs[0].ID != live {
		t.Fatalf("Unfinished = %v, want only %v", runs, live)
	}
}

func tasksAndRetire(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	var ids []task.ID
	for i := range 3 {
		rec, err := s.Submit(ctx, task.Submission{RequestID: string(rune('a' + i)), Spec: spec(),
			Actor: alice, At: t0.Add(time.Duration(i) * time.Minute)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.Run.Task)
	}
	begun := task.Removal{Why: "requested", Request: 7, At: t0.Add(time.Hour)}
	if err := s.Retire(ctx, ids[1], begun); err != nil {
		t.Fatal(err)
	}
	got, err := s.Tasks(ctx)
	if err != nil || len(got) != 3 || got[0].ID != ids[2] ||
		got[1].ID != ids[1] || got[2].ID != ids[0] {
		t.Fatalf("Tasks = %+v, %v", got, err)
	}
	if got[1].Removal == nil || *got[1].Removal != begun {
		t.Fatalf("begun removal = %+v", got[1].Removal)
	}
	done := begun
	done.Done = t0.Add(2 * time.Hour)
	for _, r := range []task.Removal{done, begun} {
		if err := s.Retire(ctx, ids[1], r); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.Task(ctx, ids[1])
	if err != nil || removed.Removal == nil || *removed.Removal != done {
		t.Fatalf("done removal = %+v, %v", removed.Removal, err)
	}
	removed.Removal.Why = "mutated"
	removed, err = s.Task(ctx, ids[1])
	if err != nil || *removed.Removal != done {
		t.Fatalf("aliased removal = %+v, %v", removed.Removal, err)
	}
	got, err = s.Tasks(ctx)
	if err != nil || len(got) != 2 || got[0].ID != ids[2] || got[1].ID != ids[0] {
		t.Fatalf("Tasks after removal = %+v, %v", got, err)
	}
	_, err = s.Submit(ctx, task.Submission{RequestID: "follow", Task: ids[1],
		Spec: spec(), Actor: alice, At: t0}, nil)
	if !errors.Is(err, task.ErrNoTask) {
		t.Fatalf("follow-up of removed task = %v", err)
	}
}

func daemon(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	if got, err := s.Daemon(ctx); err != nil || got != "" {
		t.Fatalf("initial Daemon = %q, %v", got, err)
	}
	for _, inst := range []task.Instance{"d1", "d2"} {
		if err := s.StartDaemon(ctx, inst, t0); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Daemon(ctx); err != nil || got != inst {
			t.Fatalf("Daemon = %q, %v, want %q", got, err, inst)
		}
	}
}

func missing(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	id := task.ID("t-aaaaaaaa")
	if _, err := s.Receipt(ctx, "missing"); !errors.Is(err, taskstore.ErrNoReceipt) {
		t.Fatalf("Receipt = %v", err)
	}
	if _, err := s.Task(ctx, id); !errors.Is(err, task.ErrNoTask) {
		t.Fatalf("Task = %v", err)
	}
	if _, err := s.Run(ctx, task.RunID{Task: id, N: 1}); !errors.Is(err, taskstore.ErrNoRun) {
		t.Fatalf("Run = %v", err)
	}
	if _, err := s.Blob(task.Hash{}); err == nil {
		t.Fatal("Blob accepted an unknown hash")
	}
	if err := s.Retire(ctx, id, task.Removal{At: t0}); !errors.Is(err, task.ErrNoTask) {
		t.Fatalf("Retire = %v", err)
	}
}

func views(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	sp := spec(task.File{Name: "notes", Size: 1})
	first := submit(t, s, "first", sp, []byte("x")).Run
	sp.Command[0], sp.Inputs[0].Name = "changed", "changed"
	finish(t, s, first, "sbx1")
	other := task.Actor{Owner: "not-the-owner", Name: "bob", Source: "api"}
	rec, err := s.Submit(ctx, task.Submission{RequestID: "follow", Task: first.Task,
		Spec: spec(), Actor: other, At: t0.Add(time.Minute)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Request(ctx, taskstore.NewRequest{RequestID: "cancel", Task: first.Task,
		Kind: task.RequestCancel, Actor: other,
		At: t0.Add(123456789 * time.Nanosecond).In(time.FixedZone("east", 3600))})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFreeze(ctx, first.Task, task.Freeze{At: t0, Frozen: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task(ctx, first.Task)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 2 || got.Runs[0].Spec.Command[0] != "codex" ||
		got.Runs[0].Spec.Inputs[0].Name != "notes" || got.Runs[1].Kept != "sbx1" ||
		got.Runs[1].Actor.Owner != alice.Owner || len(got.Requests) != 1 ||
		len(got.Runs[0].Requests) != 0 || !reflect.DeepEqual(got.Runs[1].Requests, got.Requests) ||
		got.Requests[0] != q || q.Actor.Owner != alice.Owner ||
		!got.Runs[0].Frozen() || !got.Runs[1].Frozen() {
		t.Fatalf("merged task = %+v", got)
	}
	run, err := s.Run(ctx, rec.Run)
	if err != nil || !reflect.DeepEqual(run, got.Runs[1]) {
		t.Fatalf("Run differs from Task = %+v, %v", run, err)
	}
	got.Runs[0].Spec.Command[0] = "changed"
	got.Runs[0].Spec.Inputs[0].Name = "changed"
	got, err = s.Task(ctx, first.Task)
	if err != nil || got.Runs[0].Spec.Command[0] != "codex" ||
		got.Runs[0].Spec.Inputs[0].Name != "notes" {
		t.Fatalf("spec mutated through a view = %+v, %v", got, err)
	}
}
