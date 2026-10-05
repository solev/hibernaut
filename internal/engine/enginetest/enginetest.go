// Package enginetest is the contract every engine.Engine meets.
package enginetest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
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
	t.Run("InvalidBuild", s.invalidBuild)
	t.Run("Lifecycle", s.lifecycle)
	t.Run("Exec", s.exec)
	t.Run("KillPaused", s.killPaused)
	t.Run("SaveTemplate", s.saveTemplate)
	t.Run("SavePaused", s.savePaused)
	t.Run("DeleteTemplateInUse", s.deleteTemplateInUse)
	t.Run("MissingTemplate", s.missingTemplate)
	t.Run("SaveAfterMiss", s.saveAfterMiss)
}

func (s *suite) invalidBuild(t *testing.T) {
	for _, spec := range []engine.TemplateSpec{{}, {Name: "hib-invalid"}, {FromImage: "ubuntu:24.04"}} {
		called := false
		id, err := s.cfg.Engine.BuildTemplate(s.ctx(t), spec, func(engine.LogEntry) { called = true })
		if err == nil || id != "" || called {
			t.Fatalf("invalid build = %q, %v, log callback %v", id, err, called)
		}
	}
}

type suite struct {
	cfg   Config
	runID string
}

const (
	runKey    = "hib-contract-run"
	caseKey   = "hib-contract-case"
	originKey = "hib-contract-origin"
)

func (s *suite) ctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), s.cfg.Timeout)
	t.Cleanup(cancel)
	return ctx
}

func (s *suite) labels(name string) map[string]string {
	return map[string]string{runKey: s.runID, caseKey: name}
}

func (s *suite) metadata(name string) map[string]string {
	md := maps.Clone(s.cfg.Metadata)
	if md == nil {
		md = map[string]string{}
	}
	maps.Copy(md, s.labels(name))
	return md
}

func (s *suite) create(t *testing.T, ctx context.Context, name string, timeout time.Duration) engine.Machine {
	t.Helper()
	return s.createFrom(t, ctx, s.cfg.Template, s.metadata(name), timeout)
}

