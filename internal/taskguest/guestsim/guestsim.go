// Package guestsim is a fake engine whose machines speak the task protocol: Upload stores files,
// and Exec carries out every argv package taskguest builds against simulated units, markers,
// exit records, logs and a freezer. It lets taskrun and taskmachine run end to end without E2B,
// on a fake clock, with replies that can be lost after the action happened.
//
// It models what the protocol promises, not systemd: runner.sh is tested on its own under sh.
package guestsim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
)

// Behavior is how a simulated stage runs.
type Behavior struct {
	// Output is what the stage writes to its log.
	Output []byte
	// Status is its exit status. 0 means success.
	Status int
	// Runs is how long it runs on the clock while thawed. A stage exits at the first look at or
	// after that.
	Runs time.Duration
	// Publish is what a finish stage writes to the publish file.
	Publish *task.Publication
}

// Sim is a fake.Engine with task machines.
type Sim struct {
	*fake.Engine
	now func() time.Time

	mu     sync.Mutex
	guests map[string]*guest
	// Script says how each stage behaves. nil means every stage succeeds at once with no output.
	Script func(r task.RunID, s task.Stage) Behavior
	// Lose returns true to drop the reply of a call that did happen, as ErrConnectionLost.
	Lose func(id string, argv []string) bool
	// Calls is every Exec and Upload, in order, as argv, for tests that count outside actions.
	Calls [][]string
}

type guest struct {
	files map[string][]byte
	units map[string]*unit
	// starts counts the stages that really started, by unit name.
	starts map[string]int
	// frozen is the stage slice's freezer.
	frozen bool
}

type unit struct {
	dir     string
	stage   task.Stage
	b       Behavior
	ran     time.Duration
	since   time.Time
	active  bool
	exitRec *task.ExitRecord
}

// New returns a Sim on the given clock. opts configure the fake underneath. WithExec is set
// here and must not be passed.
func New(now func() time.Time, opts ...fake.Option) *Sim {
	s := &Sim{now: now, guests: map[string]*guest{}}
	opts = append(slices.Clone(opts), fake.WithClock(now), fake.WithExec(s.exec))
	s.Engine = fake.New(opts...)
	return s
}

func (s *Sim) guest(id string) *guest {
	g := s.guests[id]
	if g == nil {
		g = &guest{files: map[string][]byte{}, units: map[string]*unit{}, starts: map[string]int{}}
		s.guests[id] = g
	}
	return g
}

// Upload stores files in a running machine.
func (s *Sim) Upload(ctx context.Context, id, user string, files []engine.File) error {
	m, err := s.Engine.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.State != engine.Running {
		return fmt.Errorf("upload to %s: %w", id, engine.ErrNotRunning)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, []string{"upload", strconv.Itoa(len(files))})
	g := s.guest(id)
	for _, f := range files {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		g.files[f.Path] = b
	}
	if s.Lose != nil && s.Lose(id, []string{"upload"}) {
		return fmt.Errorf("upload: %w", engine.ErrConnectionLost)
	}
	return nil
}

// File returns a file in a machine, for tests.
func (s *Sim) File(id, p string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.guest(id).files[p]
	return b, ok
}

// Starts counts how many times stage s of run r really started in machine id: launches the
// marker refused don't count.
func (s *Sim) Starts(id string, r task.RunID, st task.Stage) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guest(id).starts[taskguest.Unit(r, st)]
}

// Reboot drops every unit as a guest reboot would: no exit records are written, and markers and
// logs stay on disk.
func (s *Sim) Reboot(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.guest(id)
	g.units, g.frozen = map[string]*unit{}, false
}

// Frozen reports whether machine id's stage slice is frozen.
func (s *Sim) Frozen(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guest(id).frozen
}

func (s *Sim) exec(ctx context.Context, m engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	argv := append([]string{req.Cmd}, req.Args...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, argv)
	g := s.guest(m.ID)
	g.advance(s.now())
	res := g.run(argv, s.now(), s.Script)
	if s.Lose != nil && s.Lose(m.ID, argv) {
		return engine.ExecResult{}, fmt.Errorf("exec %s: %w", argv[0], engine.ErrConnectionLost)
	}
	return res, nil
}

// advance runs the clock forward for thawed units and ends those whose time is up.
func (g *guest) advance(now time.Time) {
	for _, u := range g.units {
		if !u.active {
			continue
		}
		if !g.frozen {
			u.ran += now.Sub(u.since)
		}
		u.since = now
		if !g.frozen && u.ran >= u.b.Runs {
			g.exit(u, task.ExitRecord{Result: result(u.b.Status), Code: "exited",
				Status: u.b.Status})
		}
	}
}

func result(status int) string {
	if status == 0 {
		return "success"
	}
	return "exit-code"
}

// exit ends a unit and writes its exit record and output once.
func (g *guest) exit(u *unit, rec task.ExitRecord) {
	u.active = false
	g.files[path.Join(u.dir, string(u.stage)+".log")] = append(
		g.files[path.Join(u.dir, string(u.stage)+".log")], u.b.Output...)
	if u.b.Publish != nil {
		g.files[path.Join(u.dir, "publish")] = taskguest.FormatPublication(*u.b.Publish)
	}
	if u.exitRec == nil {
		u.exitRec = &rec
		g.files[path.Join(u.dir, string(u.stage)+".exit")] = []byte(rec.Result)
	}
}

