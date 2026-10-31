package storetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func followUpAfterRemove(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	first := submit(t, s, "first", spec(), nil)
	finish(t, s, first.Run, "sbx1")
	earlier := task.Submission{RequestID: "earlier", Task: first.Run.Task, Spec: spec(),
		Actor: alice, At: t0}
	receipt, err := s.Submit(ctx, earlier, nil)
	if err != nil {
		t.Fatal(err)
	}
	finish(t, s, receipt.Run, "sbx1")
	if _, err := s.Request(ctx, taskstore.NewRequest{RequestID: "rm", Task: first.Run.Task,
		Kind: task.RequestRemove, Actor: alice, At: t0}); err != nil {
		t.Fatal(err)
	}
	for i := range task.MaxQueuedRuns {
		submit(t, s, "queued-"+string(rune('a'+i)), spec(), nil)
	}
	replay, err := s.Submit(ctx, earlier, nil)
	if err != nil || replay != receipt {
		t.Fatalf("receipt replay after rm = %v, %v, want %v", replay, err, receipt)
	}
	data := []byte("a new follow-up input")
	_, err = s.Submit(ctx, task.Submission{RequestID: "follow", Task: first.Run.Task,
		Spec: spec(task.File{Name: "new", Size: int64(len(data))}), Actor: alice, At: t0},
		bytes.NewReader(data))
	if !errors.Is(err, task.ErrNoTask) {
		t.Errorf("follow-up after rm = %v, want ErrNoTask", err)
	}
	got, err := s.Task(ctx, first.Run.Task)
	if err != nil || len(got.Runs) != 2 {
		t.Errorf("runs after refused follow-up = %d, %v, want 2", len(got.Runs), err)
	}
	if _, err := s.Receipt(ctx, "follow"); !errors.Is(err, taskstore.ErrNoReceipt) {
		t.Errorf("receipt for refused follow-up = %v, want ErrNoReceipt", err)
	}
	if blob, err := s.Blob(task.Hash(sha256.Sum256(data))); err == nil {
		blob.Close()
		t.Error("refused follow-up published its input blob")
	}
}

func retireLive(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	id := submit(t, s, "first", spec(), nil).Run
	finish(t, s, id, "sbx1")
	snapshot, err := s.Task(ctx, id.Task)
	if err != nil {
		t.Fatal(err)
	}
	removal, ok := task.Retire(snapshot, t0.Add(task.KeepFor))
	if !ok {
		t.Fatal("the ended snapshot has no retention removal")
	}
	if _, err := s.Submit(ctx, task.Submission{RequestID: "follow", Task: id.Task,
		Spec: spec(), Actor: alice, At: t0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Retire(ctx, id.Task, removal); !errors.Is(err, task.ErrRunLive) {
		t.Errorf("Retire after a newer run = %v, want ErrRunLive", err)
	}
	got, err := s.Task(ctx, id.Task)
	if err != nil || got.Removal != nil {
		t.Errorf("stale Retire recorded removal = %+v, %v", got.Removal, err)
	}
}

func requestsAfterRetire(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	for _, kind := range []task.RequestKind{task.RequestCancel, task.RequestRescue, task.RequestRemove} {
		t.Run(string(kind), func(t *testing.T) {
			id := submit(t, s, string(kind), spec(), nil).Run
			q := taskstore.NewRequest{RequestID: "old-" + string(kind), Task: id.Task,
				Kind: kind, Actor: alice, At: t0}
			first, err := s.Request(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			finish(t, s, id, "sbx1")
			r := task.Removal{Why: "rm", Request: first.ID, At: t0}
			for _, done := range []time.Time{{}, t0.Add(time.Minute)} {
				r.Done = done
				if err := s.Retire(ctx, id.Task, r); err != nil {
					t.Fatal(err)
				}
				if got, err := s.Request(ctx, q); err != nil || got != first {
					t.Fatalf("replayed request after Retire = %v, %v, want %v", got, err, first)
				}
				fresh := q
				fresh.RequestID = "new-" + string(kind) + done.String()
				if _, err := s.Request(ctx, fresh); !errors.Is(err, task.ErrNoTask) {
					t.Errorf("new %s after Retire = %v, want ErrNoTask", kind, err)
				}
			}
			got, err := s.Task(ctx, id.Task)
			if err != nil || len(got.Requests) != 1 {
				t.Errorf("requests after refused requests = %d, %v, want 1", len(got.Requests), err)
			}
		})
	}
}

func removing(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	var ids []task.RunID
	for i := range 5 {
		rec, err := s.Submit(ctx, task.Submission{RequestID: string(rune('a' + i)), Spec: spec(),
			Actor: alice, At: t0.Add(time.Duration(i) * time.Minute)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.Run)
	}
	for _, i := range []int{0, 3} {
		if _, err := s.Request(ctx, taskstore.NewRequest{Task: ids[i].Task,
			Kind: task.RequestRemove, Actor: alice, At: t0}); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []int{1, 3} {
		finish(t, s, ids[i], "sbx1")
		r := task.Removal{Why: "retention", At: t0}
		if i == 3 {
			r.Done = t0.Add(time.Hour)
		}
		if err := s.Retire(ctx, ids[i].Task, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Request(ctx, taskstore.NewRequest{Task: ids[4].Task,
		Kind: task.RequestCancel, Actor: alice, At: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFreeze(ctx, ids[0].Task, task.Freeze{At: t0, Frozen: true}); err != nil {
		t.Fatal(err)
	}
	var want []task.Task
	for _, i := range []int{0, 1} {
		got, err := s.Task(ctx, ids[i].Task)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, got)
	}
	got, err := s.Removing(ctx)
	if err != nil || len(got) != len(want) {
		t.Fatalf("Removing returned %d tasks, %v, want 2", len(got), err)
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Removing = %+v, %v, want only rm and open removal oldest first %+v", got, err, want)
	}
}

func requestIDConflict(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	for _, kind := range []task.RequestKind{task.RequestCancel, task.RequestRescue, task.RequestRemove} {
		t.Run(string(kind), func(t *testing.T) {
			a := submit(t, s, "a-"+string(kind), spec(), nil).Run
			b := submit(t, s, "b-"+string(kind), spec(), nil).Run
			q := taskstore.NewRequest{RequestID: "shared-" + string(kind), Task: a.Task,
				Kind: kind, Actor: alice, At: t0}
			first, err := s.Request(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []taskstore.NewRequest{
				{RequestID: q.RequestID, Task: b.Task, Kind: kind, Actor: alice, At: t0},
				{RequestID: q.RequestID, Task: a.Task, Kind: nextKind(kind), Actor: alice, At: t0},
			} {
				if _, err := s.Request(ctx, candidate); !errors.Is(err, task.ErrConflict) {
					t.Errorf("reused request ID for task %s kind %s = %v, want ErrConflict",
						candidate.Task, candidate.Kind, err)
				}
			}
			q.At, q.Actor.Name = t0.Add(time.Hour), "bob"
			if got, err := s.Request(ctx, q); err != nil || got != first {
				t.Errorf("same task and kind replay = %v, %v, want %v", got, err, first)
			}
			for i, id := range []task.ID{a.Task, b.Task} {
				got, err := s.Task(ctx, id)
				if err != nil || len(got.Requests) != 1-i {
					t.Errorf("requests after conflicts = %d, %v, want %d", len(got.Requests), err, 1-i)
				}
			}
		})
	}
}

func nextKind(k task.RequestKind) task.RequestKind {
	if k == task.RequestCancel {
		return task.RequestRemove
	}
	return task.RequestCancel
}