func (s *suite) createFrom(t *testing.T, ctx context.Context, template string, md map[string]string, timeout time.Duration) engine.Machine {
	t.Helper()
	m, err := s.cfg.Engine.Create(ctx, engine.CreateOptions{Template: template, Timeout: timeout, Metadata: md})
	if err != nil {
		t.Fatalf("Create from %s: %v", template, err)
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
	m, err := s.cfg.Engine.Create(ctx, engine.CreateOptions{Template: "hib-contract-none-" + RandomID(), Metadata: s.metadata("UnknownTemplate")})
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
	started := s.startedAt(t, ctx, labels, m.ID)

	s.mustExec(t, ctx, m.ID, "echo persisted > marker")

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

	if st := s.startedAt(t, ctx, labels, m.ID); !st.Equal(started) {
		t.Errorf("StartedAt after SetTimeout and Resume(running) = %v, want it still %v", st, started)
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
	if st := s.startedAt(t, ctx, labels, m.ID); !st.After(started) {
		t.Errorf("StartedAt after Pause and Resume = %v, want later than %v", st, started)
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

func (s *suite) saveTemplate(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	md := s.metadata("save-template")
	md[originKey] = s.runID
	origin := s.createFrom(t, ctx, s.cfg.Template, md, 10*time.Minute)
	s.mustExec(t, ctx, origin.ID, "echo saved > marker")

	alias := s.alias("save")
	s.deleteTemplateLater(t, alias)
	info, err := e.SaveTemplate(ctx, origin.ID, alias)
	if err != nil {
		t.Fatalf("SaveTemplate: %v", err)
	}
	if info.ID == "" || info.Alias != alias || info.CPUs < 1 || info.MemoryMB != origin.MemoryMB || info.Created.IsZero() {
		t.Errorf("SaveTemplate = %+v, want an ID, alias %s, CPUs, the origin's %d MiB and a time", info, alias, origin.MemoryMB)
	}
	if got, err := e.Template(ctx, alias); err != nil || !sameTemplate(got, info) {
		t.Errorf("Template(%s) = %+v, %v; want %+v", alias, got, err, info)
	}
	if st := s.get(t, ctx, origin.ID).State; st != engine.Running {
		t.Errorf("origin after SaveTemplate is %s, want still running", st)
	}
	s.wantSaved(t, ctx, info, origin)

	want := s.metadata("save-template-copy")
	cp := s.createFrom(t, ctx, alias, want, 10*time.Minute)
	if cp.State != engine.Running || cp.Template != info.ID || cp.MemoryMB != origin.MemoryMB {
		t.Errorf("Create from %s = %+v, want running from template %s with the origin's %d MiB", alias, cp, info.ID, origin.MemoryMB)
	}
	if !maps.Equal(cp.Metadata, want) {
		t.Errorf("copy's metadata = %v, want only its own %v", cp.Metadata, want)
	}
	if out := s.mustExec(t, ctx, cp.ID, "cat marker"); out != "saved\n" {
		t.Errorf("copy's marker = %q, want the origin's %q", out, "saved\n")
	}
	s.mustExec(t, ctx, cp.ID, "echo copy > marker")
	if out := s.mustExec(t, ctx, origin.ID, "cat marker"); out != "saved\n" {
		t.Errorf("origin's marker after the copy changed its own = %q, want %q", out, "saved\n")
	}
}

func (s *suite) wantSaved(t *testing.T, ctx context.Context, info engine.TemplateInfo, origin engine.Machine) {
	t.Helper()
	e := s.cfg.Engine
	saved, err := e.SavedTemplates(ctx)
	if err != nil {
		t.Fatalf("SavedTemplates: %v", err)
	}
	if i := slices.IndexFunc(saved, func(x engine.TemplateInfo) bool { return x.ID == info.ID }); i < 0 || !sameTemplate(saved[i], info) {
		t.Errorf("SavedTemplates has %d templates, want them to include %+v", len(saved), info)
	}
	if !slices.IsSortedFunc(saved, func(a, b engine.TemplateInfo) int { return strings.Compare(a.Alias, b.Alias) }) {
		t.Error("SavedTemplates isn't sorted by alias")
	}

	built, err := e.Template(ctx, s.cfg.Template)
	if err != nil {
		t.Fatalf("Template(%s): %v", s.cfg.Template, err)
	}
	if built.ID != origin.Template || built.MemoryMB != origin.MemoryMB {
		t.Errorf("Template(%s) = %+v, want the template %s that made a machine of %d MiB", s.cfg.Template, built, origin.Template, origin.MemoryMB)
	}
	if slices.ContainsFunc(saved, func(x engine.TemplateInfo) bool { return x.ID == built.ID }) {
		t.Errorf("SavedTemplates lists built template %s", s.cfg.Template)
	}
}

func (s *suite) savePaused(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	m := s.create(t, ctx, "save-paused", 10*time.Minute)
	if err := e.Pause(ctx, m.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	alias := s.alias("save-paused")
	s.deleteTemplateLater(t, alias)
	if _, err := e.SaveTemplate(ctx, m.ID, alias); !errors.Is(err, engine.ErrNotRunning) {
		t.Errorf("SaveTemplate(paused) = %v, want ErrNotRunning", err)
	}
	if _, err := e.Template(ctx, alias); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("Template after a refused save = %v, want ErrNoTemplate", err)
	}
	if st := s.get(t, ctx, m.ID).State; st != engine.Paused {
		t.Errorf("state after a refused save = %s, want still paused", st)
	}
}

func (s *suite) deleteTemplateInUse(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	origin := s.create(t, ctx, "delete-template", 10*time.Minute)

	alias := s.alias("delete")
	s.deleteTemplateLater(t, alias)
	info, err := e.SaveTemplate(ctx, origin.ID, alias)
	if err != nil {
		t.Fatalf("SaveTemplate: %v", err)
	}
	cp := s.createFrom(t, ctx, alias, s.metadata("delete-template-copy"), 10*time.Minute)

	if err := e.DeleteTemplate(ctx, info.ID); !errors.Is(err, engine.ErrTemplateInUse) {
		t.Errorf("DeleteTemplate with a running copy = %v, want ErrTemplateInUse", err)
	}
	if err := e.Pause(ctx, cp.ID); err != nil {
		t.Fatalf("Pause(copy): %v", err)
	}
	if err := e.DeleteTemplate(ctx, info.ID); !errors.Is(err, engine.ErrTemplateInUse) {
		t.Errorf("DeleteTemplate with a paused copy = %v, want ErrTemplateInUse", err)
	}
	if err := e.Kill(ctx, cp.ID); err != nil {
		t.Fatalf("Kill(copy): %v", err)
	}
	if err := e.DeleteTemplate(ctx, info.ID); err != nil {
		t.Fatalf("DeleteTemplate with no copies left: %v", err)
	}

	if _, err := e.Template(ctx, alias); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("Template after DeleteTemplate = %v, want ErrNoTemplate", err)
	}
	if err := e.DeleteTemplate(ctx, info.ID); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("second DeleteTemplate = %v, want ErrNoTemplate", err)
	}
	saved, err := e.SavedTemplates(ctx)
	if err != nil {
		t.Fatalf("SavedTemplates: %v", err)
	}
	if slices.ContainsFunc(saved, func(x engine.TemplateInfo) bool { return x.ID == info.ID }) {
		t.Errorf("SavedTemplates lists deleted template %s", alias)
	}
}

func (s *suite) missingTemplate(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	if info, err := e.Template(ctx, "hib-contract-none-"+RandomID()); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("Template(unknown) = %+v, %v; want ErrNoTemplate", info, err)
	}
	if err := e.DeleteTemplate(ctx, RandomID()); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("DeleteTemplate(unknown) = %v, want ErrNoTemplate", err)
	}
}

