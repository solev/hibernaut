package fake_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/enginetest"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestContract(t *testing.T) {
	enginetest.Run(t, enginetest.Config{
		Engine:   fake.New(fake.WithLocalExec(t.TempDir())),
		Template: "base",
		Metadata: map[string]string{"hib-test": "unit"},
		Wait:     time.Second,
	})
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
