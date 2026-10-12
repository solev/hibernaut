//go:build host

package linkadmit_test

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/links"
)

// TestHostRunWatcherSeesARealCgroup: over real cgroup v2 directories, as E2B makes them, a
// run's watch closes when its last process exits, woken by the kernel's cgroup.events rather
// than the backstop. A run whose cgroup is gone is closed from the start, and the next run of
// the machine is a new watch that closes when its process exits and its cgroup goes at once,
// as when E2B stops a sandbox.
func TestHostRunWatcherSeesARealCgroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("needs root to make cgroups")
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Fatalf("needs cgroup v2 at /sys/fs/cgroup: %v", err)
	}
	root, err := os.MkdirTemp("/sys/fs/cgroup", "hibernaut-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rmCgroup(t, root) })
	w, err := linkadmit.NewRuns(root)
	if err != nil {
		t.Fatal(err)
	}

	dir, kill := runInCgroup(t, root, "api", "r1")
	ended, err := w.Watch("api", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if closedWithin(ended, 300*time.Millisecond) {
		t.Fatal("closed while the run's process is alive")
	}
	if live, err := w.Live(); err != nil || !maps.Equal(live, map[string]links.Run{"api": "r1"}) {
		t.Fatalf("Live = %v, %v, want api's r1", live, err)
	}
	killed := time.Now()
	kill()
	if !closedWithin(ended, 5*time.Second) {
		t.Fatal("not closed after the run's last process exited")
	}
	if took := time.Since(killed); took >= linkadmit.RunBackstop/2 {
		t.Errorf("closed %s after SIGKILL, so the backstop closed it and not the event", took)
	} else {
		t.Logf("closed %s after SIGKILL", took)
	}
	if _, err := w.Current("api"); !errors.Is(err, links.ErrNotRunning) {
		t.Errorf("Current(api) with its cgroup empty = %v, want ErrNotRunning", err)
	}
	rmCgroup(t, dir)
	if gone, err := w.Watch("api", "r1"); err != nil || !closedWithin(gone, 0) {
		t.Errorf("a watch of the run whose cgroup is gone is open (%v), want it closed", err)
	}

	dir, kill = runInCgroup(t, root, "api", "r2")
	next, err := w.Watch("api", "r2")
	if err != nil {
		t.Fatal(err)
	}
	if closedWithin(next, 300*time.Millisecond) {
		t.Fatal("the next run's watch is closed while its process is alive")
	}
	if run, err := w.Current("api"); err != nil || run != "r2" {
		t.Errorf("Current(api) = %q, %v, want r2", run, err)
	}
	kill()
	rmCgroup(t, dir)
	if !closedWithin(next, 5*time.Second) {
		t.Fatal("not closed after the run's process exited and its cgroup went")
	}
}

// runInCgroup makes the cgroup of a run of machine id under root and starts a process in it. The
// process is not the test's child, as E2B's are not hibd's: a child's exit would send the test
// SIGCHLD, which interrupts the watcher's poll and wakes it without any event. kill ends the
// process and waits until it has exited.
func runInCgroup(t *testing.T, root, id string, run links.Run) (dir string, kill func()) {
	t.Helper()
	dir = filepath.Join(root, "sbx-"+id+"-"+string(run))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rmCgroup(t, dir) })
	out, err := exec.Command("sh", "-c", "sleep 600 >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal(err)
	}
	var once sync.Once
	kill = func() {
		once.Do(func() {
			defer unix.Close(pidfd)
			if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil {
				t.Errorf("killing process %d: %v", pid, err)
				return
			}
			fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
			for {
				n, err := unix.Poll(fds, 5000)
				if err == unix.EINTR {
					continue
				}
				if err != nil || n == 0 {
					t.Errorf("process %d has not exited 5s after SIGKILL: %v", pid, err)
				}
				return
			}
		})
	}
	t.Cleanup(kill)
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0); err != nil {
		t.Fatal(err)
	}
	return dir, kill
}

// rmCgroup removes a cgroup with rmdir, since the files in one can't be unlinked. One that is
// already gone is fine.
func rmCgroup(t *testing.T, dir string) {
	if err := syscall.Rmdir(dir); err != nil && !errors.Is(err, syscall.ENOENT) {
		t.Errorf("removing cgroup %s: %v", dir, err)
	}
}
