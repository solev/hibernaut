//go:build host

package machines

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestHostRoomLockWorksForRootAndStateOwner(t *testing.T) {
	if dir := os.Getenv("HIB_ROOM_OWNER_STATE"); dir != "" {
		m := New(Config{StateDir: dir})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		unlock, err := m.lockRoom(ctx)
		if err != nil {
			t.Fatal(err)
		}
		unlock()
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root to run the other admission as the state owner")
	}
	const uid, gid = 65534, 65534
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, first := range []string{"root", "owner"} {
		t.Run(first+" first", func(t *testing.T) {
			dir, err := os.MkdirTemp("", "hib-room-owner-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			})
			if err := os.Chown(dir, uid, gid); err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(dir, "room.test")
			if err := os.WriteFile(exe, binary, 0o755); err != nil {
				t.Fatal(err)
			}
			root := func() {
				m := New(Config{StateDir: dir})
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				unlock, err := m.lockRoom(ctx)
				if err != nil {
					t.Fatal(err)
				}
				unlock()
			}
			owner := func() {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestHostRoomLockWorksForRootAndStateOwner$")
				cmd.Env = append(os.Environ(), "HIB_ROOM_OWNER_STATE="+dir)
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("state owner's admission = %v\n%s", err, out)
				}
			}
			if first == "root" {
				root()
				owner()
			} else {
				owner()
				root()
			}
			fi, err := os.Stat(filepath.Join(dir, RoomLock))
			if err != nil {
				t.Fatal(err)
			}
			st := fi.Sys().(*syscall.Stat_t)
			if st.Uid != uid || st.Gid != gid || fi.Mode().Perm() != 0o600 {
				t.Errorf("room.lock = %d:%d %v, want the state owner's 0600", st.Uid, st.Gid, fi.Mode().Perm())
			}
		})
	}
}

func TestHostRootSharedStateOwnership(t *testing.T) {
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
