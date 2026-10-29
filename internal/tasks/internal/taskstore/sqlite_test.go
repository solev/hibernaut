package taskstore_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore/storetest"
)

func TestSQLiteContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) taskstore.Store {
		dir := t.TempDir()
		s, err := taskstore.Open(context.Background(), statefile.Dir{Path: dir})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			entries, err := os.ReadDir(filepath.Join(dir, taskstore.Files, "staging"))
			if err != nil || len(entries) != 0 {
				t.Errorf("staging after contract = %v, %v, want empty", entries, err)
			}
			s.Close()
		})
		return s
	})
}

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := taskstore.Open(context.Background(), statefile.Dir{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{taskstore.DB, taskstore.DB + "-wal", taskstore.DB + "-shm"} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s = %v, %v", name, fi, err)
		}
	}
}

func TestSQLiteRefusesLinkedSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			dir := statefile.Dir{Path: t.TempDir()}
			ctx := context.Background()
			s, err := taskstore.Open(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.StartDaemon(ctx, "initial", time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "target")
			want := []byte("outside data must not change")
			if err := os.WriteFile(target, want, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir.Path, taskstore.DB+suffix)); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(dir.Path, taskstore.DB))
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := db.ExecContext(ctx, `UPDATE daemon SET instance = 'linked' WHERE one = 1`)
			db.Close()
			if writeErr == nil {
				t.Fatal("SQLite accepted a linked " + suffix)
			}
			if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("symlink target = %q, %v", got, err)
			}
		})
	}
}

func TestOpenRefusesSymlinks(t *testing.T) {
	for _, name := range []string{taskstore.DB, taskstore.DB + "-wal", taskstore.DB + "-shm"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "target")
			want := []byte("outside data must not change")
			if err := os.WriteFile(target, want, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			s, err := taskstore.Open(context.Background(), statefile.Dir{Path: dir})
			if err == nil {
				err = s.StartDaemon(context.Background(), "d1", time.Now())
				s.Close()
			}
			if err == nil {
				t.Fatal("accepted a symlinked " + name)
			}
			got, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("symlink target = %q, %v", got, err)
			}
		})
	}
}

func TestSQLiteReadersDoNotWaitForWriter(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := taskstore.Open(ctx, statefile.Dir{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec, err := s.Submit(ctx, task.Submission{RequestID: "first", At: time.Now()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(dir, taskstore.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	if _, err := conn.ExecContext(ctx, `INSERT INTO daemon VALUES (1, 'uncommitted', 0)`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if tasks, err := s.Tasks(readCtx); err != nil || len(tasks) != 1 {
		t.Fatalf("Tasks beside a writer = %v, %v", tasks, err)
	}
	if got, err := s.Task(readCtx, rec.Run.Task); err != nil || got.ID != rec.Run.Task {
		t.Fatalf("Task beside a writer = %v, %v", got, err)
	}
	if _, err := s.Run(readCtx, rec.Run); err != nil {
		t.Fatalf("Run beside a writer = %v", err)
	}
	if _, err := s.Unfinished(readCtx); err != nil {
		t.Fatalf("Unfinished beside a writer = %v", err)
	}
	start := time.Now()
	err = s.StartDaemon(ctx, "blocked", time.Now())
	if !errors.Is(err, taskstore.ErrBusy) {
		t.Fatalf("write beside a writer = %v, want ErrBusy", err)
	}
	if elapsed := time.Since(start); elapsed < time.Second || elapsed > 5*time.Second {
		t.Fatalf("busy timeout took %s, want about 2s", elapsed)
	}
}

func TestSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	dir := statefile.Dir{Path: t.TempDir()}
	s, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	rec, err := s.Submit(ctx, task.Submission{RequestID: "first",
		Spec: task.Spec{Inputs: []task.File{{Name: "prompt", Size: 3}}}, At: time.Now()},
		bytes.NewBufferString("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Receipt(ctx, "first"); err != nil || got != rec {
		t.Fatalf("Receipt after reopen = %v, %v", got, err)
	}
	got, err := s.Run(ctx, rec.Run)
	if err != nil || len(got.Spec.Inputs) != 1 {
		t.Fatalf("Run after reopen = %v, %v", got, err)
	}
	blob, err := s.Blob(got.Spec.Inputs[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	defer blob.Close()
	var data bytes.Buffer
	if _, err := data.ReadFrom(blob); err != nil || data.String() != "abc" {
		t.Fatalf("Blob after reopen = %q, %v", data.String(), err)
	}
}

func TestRemovingDoesNotReadOtherRuns(t *testing.T) {
	ctx := t.Context()
	dir := statefile.Dir{Path: t.TempDir()}
	s, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []task.ID
	for _, name := range []string{"removing", "unrelated"} {
		rec, err := s.Submit(ctx, task.Submission{RequestID: name, At: time.Now()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.Run.Task)
	}
	if _, err := s.Request(ctx, taskstore.NewRequest{Task: ids[0],
		Kind: task.RequestRemove, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(dir.Path, taskstore.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `UPDATE runs SET spec = 'invalid' WHERE task = ?`,
		ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Task(ctx, ids[1]); err == nil {
		t.Fatal("the unrelated task's run is still readable")
	}
	got, err := s.Removing(ctx)
	if err != nil || len(got) != 1 || got[0].ID != ids[0] {
		t.Fatalf("Removing beside an unreadable unrelated run = %v, %v", got, err)
	}
}

func TestSQLiteSubmitCleansStaging(t *testing.T) {
	ctx := context.Background()
	dir := statefile.Dir{Path: t.TempDir()}
	s, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sub := task.Submission{RequestID: "inputs", Spec: task.Spec{Inputs: []task.File{
		{Name: "a", Size: 1}, {Name: "b", Size: 2},
	}}, At: time.Now()}
	for _, data := range []string{"a", "abc", "abc", "xyz"} {
		_, err := s.Submit(ctx, sub, bytes.NewBufferString(data))
		if (data == "a" || data == "xyz") != (err != nil) {
			t.Fatalf("Submit(%q) = %v", data, err)
		}
		entries, err := os.ReadDir(filepath.Join(dir.Path, taskstore.Files, "staging"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("staging after Submit(%q) = %v, %v", data, entries, err)
		}
	}
	other, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if tasks, err := other.Tasks(ctx); err != nil || len(tasks) != 1 {
		t.Fatalf("second process sees Tasks = %v, %v", tasks, err)
	}
}
