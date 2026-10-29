package storetest

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

// RunLogs runs the contract against logs open makes, one fresh set per subtest.
func RunLogs(t *testing.T, open func(*testing.T) taskstore.Logs) {
	t.Run("Offsets", func(t *testing.T) { logOffsets(t, open(t)) })
	t.Run("RunCap", func(t *testing.T) { logRunCap(t, open(t)) })
}

func logOffsets(t *testing.T, logs taskstore.Logs) {
	r := task.RunID{Task: "t-aaaaaaaa", N: 1}
	stage := task.StageCommand
	if size, err := logs.Size(r, stage); err != nil || size != 0 {
		t.Fatalf("missing Size = %d, %v", size, err)
	}
	readLog(t, logs, r, stage, 0, "")
	for _, offset := range []int64{1, -1} {
		if rc, err := logs.Open(r, stage, offset); err == nil {
			rc.Close()
			t.Fatalf("missing Open(%d) succeeded", offset)
		}
	}
	for _, offset := range []int64{1, -1} {
		if err := logs.Append(r, stage, offset, []byte("wrong")); !errors.Is(err, taskstore.ErrStale) {
			t.Fatalf("missing Append(%d) = %v", offset, err)
		}
	}
	for _, chunk := range []string{"abc", "def"} {
		offset, err := logs.Size(r, stage)
		if err != nil {
			t.Fatal(err)
		}
		if err := logs.Append(r, stage, offset, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	for _, offset := range []int64{0, 5, 7, -1} {
		if err := logs.Append(r, stage, offset, []byte("wrong")); !errors.Is(err, taskstore.ErrStale) {
			t.Fatalf("Append(%d) = %v", offset, err)
		}
	}
	readLog(t, logs, r, stage, 0, "abcdef")
	readLog(t, logs, r, stage, 2, "cdef")
	readLog(t, logs, r, stage, 6, "")
	if rc, err := logs.Open(r, stage, 7); err == nil {
		rc.Close()
		t.Fatal("Open past the end succeeded")
	}
}

func readLog(t *testing.T, logs taskstore.Logs, r task.RunID, stage task.Stage, offset int64,
	want string) {
	t.Helper()
	rc, err := logs.Open(r, stage, offset)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil || closeErr != nil || string(got) != want {
		t.Fatalf("Open(%d) = %q, %v, close %v, want %q", offset, got, err, closeErr, want)
	}
}

func logRunCap(t *testing.T, logs taskstore.Logs) {
	r := task.RunID{Task: "t-aaaaaaaa", N: 1}
	stages := task.Stages()
	chunk := bytes.Repeat([]byte("x"), task.MaxLogBytes-1)
	if err := logs.Append(r, stages[0], 0, chunk); err != nil {
		t.Fatal(err)
	}
	type result struct {
		stage task.Stage
		err   error
	}
	results := make(chan result, 2)
	for _, stage := range stages[1:3] {
		go func() { results <- result{stage, logs.Append(r, stage, 0, []byte("z"))} }()
	}
	full := 0
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			readLog(t, logs, r, got.stage, 0, "z")
		case errors.Is(got.err, taskstore.ErrLogFull):
			full++
			readLog(t, logs, r, got.stage, 0, "")
		default:
			t.Fatal(got.err)
		}
	}
	if full != 1 {
		t.Fatalf("%d appends refused, want one at the per-run cap", full)
	}
	if err := logs.Append(r, stages[0], int64(len(chunk)), []byte("x")); !errors.Is(err, taskstore.ErrLogFull) {
		t.Fatalf("append past cap = %v", err)
	}
	if size, err := logs.Size(r, stages[0]); err != nil || size != int64(len(chunk)) {
		t.Fatalf("size after refused append = %d, %v", size, err)
	}
	readLog(t, logs, r, stages[0], int64(len(chunk))-2, "xx")
	r.N++
	if err := logs.Append(r, stages[0], 0, []byte("another run")); err != nil {
		t.Fatalf("independent run = %v", err)
	}
}