func (s *suite) saveAfterMiss(t *testing.T) {
	t.Parallel()
	ctx := s.ctx(t)
	e := s.cfg.Engine
	origin := s.create(t, ctx, "save-after-miss", 10*time.Minute)

	alias := s.alias("save-after-miss")
	s.deleteTemplateLater(t, alias)
	if info, err := e.Template(ctx, alias); !errors.Is(err, engine.ErrNoTemplate) {
		t.Fatalf("Template before the save = %+v, %v; want ErrNoTemplate", info, err)
	}
	info, err := e.SaveTemplate(ctx, origin.ID, alias)
	if err != nil {
		t.Fatalf("SaveTemplate after a missed lookup: %v", err)
	}
	if got, err := e.Template(ctx, alias); err != nil || !sameTemplate(got, info) {
		t.Errorf("Template right after the save = %+v, %v; want %+v", got, err, info)
	}
}

func (s *suite) alias(name string) string {
	return "hib-contract-" + s.runID + "-" + name
}

// deleteTemplateLater deletes the template called alias when t ends. If t failed, a save may have
// failed too, and left alias hidden from Template for a minute or more, so it looks for longer.
func (s *suite) deleteTemplateLater(t *testing.T, alias string) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		info, err := s.cfg.Engine.Template(ctx, alias)
		if errors.Is(err, engine.ErrNoTemplate) && t.Failed() {
			info, err = s.hiddenTemplate(ctx, alias)
		}
		if err == nil {
			err = s.cfg.Engine.DeleteTemplate(ctx, info.ID)
		}
		if err != nil && !errors.Is(err, engine.ErrNoTemplate) {
			t.Errorf("cleanup: delete template %s: %v", alias, err)
		}
	})
}

func (s *suite) hiddenTemplate(ctx context.Context, alias string) (engine.TemplateInfo, error) {
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return engine.TemplateInfo{}, ctx.Err()
		case <-time.After(5 * time.Second):
		}
		info, err := s.cfg.Engine.Template(ctx, alias)
		if !errors.Is(err, engine.ErrNoTemplate) || time.Now().After(deadline) {
			return info, err
		}
	}
}

func (s *suite) get(t *testing.T, ctx context.Context, id string) engine.Machine {
	t.Helper()
	m, err := s.cfg.Engine.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return m
}

func (s *suite) startedAt(t *testing.T, ctx context.Context, labels map[string]string, id string) time.Time {
	t.Helper()
	started := s.get(t, ctx, id).StartedAt
	ms, err := s.cfg.Engine.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}, Metadata: labels})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	i := slices.IndexFunc(ms, func(m engine.Machine) bool { return m.ID == id })
	switch {
	case i < 0:
		t.Errorf("List(running) doesn't have %s", id)
	case !ms[i].StartedAt.Equal(started):
		t.Errorf("List says %s started at %v, Get says %v", id, ms[i].StartedAt, started)
	}
	return started
}

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

func sameTemplate(a, b engine.TemplateInfo) bool {
	return a.ID == b.ID && a.Alias == b.Alias && a.CPUs == b.CPUs && a.MemoryMB == b.MemoryMB && a.Created.Equal(b.Created)
}

func hasAll(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}
