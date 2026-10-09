package linkadmit

import (
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// runBackstop is how often the watch loop re-reads every watched cgroup even without an event.
// The events are the mechanism. This only bounds what a missed one costs.
const runBackstop = time.Second

// inotifier wakes the loop on inotify events: cgroup.events is modified when populated changes,
// and removing a run's cgroup is a delete on its parent.
type inotifier struct {
	fd   int
	buf  []byte
	once sync.Once
}

func newNotifier(root string) notifier {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return newPollNotifier(runBackstop)
	}
	// The root may not exist yet on a host whose E2B is still being installed. The backstop
	// covers that.
	_, _ = unix.InotifyAddWatch(fd, root, unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_DELETE_SELF|unix.IN_ONLYDIR)
	return &inotifier{fd: fd, buf: make([]byte, 64<<10)}
}

func (n *inotifier) add(dir string) int {
	wd, err := unix.InotifyAddWatch(n.fd, filepath.Join(dir, "cgroup.events"), unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_DELETE_SELF)
	if err != nil {
		return -1
	}
	return wd
}

func (n *inotifier) remove(wd int) {
	if wd >= 0 {
		_, _ = unix.InotifyRmWatch(n.fd, uint32(wd))
	}
}

func (n *inotifier) wait() {
	fds := []unix.PollFd{{Fd: int32(n.fd), Events: unix.POLLIN}}
	if _, err := unix.Poll(fds, int(runBackstop/time.Millisecond)); err != nil && err != unix.EINTR {
		time.Sleep(runBackstop)
		return
	}
	for {
		if _, err := unix.Read(n.fd, n.buf); err != nil {
			return
		}
	}
}

func (n *inotifier) close() { n.once.Do(func() { unix.Close(n.fd) }) }
