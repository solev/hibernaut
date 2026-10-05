package fake_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/enginetest"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []fake.Option
	}{
		{"WithoutAliasCache", nil},
		{"WithAliasCache", []fake.Option{fake.WithAliasCache()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]fake.Option{fake.WithLocalExec(t.TempDir()), fake.WithTemplates("base")}, tc.opts...)
			enginetest.Run(t, enginetest.Config{
				Engine:   fake.New(opts...),
				Template: "base",
				Metadata: map[string]string{"hib-test": "unit"},
				Wait:     time.Second,
			})
		})
	}
}

func TestKillRemovesHome(t *testing.T) {
	root := t.TempDir()
	e := fake.New(fake.WithLocalExec(root))
	ctx := t.Context()
	m, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", "pwd"}})
	if err != nil {
		t.Fatal(err)
	}
	home := strings.TrimSpace(string(res.Stdout))
	if !strings.HasPrefix(home, root) && !strings.Contains(home, m.ID) {
		t.Fatalf("home = %q, want a directory for %s under %s", home, m.ID, root)
	}
	if err := e.Kill(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "true"}); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Exec after Kill = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(home); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(home) after Kill = %v, want not exist", err)
	}
}

func TestPauseHookFailureKeepsMachineRunning(t *testing.T) {
	boom := errors.New("boom")
	e := fake.New(fake.WithPauseHook(func(context.Context, string) error { return boom }))
	ctx := t.Context()
	m, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Pause(ctx, m.ID); !errors.Is(err, boom) {
		t.Fatalf("Pause = %v, want the hook's error", err)
	}
	if got, _ := e.Get(ctx, m.ID); got.State != engine.Running {
		t.Errorf("state = %s, want running", got.State)
	}
}

func TestBuildHookFailureBuildsNothing(t *testing.T) {
	boom := errors.New("boom")
	var seen []string
	e := fake.New(fake.WithTemplates(), fake.WithBuildHook(func(_ context.Context, spec engine.TemplateSpec) error {
		seen = append(seen, spec.Name)
		if spec.Name == "hib-bad" {
			return boom
		}
		return nil
	}))
	ctx := t.Context()
	for name, want := range map[string]error{"hib-good": nil, "hib-bad": boom} {
		if _, err := e.BuildTemplate(ctx, engine.TemplateSpec{Name: name, FromImage: "ubuntu:24.04"}, nil); !errors.Is(err, want) {
			t.Errorf("BuildTemplate(%s) = %v, want %v", name, err, want)
		}
	}
	if len(seen) != 2 {
		t.Errorf("hook saw %v, want both builds", seen)
	}
	if built := e.Built(); len(built) != 1 || built[0].Name != "hib-good" {
		t.Errorf("Built() = %+v, want only hib-good", built)
	}
	if _, err := e.Template(ctx, "hib-bad"); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("Template(hib-bad) = %v, want ErrNoTemplate", err)
	}
}

func TestExecWithoutRunner(t *testing.T) {
	e := fake.New()
	ctx := t.Context()
	m, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Exec(ctx, m.ID, engine.ExecRequest{Cmd: "true"}); err == nil {
		t.Error("Exec without WithExec or WithLocalExec succeeded, want an error")
	}
}

func TestClock(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e := fake.New(fake.WithClock(func() time.Time { return now }))
	ctx := t.Context()
	m, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.StartedAt.Equal(now) || !m.EndAt.Equal(now.Add(fake.DefaultTimeout)) {
		t.Errorf("Create with no timeout = start %v end %v, want %v and %v", m.StartedAt, m.EndAt, now, now.Add(fake.DefaultTimeout))
	}
}

func TestDialResumesAndCallsDialer(t *testing.T) {
	var dialed []string
	e := fake.New(fake.WithDialer(func(_ context.Context, m engine.Machine, port int) (net.Conn, error) {
		if m.State != engine.Running {
			t.Errorf("dialer got %s in state %s, want running", m.ID, m.State)
		}
		dialed = append(dialed, fmt.Sprintf("%s:%d", m.ID, port))
		client, server := net.Pipe()
		server.Close()
		return client, nil
	}))
	ctx := t.Context()
	m, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Pause(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	c, err := e.DialWebSocket(ctx, m.ID, 8081, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if got, _ := e.Get(ctx, m.ID); got.State != engine.Running {
		t.Errorf("state after dial = %s, want running", got.State)
	}
	if len(dialed) != 1 || dialed[0] != m.ID+":8081" {
		t.Errorf("dialer calls = %v, want one for port 8081", dialed)
	}

	if _, err := fake.New().DialWebSocket(ctx, m.ID, 8081, time.Hour); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("dial on another engine = %v, want ErrNotFound", err)
	}
	noDialer := fake.New()
	m2, _ := noDialer.Create(ctx, engine.CreateOptions{Template: "base"})
	if _, err := noDialer.DialWebSocket(ctx, m2.ID, 8081, time.Hour); err == nil {
		t.Error("dial without a dialer succeeded")
	}
}

func TestWithTemplatesAddsBuiltOnes(t *testing.T) {
	e := fake.New(fake.WithTemplates("base"))
	if _, err := e.Create(t.Context(), engine.CreateOptions{Template: "hib-dev"}); !errors.Is(err, engine.ErrNoTemplate) {
		t.Fatalf("Create before build = %v, want ErrNoTemplate", err)
	}
	if _, err := e.Template(t.Context(), "hib-dev"); !errors.Is(err, engine.ErrNoTemplate) {
		t.Fatalf("Template before build = %v, want ErrNoTemplate", err)
	}
	if _, err := e.BuildTemplate(t.Context(), engine.TemplateSpec{Name: "hib-dev", FromImage: "ubuntu:24.04", CPUs: 4, MemoryMB: 2048}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range []string{"base", "hib-dev", "tmpl-hib-dev"} {
		if _, err := e.Create(t.Context(), engine.CreateOptions{Template: tmpl}); err != nil {
			t.Errorf("Create(%s) = %v", tmpl, err)
		}
	}
	for _, want := range []engine.TemplateInfo{
		{ID: "base", Alias: "base", CPUs: fake.DefaultCPUs, MemoryMB: fake.DefaultMemoryMB},
		{ID: "tmpl-hib-dev", Alias: "hib-dev", CPUs: 4, MemoryMB: 2048},
	} {
		got, err := e.Template(t.Context(), want.Alias)
		got.Created = time.Time{}
		if err != nil || got != want {
			t.Errorf("Template(%s) = %+v, %v, want %+v", want.Alias, got, err, want)
		}
	}
}

func TestLocalExecRefusesOtherUsers(t *testing.T) {
	e := fake.New(fake.WithLocalExec(t.TempDir()))
	m, err := e.Create(t.Context(), engine.CreateOptions{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Exec(t.Context(), m.ID, engine.ExecRequest{Cmd: "true", User: "root"}); err == nil {
		t.Error("local exec as root succeeded, want an error: it can't switch users")
	}
}
