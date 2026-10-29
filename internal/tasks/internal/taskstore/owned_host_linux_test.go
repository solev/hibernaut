//go:build host

package taskstore_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func TestHostRootStoreStaysTheOwners(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create state owned by another UID")
	}
	const uid, gid = 65534, 65534
	ownedDir := func(t *testing.T) statefile.Dir {
		t.Helper()
		dir := t.TempDir()
		if err := os.Chmod(filepath.Dir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			t.Fatal(err)
		}
		return statefile.Dir{Path: dir}
	}
	ctx := context.Background()
	dir := ownedDir(t)
	s, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now()
	rec, err := s.Submit(ctx, task.Submission{RequestID: "root", Actor: task.Actor{Owner: "nobody"},
		At: at, Spec: task.Spec{Command: []string{"true"}, Timeout: time.Hour,
			Inputs: []task.File{{Name: "prompt", Size: 5}}}}, bytes.NewBufferString("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, task.Checkpoint{Run: rec.Run, Seq: 1, At: at, By: "root",
		Body: &task.MachineIntent{}}); err != nil {
		t.Fatal(err)
	}
	logs := taskstore.FileLogs{Dir: dir}
	if err := logs.Append(rec.Run, task.StageCommand, 0, []byte("root\n")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{taskstore.DB + "-wal", taskstore.DB + "-shm"} {
		if _, err := os.Lstat(filepath.Join(dir.Path, name)); err != nil {
			t.Fatal(err)
		}
	}
	err = filepath.WalkDir(dir.Path, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		fi, err := entry.Info()
		if err != nil {
			return err
		}
		st := fi.Sys().(*syscall.Stat_t)
		mode := fs.FileMode(0o600)
		if fi.IsDir() {
			mode = 0o700
		} else if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not regular", name)
		}
		if st.Uid != uid || st.Gid != gid || fi.Mode().Perm() != mode {
			return fmt.Errorf("%s is %d:%d %v, want %d:%d %v",
				name, st.Uid, st.Gid, fi.Mode().Perm(), uid, gid, mode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("RootOwnedDatabase", func(t *testing.T) {
		d := ownedDir(t)
		if err := os.WriteFile(filepath.Join(d.Path, taskstore.DB), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if other, err := taskstore.Open(ctx, d); err == nil {
			other.Close()
			t.Fatal("accepted a root-owned database in the user's directory")
		}
	})
	t.Run("LinkedWAL", func(t *testing.T) {
		d := ownedDir(t)
		target := filepath.Join(t.TempDir(), "target")
		want := []byte("outside data")
		if err := os.WriteFile(target, want, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(d.Path, taskstore.DB+"-wal")); err != nil {
			t.Fatal(err)
		}
		if other, err := taskstore.Open(ctx, d); err == nil {
			other.Close()
			t.Fatal("accepted a linked WAL")
		}
		if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("symlink target = %q, %v", got, err)
		}
	})
	if err := syscall.Setegid(gid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Seteuid(uid); err != nil {
		_ = syscall.Setegid(0)
		t.Fatal(err)
	}
	userErr := func() error {
		defer func() {
			if err := syscall.Seteuid(0); err != nil {
				t.Fatalf("restoring root: %v", err)
			}
			if err := syscall.Setegid(0); err != nil {
				t.Fatalf("restoring root's group: %v", err)
			}
		}()
		user, err := taskstore.Open(ctx, dir)
		if err != nil {
			return err
		}
		defer user.Close()
		got, err := user.Task(ctx, rec.Run.Task)
		if err != nil {
			return err
		}
		if len(got.Runs) != 1 || len(got.Runs[0].Log) != 1 {
			return fmt.Errorf("owner read task: %+v", got)
		}
		if err := user.Append(ctx, task.Checkpoint{Run: rec.Run, Seq: 2, At: at, By: "owner",
			Body: &task.MachineResult{ID: "sandbox"}}); err != nil {
			return err
		}
		return logs.Append(rec.Run, task.StageCommand, 5, []byte("owner\n"))
	}()
	if userErr != nil {
		t.Fatalf("the owner's store after root: %v", userErr)
	}
}
