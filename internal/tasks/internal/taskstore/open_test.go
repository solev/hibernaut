package taskstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

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
