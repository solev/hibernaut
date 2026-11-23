package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func tiedQueue(t *testing.T, s taskstore.Store) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var want []task.ID
	for i := range 30 {
		rec, err := s.Submit(ctx, task.Submission{RequestID: fmt.Sprint(i), Spec: spec(), Actor: alice, At: t0}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, rec.Run.Task)
	}
	slices.Sort(want)
	for read := range 2 {
		runs, err := s.Unfinished(ctx)
		if err != nil || len(runs) != 30 {
			t.Fatalf("read %d Unfinished = %d runs, %v, want 30", read, len(runs), err)
		}
		q := task.QueueOf(runs, t0)
		for i, r := range runs {
			if r.ID.Task != want[i] {
				t.Errorf("read %d Unfinished[%d] = %s, want %s", read, i, r.ID.Task, want[i])
			}
			word := "runs"
			if i == 1 {
				word = "run"
			}
			if reason := task.WaitReason(r, q, t0); !strings.Contains(reason, fmt.Sprintf("; %d %s ahead;", i, word)) {
				t.Errorf("read %d queue position for %s = %q, want %d ahead", read, r.ID.Task, reason, i)
			}
		}
		records, err := s.Tasks(ctx)
		if err != nil || len(records) != 30 {
			t.Fatalf("read %d Tasks = %d tasks, %v, want 30", read, len(records), err)
		}
		for i, record := range records {
			if record.ID != want[i] {
				t.Errorf("read %d Tasks[%d] = %s, want %s for tied creation", read, i, record.ID, want[i])
			}
		}
	}
}

func requestsAfterEnd(t *testing.T, s taskstore.Store) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, kind := range []task.RequestKind{task.RequestCancel, task.RequestRescue} {
		t.Run(string(kind), func(t *testing.T) {
			rec, err := s.Submit(ctx, task.Submission{RequestID: string(kind), Spec: spec(), Actor: alice, At: t0}, nil)
			if err != nil {
				t.Fatal(err)
			}

			q := taskstore.NewRequest{RequestID: "retry-" + string(kind), Task: rec.Run.Task,
				Kind: kind, Actor: alice, At: t0}
			first, err := s.Request(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			finish(t, s, rec.Run, "machine")
			q.At, q.Actor.Name = t0.Add(time.Minute), "after-reconnect"
			if got, err := s.Request(ctx, q); err != nil || got != first {
				t.Fatalf("%s replay after end = %+v, %v, want original %+v", kind, got, err, first)
			}
			record, err := s.Task(ctx, rec.Run.Task)
			if err != nil || len(record.Requests) != 1 {
				t.Fatalf("replayed %s recorded another request: %+v, %v", kind, record.Requests, err)
			}
			q.RequestID += "-new"
			if _, err := s.Request(ctx, q); !errors.Is(err, taskstore.ErrNotLive) {
				t.Fatalf("new %s after end = %v, want NOT_LIVE", kind, err)
			}
		})
	}
}
