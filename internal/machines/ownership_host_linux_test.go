//go:build host

package machines

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRootSharedStateOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create state owned by another UID")
	}
	const uid, gid = 65534, 65534
	m, _ := sharedStateManager(t)
	if err := os.Chown(m.dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(m.keysFile, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := m.SweepForks(t.Context()); err != nil {
		t.Fatal(err)
	}
	pins := filepath.Join(m.dir, "host-keys")
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{
		{pins, 0o700},
		{filepath.Join(m.dir, "hibd.lock"), 0o600},
	} {
		fi, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != uid || st.Gid != gid || fi.Mode().Perm() != tc.mode {
			t.Errorf("%s ownership/mode = %d:%d %v", tc.path, st.Uid, st.Gid, fi.Mode().Perm())
		}
	}
	web, err := m.Create(t.Context(), "web", "dev", DefaultIdle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	made, err := m.Fork(t.Context(), "web", 1)
	if err != nil || len(made) != 1 {
		t.Fatalf("Fork = %+v, %v", made, err)
	}
	for _, id := range []string{web.ID, made[0].ID} {
		fi, err := os.Stat(filepath.Join(pins, id))
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != uid || st.Gid != gid || fi.Mode().Perm() != 0o600 {
			t.Errorf("pin ownership/mode = %d:%d %v", st.Uid, st.Gid, fi.Mode().Perm())
		}
	}
	if err := os.Chown(pins, uid-1, gid); err != nil {
		t.Fatal(err)
	}
	if err := m.SweepForks(t.Context()); err == nil {
		t.Fatal("root accepted host-keys owned by a different user")
	}
}