func (g *guest) run(argv []string, now time.Time, script func(task.RunID, task.Stage) Behavior) engine.ExecResult {
	switch {
	case argv[0] == "systemd-run":
		return g.launch(argv, now, script)
	case argv[0] == "systemctl" && argv[1] == "freeze":
		g.frozen = true
		return ok("")
	case argv[0] == "systemctl" && argv[1] == "thaw":
		g.frozen = false
		return ok("")
	case argv[0] == "systemctl" && (argv[1] == "stop" || argv[1] == "kill"):
		u := g.units[argv[len(argv)-1]]
		if u != nil && u.active && (argv[1] == "kill" || !g.frozen) {
			g.exit(u, task.ExitRecord{Result: "signal", Code: "killed", Status: -1})
		}
		return ok("")
	case argv[0] == "cat":
		b, found := g.files[argv[1]]
		if !found {
			return engine.ExecResult{ExitCode: 1}
		}
		return ok(string(b))
	case argv[0] == "sh" && len(argv) > 2 && argv[2] == "verify":
		return g.verify(argv[3])
	case argv[0] == "sh" && len(argv) > 2 && argv[2] == "look":
		return g.look(argv[3], task.Stage(argv[4]), argv[5])
	case argv[0] == "sh" && len(argv) > 2 && argv[2] == "log":
		return g.log(argv[3], task.Stage(argv[4]), argv[5], argv[6])
	case argv[0] == "sh" && argv[1] == "-c" && strings.Contains(argv[2], taskguest.Root):
		g.units, g.frozen = map[string]*unit{}, false
		for p := range g.files {
			if strings.HasPrefix(p, taskguest.Root+"/") {
				delete(g.files, p)
			}
		}
		return ok("")
	}
	return engine.ExecResult{ExitCode: 127, Stderr: []byte("guestsim: unknown argv " +
		strings.Join(argv, " "))}
}

func ok(out string) engine.ExecResult { return engine.ExecResult{Stdout: []byte(out)} }

func (g *guest) launch(argv []string, now time.Time, script func(task.RunID, task.Stage) Behavior) engine.ExecResult {
	var name string
	for _, a := range argv {
		if v, found := strings.CutPrefix(a, "--unit="); found {
			name = v
		}
	}
	i := slices.Index(argv, "start")
	dir, stage := argv[i+1], task.Stage(argv[i+2])
	if u := g.units[name]; u != nil && u.active {
		return engine.ExecResult{ExitCode: 1, Stderr: []byte("Unit " + name + " already exists.")}
	}
	marker := path.Join(dir, string(stage)+".started")
	if _, found := g.files[marker]; found {
		return ok("")
	}
	g.files[marker] = nil
	g.starts[name]++
	b := Behavior{}
	if script != nil {
		b = script(runOf(dir), stage)
	}
	g.units[name] = &unit{dir: dir, stage: stage, b: b, since: now, active: true}
	return ok("")
}

// runOf parses a run folder back into its run ID.
func runOf(dir string) task.RunID {
	n, _ := strconv.Atoi(strings.TrimPrefix(path.Base(dir), "r"))
	return task.RunID{Task: task.ID(path.Base(path.Dir(dir))), N: n}
}

func (g *guest) verify(dir string) engine.ExecResult {
	manifest, found := g.files[path.Join(dir, "in", ".manifest")]
	if !found {
		return engine.ExecResult{ExitCode: 2}
	}
	for line := range strings.Lines(string(manifest)) {
		sum, name, _ := strings.Cut(strings.TrimSpace(line), "  ")
		b, found := g.files[path.Clean(path.Join(dir, "in", name))]
		h := sha256.Sum256(b)
		if !found || hex.EncodeToString(h[:]) != sum {
			return engine.ExecResult{ExitCode: 2, Stderr: []byte("runner: an upload doesn't match")}
		}
	}
	return ok("")
}

func (g *guest) look(dir string, st task.Stage, name string) engine.ExecResult {
	var out bytes.Buffer
	_, marker := g.files[path.Join(dir, string(st)+".started")]
	fmt.Fprintf(&out, "marker=%s\n", map[bool]string{true: "yes", false: "no"}[marker])
	u := g.units[name]
	if u != nil && u.exitRec != nil {
		fmt.Fprintf(&out, "exit.result=%s\nexit.code=%s\nexit.status=%d\n", u.exitRec.Result,
			u.exitRec.Code, u.exitRec.Status)
	}
	switch {
	case u != nil && u.active:
		freezer := "running"
		if g.frozen {
			freezer = "frozen-by-parent"
		}
		fmt.Fprintf(&out, "LoadState=loaded\nActiveState=active\nSubState=running\n"+
			"FreezerState=%s\n", freezer)
	default:
		fmt.Fprintf(&out, "LoadState=not-found\nActiveState=inactive\nSubState=dead\n"+
			"FreezerState=running\n")
	}
	if b, found := g.files[path.Join(dir, string(st)+".log")]; found {
		fmt.Fprintf(&out, "log=%d\n", len(b))
	}
	return ok(out.String())
}

func (g *guest) log(dir string, st task.Stage, off, max string) engine.ExecResult {
	o, _ := strconv.ParseInt(off, 10, 64)
	n, _ := strconv.ParseInt(max, 10, 64)
	b := g.files[path.Join(dir, string(st)+".log")]
	chunk := b[min(o, int64(len(b))):min(o+n, int64(len(b)))]
	return ok(fmt.Sprintf("size=%d\n%s\n", len(b), base64.StdEncoding.EncodeToString(chunk)))
}
