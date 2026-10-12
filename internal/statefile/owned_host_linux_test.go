//go:build host

package statefile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A lock root creates first belongs to the state directory's owner, and the login user can take
// it afterwards. The user's half runs with the effective IDs switched, because the test binary
// isn't somewhere another user could run it from.
func TestHostRootLockStaysTheOwners(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create state owned by another UID")
	}
	const uid, gid = 65534, 65534
	dir := t.TempDir()
	if err := os.Chmod(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	d := Dir{Path: dir}
	unlock, ok, err := d.TryLock("links.lock", "link state")
	if err != nil || !ok {
		t.Fatalf("root TryLock: ok %v, err %v", ok, err)
	}
	unlock()
	fi, err := os.Stat(filepath.Join(dir, "links.lock"))
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != uid || st.Gid != gid || fi.Mode().Perm() != 0o600 {
		t.Fatalf("links.lock is %d:%d %v, want %d:%d 0600", st.Uid, st.Gid, fi.Mode().Perm(), uid, gid)
	}

	if err := syscall.Setegid(gid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Seteuid(uid); err != nil {
		_ = syscall.Setegid(0)
		t.Fatal(err)
	}
	unlock, ok, err = d.TryLock("links.lock", "link state")
	if unlock != nil {
		unlock()
	}
	held, heldErr := Held(dir, "links.lock")
	if err := syscall.Seteuid(0); err != nil {
		t.Fatalf("restoring root: %v", err)
	}
	if err := syscall.Setegid(0); err != nil {
		t.Fatalf("restoring root's group: %v", err)
	}
	if err != nil || !ok {
		t.Fatalf("the owner's TryLock after root: ok %v, err %v", ok, err)
	}
	if heldErr != nil || held {
		t.Fatalf("Held after the owner let go: %v, %v", held, heldErr)
	}
}
