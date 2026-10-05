package hibd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

type cleanupEngine struct {
	engine.Engine
	list func(context.Context, engine.ListOptions) ([]engine.Machine, error)
}

func (e cleanupEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	return e.list(ctx, opts)
}

func TestCleanupReadWaitsOnceAndCancels(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			a, _, _ := newApp(t, fake.New())
			m := machines.New(machines.Config{StateDir: a.Getenv("HIBD_STATE_DIR")})
			release, err := m.TryCleanupRead(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if release != nil {
					release()
				}
			}()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			waits := 0
			a.WaitCleanup = func(context.Context) error {
				waits++
				if waits == 2 {
					release()
					release = nil
					if cancelled {
						cancel()
					}
				}
				return ctx.Err()
			}
			var logs []string
			unlock, err := a.lockCleanupRead(ctx, m, true, func(f string, args ...any) {
				logs = append(logs, fmt.Sprintf(f, args...))
			})
			if cancelled {
				if !errors.Is(err, context.Canceled) || unlock != nil {
					t.Fatalf("cancelled wait = %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				unlock()
			}
			if len(logs) != 1 || !strings.Contains(logs[0], "waiting for an image save or fork") {
				t.Fatalf("wait logs = %v", logs)
			}
			unlock, err = m.TryCleanupRead(t.Context())
			if err != nil {
				t.Fatalf("lock left held: %v", err)
			}
			unlock()
		})
	}
}

func TestCleanupReadErrorsReleaseBothLocks(t *testing.T) {
	for _, failure := range []string{"list", "query", "directory", "cancelled wait"} {
		t.Run(failure, func(t *testing.T) {
			a, _, _ := newApp(t, fake.New())
			a.Clean = nil
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			cfg.e2bStorage, cfg.trimScope = t.TempDir(), "off"
			if failure == "directory" {
				cfg.e2bStorage += "/missing"
			}
			want := errors.New("read failed")
			e := cleanupEngine{fake.New(), func(context.Context, engine.ListOptions) ([]engine.Machine, error) {
				if failure == "list" {
					return nil, want
				}
				return nil, nil
			}}
			a.Query = func(context.Context, string) ([][]string, error) {
				if failure == "query" {
					return nil, want
				}
				return nil, nil
			}
			m := machines.New(machines.Config{StateDir: cfg.stateDir})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "cancelled wait" {
				held, err := m.TryCleanupRead(ctx)
				if err != nil {
					t.Fatal(err)
				}
				a.WaitCleanup = func(context.Context) error { cancel(); return ctx.Err() }
				defer held()
				err = a.clean(ctx, ctx, cfg, e, time.Now(), false, false, func(string, ...any) {})
				held()
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("wait = %v", err)
				}
			} else if err := a.clean(ctx, ctx, cfg, e, time.Now(), false, false, func(string, ...any) {}); err == nil {
				t.Fatal("cleanup ignored read failure")
			}
			unlock, err := m.TryCleanupRead(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			unlock()
			unlock, ok, err := tryLock(cfg.stateDir, "cleanup.lock", "cleanup")
			if err != nil || !ok {
				t.Fatalf("cleanup lock left held: %v", err)
			}
			unlock()
		})
	}
}

func TestCleanupReadSweepContentionRetriesMinute(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	a.Clean = nil
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	m := machines.New(machines.Config{StateDir: cfg.stateDir})
	held, err := m.TryCleanupRead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	a.Query = func(context.Context, string) ([][]string, error) {
		t.Fatal("SQL ran during a save")
		return nil, nil
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var logs []string
	logf := func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
	e := fake.New()
	err = a.clean(t.Context(), t.Context(), cfg, e, now, false, true, logf)
	if !errors.Is(err, machines.ErrBusy) || len(logs) != 0 {
		t.Fatalf("contention = %v, logs %v", err, logs)
	}
	s := sweeper{engine: e, clean: func(ctx, listing context.Context, at time.Time) error {
		return a.clean(ctx, listing, cfg, e, at, false, true, logf)
	}, logf: logf}
	s.look(t.Context(), now)
	if !s.next.Equal(now.Add(time.Minute)) || strings.Contains(strings.Join(logs, "\n"), "not trimming") {
		t.Fatalf("retry = %v, logs %v", s.next, logs)
	}
}
