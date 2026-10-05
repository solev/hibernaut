package machines

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
)

func TestRootPreservesOwnerBeforePublishingPins(t *testing.T) {
	m, e := sharedStateManager(t)
	m.euid = func() int { return 0 }
	fi, err := os.Stat(m.dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := fi.Sys().(*syscall.Stat_t)
	pinChowns := 0
	m.chownFile = func(f *os.File, uid, gid int) error {
		if uid != int(owner.Uid) || gid != int(owner.Gid) {
			t.Errorf("root changed ownership to %d:%d, want state owner %d:%d", uid, gid, owner.Uid, owner.Gid)
		}
		ms, err := e.List(t.Context(), engine.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) != 0 {
			pinChowns++
			if _, err := os.Lstat(filepath.Join(m.dir, "host-keys", ms[0].ID)); !errors.Is(err, os.ErrNotExist) {
				t.Error("pin was published before its ownership was set")
			}
		}
		return f.Chown(uid, gid)
	}
	web, err := m.Create(t.Context(), "web", "dev", DefaultIdle)
	if err != nil {
		t.Fatal(err)
	}
	if pinChowns != 1 {
		t.Errorf("root pin owner set %d times, want once before publication", pinChowns)
	}
	path := filepath.Join(m.dir, "host-keys", web.ID)
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != owner.Uid || st.Gid != owner.Gid || fi.Mode().Perm() != 0o600 {
		t.Errorf("root pin ownership/mode = %d:%d %v", st.Uid, st.Gid, fi.Mode().Perm())
	}
}

func TestRootDoesNotPublishUnownedPin(t *testing.T) {
	m, e := sharedStateManager(t)
	if err := os.Mkdir(filepath.Join(m.dir, "host-keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Create(filepath.Join(m.dir, "hibd.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	m.euid = func() int { return 0 }
	chownFailed := errors.New("ownership could not be set")
	m.chownFile = func(*os.File, int, int) error { return chownFailed }
	if _, err := m.Create(t.Context(), "web", "dev", DefaultIdle); !errors.Is(err, chownFailed) {
		t.Fatalf("Create = %v, want ownership failure", err)
	}
	ms, err := e.List(t.Context(), engine.ListOptions{})
	if err != nil || len(ms) != 0 {
		t.Errorf("copy whose pin ownership failed wasn't killed: %+v, %v", ms, err)
	}
	entries, err := os.ReadDir(filepath.Join(m.dir, "host-keys"))
	if err != nil || len(entries) != 0 {
		t.Errorf("unowned pin or staging file was published: %v, %v", entries, err)
	}
}
