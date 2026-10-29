package taskstore_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore/storetest"
)

func TestFileLogsContract(t *testing.T) {
	storetest.RunLogs(t, func(t *testing.T) taskstore.Logs {
		return taskstore.FileLogs{Dir: statefile.Dir{Path: t.TempDir()}}
	})
}

func TestFileLogsRefuseSymlinks(t *testing.T) {
	dir := statefile.Dir{Path: t.TempDir()}
	logs := taskstore.FileLogs{Dir: dir}
	r := task.RunID{Task: "t-aaaaaaaa", N: 1}
	stage := task.StageCommand
	if err := logs.Append(r, stage, 0, []byte("first")); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir.Path, taskstore.Files, "logs", r.String(), string(stage))
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	want := []byte("outside")
	if err := os.WriteFile(target, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.Size(r, stage); err == nil {
		t.Fatal("Size accepted a symlink")
	}
	if rc, err := logs.Open(r, stage, 0); err == nil {
		rc.Close()
		t.Fatal("Open accepted a symlink")
	}
	if err := logs.Append(r, stage, int64(len(want)), []byte("changed")); err == nil {
		t.Fatal("Append accepted a symlink")
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("symlink target = %q, %v", got, err)
	}
}
