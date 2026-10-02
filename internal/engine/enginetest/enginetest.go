// Package enginetest is the contract every engine.Engine meets. It runs against the fake in unit
// tests and against real E2B end to end (e2e TestEngineContract), so the fake can't drift.
package enginetest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

// Config is what Run needs.
type Config struct {
	Engine engine.Engine
	// Template the suite creates machines from.
	Template string
	// Metadata is added to every machine the suite creates, so a crashed run's leftovers can be
	// found and removed.
	Metadata map[string]string
	// Wait bounds polling for eventually-consistent results, such as the first metrics sample.
	// Zero means one minute.
	Wait time.Duration
	// Timeout bounds each subtest. Zero means three minutes.
	Timeout time.Duration
	// Users says the engine runs commands as other users: ExecRequest.User "root" runs as root,
	// and the default user isn't root.
	Users bool
}

// Run runs the contract suite. Subtests run in parallel, each with its own machines.
func Run(t *testing.T, cfg Config) {
	if cfg.Wait == 0 {
		cfg.Wait = time.Minute
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 3 * time.Minute
	}
	s := &suite{cfg: cfg, runID: RandomID()}

	t.Run("UnknownMachine", s.unknownMachine)
	t.Run("UnknownTemplate", s.unknownTemplate)
	t.Run("Lifecycle", s.lifecycle)
	t.Run("Exec", s.exec)
	t.Run("KillPaused", s.killPaused)
}

type suite struct {
	cfg   Config
	runID string
}

const (
	runKey  = "hib-contract-run"
	caseKey = "hib-contract-case"
)

func (s *suite) ctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), s.cfg.Timeout)
	t.Cleanup(cancel)
	return ctx
}

// labels returns the metadata that marks machines of one subtest.
func (s *suite) labels(name string) map[string]string {
	return map[string]string{runKey: s.runID, caseKey: name}
}

func (s *suite) create(t *testing.T, ctx context.Context, name string, timeout time.Duration) engine.Machine {
	t.Helper()
	md := maps.Clone(s.cfg.Metadata)
	if md == nil {
		md = map[string]string{}
	}
	maps.Copy(md, s.labels(name))
	m, err := s.cfg.Engine.Create(ctx, engine.CreateOptions{Template: s.cfg.Template, Timeout: timeout, Metadata: md})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.cfg.Engine.Kill(ctx, m.ID); err != nil && !errors.Is(err, engine.ErrNotFound) {
			t.Errorf("cleanup: Kill(%s): %v", m.ID, err)
		}
	})
	return m
}

func (s *suite) unknownMachine(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	id := RandomID()

	calls := map[string]func() error{
		"Get":        func() error { _, err := e.Get(ctx, id); return err },
		"Pause":      func() error { return e.Pause(ctx, id) },
		"Resume":     func() error { _, err := e.Resume(ctx, id, time.Minute); return err },
		"Kill":       func() error { return e.Kill(ctx, id) },
		"SetTimeout": func() error { return e.SetTimeout(ctx, id, time.Minute) },
		"Metrics":    func() error { _, err := e.Metrics(ctx, id); return err },
		"Exec":       func() error { _, err := e.Exec(ctx, id, engine.ExecRequest{Cmd: "true"}); return err },
		"DialWebSocket": func() error {
			c, err := e.DialWebSocket(ctx, id, 8080, time.Minute)
			if c != nil {
				c.Close()
			}
			return err
		},
	}
	for _, name := range slices.Sorted(maps.Keys(calls)) {
		if err := calls[name](); !errors.Is(err, engine.ErrNotFound) {
			t.Errorf("%s(unknown id) = %v, want ErrNotFound", name, err)
		}
	}
}

func (s *suite) unknownTemplate(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	md := maps.Clone(s.cfg.Metadata)
	if md == nil {
		md = map[string]string{}
	}
	maps.Copy(md, s.labels("UnknownTemplate"))
	m, err := s.cfg.Engine.Create(ctx, engine.CreateOptions{Template: "hib-contract-none-" + RandomID(), Metadata: md})
	if err == nil {
		s.cfg.Engine.Kill(ctx, m.ID)
	}
	if !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("Create(unknown template) = %v, want ErrNoTemplate", err)
	}
}

