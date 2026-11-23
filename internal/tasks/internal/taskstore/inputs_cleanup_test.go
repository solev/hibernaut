package taskstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solev/hibernaut/internal/statefile"
)

func TestSweepInputsRemovesPreChownFiles(t *testing.T) {
	dir := statefile.Dir{Path: t.TempDir()}
	root, owner, err := dir.Root()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{Files, Files + "/staging"} {
		if err := dir.EnsureDirectory(root, owner, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, contents := range []string{"", "partial input"} {
		name := filepath.Join(dir.Path, Files, "staging", "killed-before-chown")
		if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		foreign := owner
		foreign.UID++
		if err := sweepInputs(dir, root, foreign); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatalf("pre-chown staging file with %d bytes after sweep = %v; want removed", len(contents), err)
		}
	}
}
