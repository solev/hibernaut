package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func newApp(t *testing.T, e engine.Engine) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	return &app{
		stdout:    &out,
		stderr:    &errOut,
		getenv:    func(string) string { return "" },
		newEngine: func() (engine.Engine, error) { return e, nil },
	}, &out, &errOut
}

func TestLs(t *testing.T) {
	e := fake.New()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e.Add(engine.Machine{ID: "run1", Template: "base", State: engine.Running, StartedAt: now, EndAt: now.Add(time.Hour)})
	e.Add(engine.Machine{ID: "zzz1", Template: "base", State: engine.Paused, StartedAt: now.Add(-time.Hour)})

	a, out, _ := newApp(t, e)
	if code := a.run(t.Context(), []string{"ls", "-state", "running", "-q"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != "run1\n" {
		t.Errorf("ls -state running -q = %q, want %q", out.String(), "run1\n")
	}

	out.Reset()
	if code := a.run(t.Context(), []string{"ls"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") || !strings.HasPrefix(lines[1], "run1") ||
		!strings.Contains(lines[1], "2026-09-30T13:00:00Z") || !strings.HasPrefix(lines[2], "zzz1") {
		t.Errorf("ls =\n%s", out.String())
	}

	if code := a.run(t.Context(), []string{"ls", "-state", "sleeping"}); code != 2 {
		t.Errorf("ls -state sleeping exit = %d, want 2", code)
	}
}

func TestPauseAll(t *testing.T) {
	e := fake.New()
	e.Add(engine.Machine{ID: "run1", State: engine.Running})
	a, _, errOut := newApp(t, e)
	if code := a.run(t.Context(), []string{"pause-all"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if m, _ := e.Get(t.Context(), "run1"); m.State != engine.Paused {
		t.Errorf("run1 is %s, want paused", m.State)
	}
	if !strings.Contains(errOut.String(), "paused 1 machine(s)") {
		t.Errorf("log = %q", errOut)
	}
}

func TestPauseAllTimesOut(t *testing.T) {
	e := fake.New(fake.WithPauseHook(func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	e.Add(engine.Machine{ID: "stuck", State: engine.Running})
	a, _, errOut := newApp(t, e)
	if code := a.run(t.Context(), []string{"pause-all", "-timeout", "10ms"}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "stuck") {
		t.Errorf("log = %q, want it to name the stuck machine", errOut)
	}
}

func TestEngineErrorExitsOne(t *testing.T) {
	a, _, errOut := newApp(t, nil)
	a.newEngine = func() (engine.Engine, error) { return nil, errors.New("E2B_API_KEY is not set") }
	if code := a.run(t.Context(), []string{"ls"}); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "E2B_API_KEY") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestUnknownCommand(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	if code := a.run(t.Context(), []string{"frobnicate"}); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if code := a.run(t.Context(), nil); code != 2 {
		t.Errorf("no command: exit %d, want 2", code)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hibd.env")
	content := "# E2B\nE2B_API_URL=http://10.0.0.1:3000\n\nE2B_API_KEY=\"from-file\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HIBD_CONFIG": path, "E2B_API_KEY": "from-env"}
	cfg, err := loadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIURL != "http://10.0.0.1:3000" || cfg.SandboxURL != "http://127.0.0.1:3002" || cfg.APIKey != "from-env" {
		t.Errorf("config = %+v, want the file's API URL, the default sandbox URL and the env's key", cfg)
	}

	delete(env, "E2B_API_KEY")
	if cfg, err := loadConfig(func(k string) string { return env[k] }); err != nil || cfg.APIKey != "from-file" {
		t.Errorf("config = %+v, %v; want the file's key without quotes", cfg, err)
	}

	env["HIBD_CONFIG"] = filepath.Join(dir, "missing.env")
	if _, err := loadConfig(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "E2B_API_KEY") {
		t.Errorf("no key anywhere: err = %v, want one naming E2B_API_KEY", err)
	}

	if err := os.WriteFile(path, []byte("not a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["HIBD_CONFIG"] = path
	if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
		t.Error("malformed file: no error")
	}
}
