//go:build sqlite

package taskstore_test

import (
	"context"
	"testing"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore/storetest"
)

// TestSQLiteContract runs the contract on a temporary tasks.db. It needs the modernc.org/sqlite
// driver, so it runs under the sqlite tag until M13a adds the module.
func TestSQLiteContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) taskstore.Store {
		s, err := taskstore.Open(context.Background(), statefile.Dir{Path: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

// TestOpenRefusesSymlinks is the store's half of the symlink spike: tasks.db, -wal or -shm as
// a symlink, or owned by someone else, fails Open before SQLite sees the path.
func TestOpenRefusesSymlinks(t *testing.T) {
	t.Skip("TODO: for each of the three names, symlink it to a file outside the directory and " +
		"check that Open fails and the target is untouched")
}