func (s *suite) lifecycle(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	labels := s.labels("lifecycle")

	m := s.create(t, ctx, "lifecycle", 10*time.Minute)
	if m.ID == "" || m.State != engine.Running || m.Template == "" || m.MemoryMB <= 0 {
		t.Fatalf("Create = %+v, want an ID, a template, its memory and state running", m)
	}
	if !hasAll(m.Metadata, labels) {
		t.Errorf("Create metadata = %v, want it to contain %v", m.Metadata, labels)
	}
	if got := m.EndAt.Sub(m.StartedAt); got < 10*time.Minute-5*time.Second || got > 10*time.Minute+5*time.Second {
		t.Errorf("EndAt - StartedAt = %v, want the 10m timeout", got)
	}

	got := s.get(t, ctx, m.ID)
	if got.ID != m.ID || got.State != engine.Running || !hasAll(got.Metadata, labels) || got.MemoryMB != m.MemoryMB {
		t.Errorf("Get = %+v, want running %s with %v and %d MiB", got, m.ID, labels, m.MemoryMB)
	}
	s.wantListed(t, ctx, labels, []engine.State{engine.Running}, m.ID)
	s.wantListed(t, ctx, labels, []engine.State{engine.Paused})
	s.wantListed(t, ctx, labels, nil, m.ID)

	s.mustExec(t, ctx, m.ID, "echo persisted > marker")

	// SetTimeout moves EndAt either way.
	before := got.EndAt
	if err := e.SetTimeout(ctx, m.ID, 20*time.Minute); err != nil {
		t.Fatalf("SetTimeout(20m): %v", err)
	}
	longer := s.get(t, ctx, m.ID).EndAt
	if d := longer.Sub(before); d < 10*time.Minute-5*time.Second || d > 11*time.Minute {
		t.Errorf("SetTimeout(20m) after a 10m timeout moved EndAt by %v, want about 10m", d)
	}
	if err := e.SetTimeout(ctx, m.ID, 5*time.Minute); err != nil {
		t.Fatalf("SetTimeout(5m): %v", err)
	}
	shorter := s.get(t, ctx, m.ID).EndAt
	if d := longer.Sub(shorter); d < 14*time.Minute || d > 15*time.Minute+5*time.Second {
		t.Errorf("SetTimeout(5m) after SetTimeout(20m) moved EndAt back by %v, want about 15m", d)
	}

	// Resume on a running machine only ever extends.
	r, err := e.Resume(ctx, m.ID, time.Minute)
	if err != nil {
		t.Fatalf("Resume(running, 1m): %v", err)
	}
	if r.State != engine.Running {
		t.Errorf("Resume(running) state = %s, want running", r.State)
	}
	if endAt := s.get(t, ctx, m.ID).EndAt; endAt.Before(shorter.Add(-2 * time.Second)) {
		t.Errorf("Resume(running, 1m) moved EndAt from %v back to %v, want no change", shorter, endAt)
	}
	if _, err := e.Resume(ctx, m.ID, 15*time.Minute); err != nil {
		t.Fatalf("Resume(running, 15m): %v", err)
	}
	if endAt := s.get(t, ctx, m.ID).EndAt; endAt.Before(shorter.Add(9 * time.Minute)) {
		t.Errorf("Resume(running, 15m) moved EndAt from %v to %v, want about 10m later", shorter, endAt)
	}

	if err := e.Pause(ctx, m.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused := s.get(t, ctx, m.ID)
	if paused.State != engine.Paused || !hasAll(paused.Metadata, labels) || paused.MemoryMB != m.MemoryMB {
		t.Errorf("Get after Pause = %+v, want paused with %v and %d MiB", paused, labels, m.MemoryMB)
	}
	s.wantListed(t, ctx, labels, []engine.State{engine.Paused}, m.ID)
	s.wantListed(t, ctx, labels, []engine.State{engine.Running})
	s.wantListed(t, ctx, labels, []engine.State{engine.Running, engine.Paused}, m.ID)

	notRunning := map[string]func() error{
		"Pause":      func() error { return e.Pause(ctx, m.ID) },
		"SetTimeout": func() error { return e.SetTimeout(ctx, m.ID, time.Minute) },
		"Metrics":    func() error { _, err := e.Metrics(ctx, m.ID); return err },
		"Exec":       func() error { _, err := e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "true"}); return err },
	}
	for _, name := range slices.Sorted(maps.Keys(notRunning)) {
		if err := notRunning[name](); !errors.Is(err, engine.ErrNotRunning) {
			t.Errorf("%s(paused) = %v, want ErrNotRunning", name, err)
		}
	}
	if st := s.get(t, ctx, m.ID).State; st != engine.Paused {
		t.Errorf("state after calls on a paused machine = %s, want still paused", st)
	}

	r, err = e.Resume(ctx, m.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("Resume(paused): %v", err)
	}
	if r.State != engine.Running {
		t.Errorf("Resume(paused) state = %s, want running", r.State)
	}
	if st := s.get(t, ctx, m.ID).State; st != engine.Running {
		t.Errorf("Get after Resume state = %s, want running", st)
	}
	if out := s.mustExec(t, ctx, m.ID, "cat marker"); out != "persisted\n" {
		t.Errorf("marker after pause and resume = %q, want %q", out, "persisted\n")
	}

	if err := e.Kill(ctx, m.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := e.Get(ctx, m.ID); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get after Kill = %v, want ErrNotFound", err)
	}
	if err := e.Kill(ctx, m.ID); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("second Kill = %v, want ErrNotFound", err)
	}
	s.wantListed(t, ctx, labels, nil)
}

