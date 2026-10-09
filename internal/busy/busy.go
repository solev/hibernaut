// Package busy keeps busy machines running.
package busy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

const (
	// CPUBusy is the share of one CPU a machine must use, on average between two looks, to be
	// busy.
	CPUBusy = 0.10

	callTimeout = 10 * time.Second
)

// DefaultAgents are the coding agents that keep a machine busy while they run.
var DefaultAgents = []string{"codex", "copilot", "claude"}

var (
	agentRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	idRE    = regexp.MustCompile(`^[a-z0-9]{1,64}$`)
)

// Config configures a Watcher. CPU, Now and Logf may be called concurrently.
type Config struct {
	Engine engine.Engine
	// CPU returns how much CPU time machine id has used. On the host, that's CgroupCPU.
	CPU func(id string) (time.Duration, error)
	// Agents are the programs that keep a machine busy while they run, by the name they run as.
	// Default DefaultAgents.
	Agents []string
	// Now tells the time. Default time.Now.
	Now func() time.Time
	// Logf logs machines turning busy or idle, and problems looking at them. Default: no log.
	Logf func(format string, args ...any)
	// Activity records that a machine did something that was not a link, at the time it was
	// seen. M12's links read it: a machine whose own activity has gone stale may not wake or
	// renew another, which is what stops a ring of machines holding each other up. The
	// watcher calls it for every busy observation, machines with idle=0 included — they never
	// pause, so there is nothing to renew, but whether they are working still decides whether
	// their links count. Default: not recorded.
	Activity func(id string, at time.Time)
	// StateDir is where the activity clock is kept across restarts of hibd, usually
	// HIBD_STATE_DIR. Empty keeps it in memory only.
	StateDir string
}

// Watcher keeps busy machines running. Its methods must not be called concurrently.
type Watcher struct {
	e     engine.Engine
	cpu   func(id string) (time.Duration, error)
	pgrep []string
	now   func() time.Time
	logf  func(format string, args ...any)
	seen  map[string]*seen
}

type seen struct {
	name     string
	last     reading
	logged   string
	problems map[string]string
}

type reading struct {
	ok        bool
	startedAt time.Time
	used      time.Duration
	at        time.Time
}

// New returns a Watcher.
func New(cfg Config) (*Watcher, error) {
	if cfg.Engine == nil || cfg.CPU == nil {
		return nil, errors.New("busy: Config.Engine and Config.CPU are required")
	}
	agents := cfg.Agents
	if len(agents) == 0 {
		agents = DefaultAgents
	}
	quoted := make([]string, len(agents))
	for i, a := range agents {
		if !agentRE.MatchString(a) {
			return nil, fmt.Errorf("bad agent name %q: want letters, digits, '.', '_' and '-'", a)
		}
		quoted[i] = regexp.QuoteMeta(a)
	}
	w := &Watcher{
		e:     cfg.Engine,
		cpu:   cfg.CPU,
		pgrep: []string{"-f", `(^|[ /])(` + strings.Join(quoted, "|") + `)( |$)`},
		now:   cfg.Now,
		logf:  cfg.Logf,
		seen:  map[string]*seen{},
	}
	if w.now == nil {
		w.now = time.Now
	}
	if w.logf == nil {
		w.logf = func(string, ...any) {}
	}
	return w, nil
}

// Tick looks once at each running machine hibd made that pauses when idle, and makes each busy
// one run for at least its idle time from now. A machine is busy if it used at least CPUBusy
// of a CPU since the last Tick, or a coding agent is running in it. Tick never shortens a
// timeout or touches a paused machine. It logs problems with a machine once, and returns an
// error only if it can't list the machines.
func (w *Watcher) Tick(ctx context.Context) error {
	lctx, cancel := context.WithTimeout(ctx, callTimeout)
	ms, err := w.e.List(lctx, engine.ListOptions{States: []engine.State{engine.Running}})
	cancel()
	if err != nil {
		return fmt.Errorf("listing machines: %w", err)
	}
	running := map[string]bool{}
	var wg sync.WaitGroup
	for _, m := range ms {
		idle, ok := machines.Idle(m)
		if !ok || idle == 0 || m.State != engine.Running || running[m.ID] {
			continue
		}
		running[m.ID] = true
		s := w.seen[m.ID]
		if s == nil {
			s = &seen{name: machines.Name(m), problems: map[string]string{}}
			w.seen[m.ID] = s
		}
		wg.Go(func() { w.look(ctx, m, s, idle) })
	}
	wg.Wait()
	for id := range w.seen {
		if !running[id] {
			delete(w.seen, id)
		}
	}
	return nil
}

