package machines

import (
	"os"
	"syscall"
	"testing"
)

func TestRootFirstSweepPreservesDirectoryAndLockOwner(t *testing.T) {
	m, _ := sharedStateManager(t)
	m.euid = func() int { return 0 }
	fi, err := os.Stat(m.dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := fi.Sys().(*syscall.Stat_t)
	var directory, lock bool
	m.chownFile = func(f *os.File, uid, gid int) error {
		fi, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if fi.IsDir() {
			directory = true
		} else {
			lock = true
		}
		if uid != int(owner.Uid) || gid != int(owner.Gid) {
			t.Errorf("ownership = %d:%d, want state owner %d:%d", uid, gid, owner.Uid, owner.Gid)
		}
		return f.Chown(uid, gid)
	}
	if err := m.SweepForks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !directory || !lock {
		t.Fatalf("root didn't preserve new state ownership: directory=%v lock=%v", directory, lock)
	}
	m.euid = os.Geteuid
	if _, err := m.Create(t.Context(), "web", "dev", DefaultIdle); err != nil {
		t.Fatalf("user Create after first root sweep: %v", err)
	}
}
