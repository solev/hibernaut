package machines

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/room"
)

type saveOwnerInfo struct {
	fs.FileInfo
	stat syscall.Stat_t
}

func (f saveOwnerInfo) Sys() any { return &f.stat }

func TestSaveDebrisOwnerClassification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debris")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	key := ".web-" + strings.Repeat("a", 32) + ".tmp-" + strings.Repeat("b", 32)
	for _, tc := range []struct {
		uid  uint32
		want saveEntryKind
	}{{1000, savePublicationDebris}, {0, savePublicationDebris}, {1001, unexpectedSaveFile}} {
		stat := *fi.Sys().(*syscall.Stat_t)
		stat.Uid = tc.uid
		kind, machine := classifySaveEntry(key, saveOwnerInfo{fi, stat}, 1000, 0)
		if kind != tc.want || machine != "web" {
			t.Fatalf("uid %d: kind %v, machine %s", tc.uid, kind, machine)
		}
	}
}

func sharedStateManager(t *testing.T) (*Manager, *fake.Engine) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(keys, ssh.MarshalAuthorizedKey(key), 0o600); err != nil {
		t.Fatal(err)
	}
	e := fake.New(fake.WithTemplates(images.Template("dev")), fake.WithExec(func(_ context.Context, em engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
		if req.Cmd == images.MachineInit {
			return engine.ExecResult{Stdout: ssh.MarshalAuthorizedKey(key)}, nil
		}
		return engine.ExecResult{}, nil
	}))
	m := New(Config{
		Engine: e, StateDir: dir, AuthorizedKeys: keys,
		ReadRoom: func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 3125, DiskFree: 80 << 30}, nil
		},
		Durable: func(context.Context, string) error { return nil },
	})
	return m, e
}

func TestRootUsesPrivateUserState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root state owner")
	}
	m, _ := sharedStateManager(t)
	if _, err := m.Create(t.Context(), "web", "dev", DefaultIdle); err != nil {
		t.Fatal(err)
	}
	m.euid = func() int { return 0 }
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatalf("root image save on user-owned state: %v", err)
	}
	made, err := m.Fork(t.Context(), "web", 1)
	if err != nil || len(made) != 1 {
		t.Fatalf("root fork on user-owned state: %+v, %v", made, err)
	}
	if err := m.SweepForks(t.Context()); err != nil {
		t.Fatalf("root sweep on user-owned state: %v", err)
	}
	m.euid = os.Geteuid
	if err := m.Remove(t.Context(), made[0].Name); err != nil {
		t.Fatalf("user cannot remove root-created copy: %v", err)
	}
	if err := m.SweepForks(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestOtherUsersCannotUseState(t *testing.T) {
	m, _ := sharedStateManager(t)
	m.euid = func() int { return os.Geteuid() + 1 }
	if _, err := m.Create(t.Context(), "web", "dev", DefaultIdle); err == nil {
		t.Fatal("another unprivileged user accessed the state")
	}
}

func TestRootStillRefusesWritableOrSymlinkedState(t *testing.T) {
	for _, unsafe := range []string{"state mode", "pins mode", "pins symlink"} {
		t.Run(unsafe, func(t *testing.T) {
			m, _ := sharedStateManager(t)
			m.euid = func() int { return 0 }
			pins := filepath.Join(m.dir, "host-keys")
			if unsafe == "state mode" {
				if err := os.Chmod(m.dir, 0o777); err != nil {
					t.Fatal(err)
				}
			} else if unsafe == "pins mode" {
				if err := os.Mkdir(pins, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(pins, 0o770); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(t.TempDir(), pins); err != nil {
				t.Fatal(err)
			}
			if err := m.SweepForks(t.Context()); err == nil || errors.Is(err, ErrBusy) {
				t.Fatalf("unsafe root state accepted: %v", err)
			}
		})
	}
}