func (w *Watcher) look(ctx context.Context, m engine.Machine, s *seen, idle time.Duration) {
	why := ""
	if share, ok := w.cpuShare(m, s); ok && share >= CPUBusy {
		why = fmt.Sprintf("using %.0f%% of a CPU", share*100)
	} else if w.agentRunning(ctx, m, s) {
		why = "a coding agent is running"
	}
	switch {
	case why != "" && s.logged != "busy":
		s.logged = "busy"
		w.logf("%s is busy: %s", s.name, why)
	case why == "" && s.logged != "idle":
		s.logged = "idle"
		w.logf("%s is idle", s.name)
	}
	if why == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	err := machines.Renew(ctx, w.e, m, idle, w.now())
	if errors.Is(err, engine.ErrNotRunning) || errors.Is(err, engine.ErrNotFound) {
		err = nil
	}
	w.report(s, "keeping it running", err)
}

func (w *Watcher) cpuShare(m engine.Machine, s *seen) (float64, bool) {
	used, err := w.cpu(m.ID)
	w.report(s, "reading its CPU time", err)
	if err != nil {
		s.last = reading{}
		return 0, false
	}
	prev := s.last
	s.last = reading{ok: true, startedAt: m.StartedAt, used: used, at: w.now()}
	if !prev.ok || !prev.startedAt.Equal(m.StartedAt) || used < prev.used || !s.last.at.After(prev.at) {
		return 0, false
	}
	return float64(used-prev.used) / float64(s.last.at.Sub(prev.at)), true
}

func (w *Watcher) agentRunning(ctx context.Context, m engine.Machine, s *seen) bool {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := w.e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "pgrep", Args: w.pgrep, User: "root"})
	if errors.Is(err, engine.ErrNotRunning) || errors.Is(err, engine.ErrNotFound) {
		return false
	}
	if err == nil && res.ExitCode != 0 && res.ExitCode != 1 {
		err = fmt.Errorf("pgrep exited %d: %q", res.ExitCode, tail(res.Stderr, 200))
	}
	w.report(s, "looking for coding agents", err)
	return err == nil && res.ExitCode == 0
}

func (w *Watcher) report(s *seen, doing string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if s.problems[doing] == msg {
		return
	}
	s.problems[doing] = msg
	if err != nil {
		w.logf("%s: %s: %v", s.name, doing, err)
	}
}

// CgroupCPU returns a Config.CPU that reads machines' CPU time from E2B's cgroups under root
// (/sys/fs/cgroup/e2b), where each run of machine ID has a cgroup sbx-ID-RUN. The cgroups of
// runs that have ended but are still there are counted too: they don't change.
func CgroupCPU(root string) func(id string) (time.Duration, error) {
	return func(id string) (time.Duration, error) {
		if !idRE.MatchString(id) {
			return 0, fmt.Errorf("unexpected machine ID %q", id)
		}
		dirs, err := filepath.Glob(filepath.Join(root, "sbx-"+id+"-*"))
		if err != nil {
			return 0, err
		}
		var total time.Duration
		found := 0
		for _, dir := range dirs {
			used, err := usage(filepath.Join(dir, "cpu.stat"))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return 0, err
			}
			total += used
			found++
		}
		if found == 0 {
			return 0, fmt.Errorf("no cgroup for machine %s in %s", id, root)
		}
		return total, nil
	}
}

func usage(path string) (time.Duration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			us, err := strconv.ParseInt(v, 10, 64)
			if err != nil || us < 0 {
				return 0, fmt.Errorf("%s: bad usage_usec %q", path, v)
			}
			return time.Duration(us) * time.Microsecond, nil
		}
	}
	return 0, fmt.Errorf("%s: no usage_usec", path)
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
