package storetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func inputLimit(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("x"), task.MaxInputBytes/2)
	first := task.Submission{RequestID: "first", Spec: spec(
		task.File{Name: task.PromptName, Size: int64(len(data) / 2)},
		task.File{Name: "notes", Size: int64(len(data) / 2)}), Actor: alice, At: t0}
	a, err := s.Submit(ctx, first, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	finish(t, s, a.Run, "sbx1")
	second := task.Submission{RequestID: "second", Task: a.Run.Task,
		Spec: first.Spec, Actor: alice, At: t0}
	b, err := s.Submit(ctx, second, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("follow-up reaching exactly MaxInputBytes: %v", err)
	}
	finish(t, s, b.Run, "sbx1")
	before, err := s.Task(ctx, a.Run.Task)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []task.Submission{first, second} {
		want := a
		if sub.RequestID == second.RequestID {
			want = b
		}
		if got, err := s.Submit(ctx, sub, bytes.NewReader(data)); err != nil || got != want {
			t.Errorf("accepted replay %s at the task cap = %v, %v, want %v", sub.RequestID, got, err, want)
		}
	}
	refused := task.Submission{RequestID: "over", Task: a.Run.Task,
		Spec: spec(task.File{Name: "extra", Size: 1}), Actor: alice, At: t0}
	for range 2 {
		if _, err := s.Submit(ctx, refused, bytes.NewBufferString("!")); !errors.Is(err, task.ErrInputFull) {
			t.Errorf("follow-up above MaxInputBytes = %v, want ErrInputFull", err)
		}
	}
	after, err := s.Task(ctx, a.Run.Task)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("refusal changed the task: runs=%d, want %d, error %v", len(after.Runs), len(before.Runs), err)
	}
	if _, err := s.Run(ctx, task.RunID{Task: a.Run.Task, N: 3}); !errors.Is(err, taskstore.ErrNoRun) {
		t.Errorf("refused run = %v, want ErrNoRun", err)
	}
	if _, err := s.Receipt(ctx, refused.RequestID); !errors.Is(err, taskstore.ErrNoReceipt) {
		t.Errorf("refused receipt = %v, want ErrNoReceipt", err)
	}
	if rc, err := s.Blob(task.Hash(sha256.Sum256([]byte("!")))); err == nil {
		rc.Close()
		t.Error("refusal published its input blob")
	}
	empty := refused
	empty.RequestID, empty.Spec = "empty", spec()
	if got, err := s.Submit(ctx, empty, nil); err != nil || got.Run.N != 3 {
		t.Errorf("empty follow-up at the cap = %v, %v, want run 3", got, err)
	}
}

func inputLimitNew(t *testing.T, s taskstore.Store) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("x"), task.MaxInputBytes+1)
	sub := task.Submission{RequestID: "over", Spec: spec(
		task.File{Name: task.PromptName, Size: int64(len(data))}), Actor: alice, At: t0}
	if _, err := s.Submit(ctx, sub, bytes.NewReader(data)); !errors.Is(err, task.ErrInputFull) {
		t.Errorf("new task above MaxInputBytes = %v, want ErrInputFull", err)
	}
	if tasks, err := s.Tasks(ctx); err != nil || len(tasks) != 0 {
		t.Errorf("refused new task left %d tasks, error %v", len(tasks), err)
	}
	if runs, err := s.Unfinished(ctx); err != nil || len(runs) != 0 {
		t.Errorf("refused new task left %d unfinished runs, error %v", len(runs), err)
	}
	if _, err := s.Receipt(ctx, sub.RequestID); !errors.Is(err, taskstore.ErrNoReceipt) {
		t.Errorf("refused new task receipt = %v, want ErrNoReceipt", err)
	}
	if rc, err := s.Blob(task.Hash(sha256.Sum256(data))); err == nil {
		rc.Close()
		t.Error("refused new task published its input blob")
	}
}
