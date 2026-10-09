package linkadmit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

var (
	idRE  = regexp.MustCompile(`^[a-z0-9]{1,64}$`)
	runRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// RunWatcher answers which run owns a machine, from E2B's cgroups. It implements links.Runs.
type RunWatcher struct {
	cgroups string

	mu      sync.Mutex
	watches map[runKey]*runWatch
	// events is the loop's source of wake-ups, nil while nothing is watched and no loop runs.
	events notifier
}

type runKey struct {
	id  string
	run links.Run
}

type runWatch struct {
	dir    string
	ended  chan struct{}
	handle int
}

// notifier wakes the watch loop when a watched cgroup may have changed. It is a hint only: the
// loop re-reads every watched cgroup each time it wakes, and wakes at least every backstop.
type notifier interface {
	// add starts watching the cgroup at dir and returns a handle for remove, or -1.
	add(dir string) int
	remove(handle int)
	// wait returns after an event or after the backstop, whichever is first.
	wait()
	close()
}

var _ links.Runs = (*RunWatcher)(nil)

// NewRuns watches runs under the E2B cgroup root, /sys/fs/cgroup/e2b when cgroups is "".
func NewRuns(cgroups string) (*RunWatcher, error) {
	return &RunWatcher{cgroups: cgroups, watches: map[runKey]*runWatch{}}, nil
}

// Current returns the run that owns id now.
func (w *RunWatcher) Current(id string) (links.Run, error) {
	if !idRE.MatchString(id) {
		return "", fmt.Errorf("unexpected machine ID %q", id)
	}
	dirs, err := filepath.Glob(filepath.Join(w.Root(), "sbx-"+id+"-*"))
	if err != nil {
		return "", err
	}
	var live []links.Run
	for _, dir := range dirs {
		gotID, run, ok := parseRunDir(dir)
		if !ok || gotID != id {
			continue
		}
		up, err := populated(dir)
		if err != nil {
			return "", err
		}
		if up {
			live = append(live, run)
		}
	}
	switch len(live) {
	case 0:
		return "", links.ErrNotRunning
	case 1:
		return live[0], nil
	default:
		// Two live runs of one machine is not something E2B does. Picking one could bind a
		// link to the wrong slot, so nothing is admitted until it resolves.
		return "", fmt.Errorf("machine %s has %d live runs", id, len(live))
	}
}

// Watch closes the returned channel when run of machine id ends. A run that has already ended
// gives an already-closed channel, so admission cannot race the first read.
func (w *RunWatcher) Watch(id string, run links.Run) (<-chan struct{}, error) {
	if !idRE.MatchString(id) {
		return nil, fmt.Errorf("unexpected machine ID %q", id)
	}
	if !runRE.MatchString(string(run)) {
		return nil, fmt.Errorf("unexpected run %q", run)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	key := runKey{id: id, run: run}
	if rw, ok := w.watches[key]; ok {
		return rw.ended, nil
	}
	started := w.events == nil
	if started {
		w.events = newNotifier(w.Root())
	}
	dir := filepath.Join(w.Root(), "sbx-"+id+"-"+string(run))
	// Watch before reading, so an end between the two is an event rather than a miss.
	rw := &runWatch{dir: dir, ended: make(chan struct{}), handle: w.events.add(dir)}
	if up, err := populated(dir); err != nil || !up {
		w.events.remove(rw.handle)
		if started {
			w.events.close()
			w.events = nil
		}
		close(rw.ended)
		return rw.ended, nil
	}
	w.watches[key] = rw
	if started {
		go w.loop(w.events)
	}
	return rw.ended, nil
}

// Live returns every machine that has a run now. It reads the cgroups at the time of the call.
func (w *RunWatcher) Live() (map[string]links.Run, error) {
	dirs, err := filepath.Glob(filepath.Join(w.Root(), "sbx-*"))
	if err != nil {
		return nil, err
	}
	live := map[string]links.Run{}
	twice := map[string]bool{}
	for _, dir := range dirs {
		id, run, ok := parseRunDir(dir)
		if !ok {
			continue
		}
		up, err := populated(dir)
		if err != nil {
			return nil, err
		}
		if !up {
			continue
		}
		if _, dup := live[id]; dup {
			twice[id] = true
		}
		live[id] = run
	}
	for id := range twice {
		delete(live, id)
	}
	return live, nil
}

// Root is where the watcher is looking, for tests and for hibd's diagnostics.
func (w *RunWatcher) Root() string {
	if w.cgroups == "" {
		return "/sys/fs/cgroup/e2b"
	}
	return w.cgroups
}

// loop closes the channels of watched runs as they end, and returns once nothing is watched.
func (w *RunWatcher) loop(n notifier) {
	for {
		n.wait()
		w.mu.Lock()
		for key, rw := range w.watches {
			if up, err := populated(rw.dir); err == nil && up {
				continue
			}
			// A cgroup that cannot be read is treated as ended: the link it guards is cut
			// rather than left running on a guess.
			n.remove(rw.handle)
			close(rw.ended)
			delete(w.watches, key)
		}
		if len(w.watches) == 0 {
			w.events = nil
			n.close()
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
	}
}

// parseRunDir splits a run cgroup's path into the machine and the run. Machine IDs have no
// dashes, so the run is everything after the first dash past "sbx-".
func parseRunDir(dir string) (string, links.Run, bool) {
	rest, ok := strings.CutPrefix(filepath.Base(dir), "sbx-")
	if !ok {
		return "", "", false
	}
	id, run, ok := strings.Cut(rest, "-")
	if !ok || !idRE.MatchString(id) || !runRE.MatchString(run) {
		return "", "", false
	}
	return id, links.Run(run), true
}

// populated reports whether the cgroup at dir has a process in it. A cgroup that is gone is not
// populated. Liveness is this rather than the directory, because the cgroups of runs that have
// ended can linger.
func populated(dir string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "populated "); ok {
			return strings.TrimSpace(v) == "1", nil
		}
	}
	return false, nil
}

// pollNotifier wakes only on its backstop.
type pollNotifier struct {
	every time.Duration
	stop  chan struct{}
	once  sync.Once
}

func newPollNotifier(every time.Duration) *pollNotifier {
	return &pollNotifier{every: every, stop: make(chan struct{})}
}

func (p *pollNotifier) add(string) int { return -1 }

func (p *pollNotifier) remove(int) {}

func (p *pollNotifier) wait() {
	t := time.NewTimer(p.every)
	defer t.Stop()
	select {
	case <-t.C:
	case <-p.stop:
	}
}

func (p *pollNotifier) close() { p.once.Do(func() { close(p.stop) }) }
