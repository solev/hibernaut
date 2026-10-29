package tasks

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func openMemory(dir statefile.Dir) *Tasks {
	t := Open(dir, time.Now)
	t.st = taskstore.NewMemory()
	return t
}

// TestOneDaemon checks a second daemon can't run every task again while the first runs.
func TestOneDaemon(t *testing.T) {
	dir := statefile.Dir{Path: t.TempDir(), EUID: os.Geteuid}
	a, b := openMemory(dir), openMemory(dir)
	life, end := context.WithCancel(t.Context())
	first, err := a.Daemon(life, DaemonConfig{Engine: a.Guard(fake.New(), "test")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Daemon(t.Context(), DaemonConfig{Engine: b.Guard(fake.New(), "test")}); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("second daemon = %v, want ErrDaemonRunning", err)
	}
	end()
	first.Close()
	next, err := b.Daemon(t.Context(), DaemonConfig{Engine: b.Guard(fake.New(), "test")})
	if err != nil {
		t.Fatalf("daemon after the first closed = %v", err)
	}
	next.Close()
}

// TestDaemonNeedsGuard checks the daemon refuses an engine that would skip the guards.
func TestDaemonNeedsGuard(t *testing.T) {
	tk := openMemory(statefile.Dir{Path: t.TempDir(), EUID: os.Geteuid})
	if _, err := tk.Daemon(t.Context(), DaemonConfig{Engine: fake.New()}); err == nil {
		t.Fatal("the daemon ran tasks on an unguarded engine")
	}
	if Guarded(fake.New()) || !Guarded(tk.Guard(fake.New(), "test")) {
		t.Fatal("Guarded doesn't tell a guarded engine from a plain one")
	}
}