func (s *suite) exec(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	m := s.create(t, ctx, "exec", 10*time.Minute)

	res, err := e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 3 || string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" {
		t.Errorf("Exec = {exit %d, stdout %q, stderr %q}, want {exit 3, %q, %q}", res.ExitCode, res.Stdout, res.Stderr, "out\n", "err\n")
	}

	res, err = e.Exec(ctx, m.ID, engine.ExecRequest{
		Cmd:  "sh",
		Args: []string{"-c", `printf '%s' "$HIB_CONTRACT_VAR"`},
		Env:  map[string]string{"HIB_CONTRACT_VAR": "two words"},
	})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "two words" {
		t.Errorf("Exec with Env = %+v, %v; want stdout %q", res, err, "two words")
	}

	res, err = e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", "pwd"}, Dir: "/tmp"})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "/tmp\n" {
		t.Errorf("Exec with Dir /tmp = %+v, %v; want stdout %q", res, err, "/tmp\n")
	}

	flood := fmt.Sprintf("head -c %d /dev/zero", engine.MaxExecOutput+1)
	if res, err := e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", flood}}); err == nil {
		t.Errorf("Exec of more than MaxExecOutput bytes = exit %d with %d bytes, want an error", res.ExitCode, len(res.Stdout))
	}

	if out := s.mustExec(t, ctx, m.ID, `test "$(pwd)" = "$HOME" && echo home`); out != "home\n" {
		t.Errorf("default working directory: got %q, want the home directory", out)
	}

	if s.cfg.Users {
		res, err := e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "id", Args: []string{"-un"}, User: "root"})
		if err != nil || res.ExitCode != 0 || string(res.Stdout) != "root\n" {
			t.Errorf("Exec as root = %+v, %v; want stdout %q", res, err, "root\n")
		}
		if out := s.mustExec(t, ctx, m.ID, "id -un"); out == "root\n" {
			t.Errorf("default user is root, want an unprivileged user")
		}
	}

	deadline := time.Now().Add(s.cfg.Wait)
	for {
		mt, err := e.Metrics(ctx, m.ID)
		if err == nil {
			if mt.CPUCount < 1 || mt.MemTotal <= 0 || mt.At.IsZero() {
				t.Errorf("Metrics = %+v, want CPUCount >= 1, MemTotal > 0 and a timestamp", mt)
			}
			break
		}
		if !errors.Is(err, engine.ErrNoMetrics) {
			t.Fatalf("Metrics: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("Metrics: still no sample after %v", s.cfg.Wait)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Metrics: %v", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (s *suite) killPaused(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	labels := s.labels("kill-paused")
	m := s.create(t, ctx, "kill-paused", 10*time.Minute)

	if err := e.Pause(ctx, m.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := e.Kill(ctx, m.ID); err != nil {
		t.Fatalf("Kill(paused): %v", err)
	}
	if _, err := e.Get(ctx, m.ID); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get after Kill(paused) = %v, want ErrNotFound", err)
	}
	s.wantListed(t, ctx, labels, nil)
}

func (s *suite) get(t *testing.T, ctx context.Context, id string) engine.Machine {
	t.Helper()
	m, err := s.cfg.Engine.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return m
}

// mustExec runs script with sh -c and returns its stdout. It fails the test unless the script
// exits 0.
func (s *suite) mustExec(t *testing.T, ctx context.Context, id, script string) string {
	t.Helper()
	res, err := s.cfg.Engine.Exec(ctx, id, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatalf("Exec(%q): %v", script, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("Exec(%q) exited %d: %s", script, res.ExitCode, res.Stderr)
	}
	return string(res.Stdout)
}

// wantListed checks that listing machines with labels in states returns exactly ids.
func (s *suite) wantListed(t *testing.T, ctx context.Context, labels map[string]string, states []engine.State, ids ...string) {
	t.Helper()
	ms, err := s.cfg.Engine.List(ctx, engine.ListOptions{States: states, Metadata: labels})
	if err != nil {
		t.Fatalf("List(%v): %v", states, err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
		if len(states) > 0 && !slices.Contains(states, m.State) {
			t.Errorf("List(%v) returned %s in state %s", states, m.ID, m.State)
		}
		if m.MemoryMB <= 0 {
			t.Errorf("List(%v) returned %s with %d MiB of memory", states, m.ID, m.MemoryMB)
		}
	}
	slices.Sort(got)
	want := slices.Sorted(slices.Values(ids))
	if !slices.Equal(got, want) {
		t.Errorf("List(%v) = %v, want %v", states, got, want)
	}
}

// RandomID returns a random ID in E2B's format: 20 characters of [a-z0-9].
func RandomID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 20)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func hasAll(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}
