package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
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
	for _, failure := range []string{"list", "query", "directory", "cancelled wait", "unsettled record", "unreadable record"} {
		t.Run(failure, func(t *testing.T) {
			a, _, _ := newApp(t, fake.New())
			a.Clean = nil
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			cfg.e2bStorage, cfg.trimScope = t.TempDir(), "off"
			if strings.HasSuffix(failure, "record") {
				saves := filepath.Join(cfg.stateDir, "saves")
				if err := os.Mkdir(saves, 0o700); err != nil {
					t.Fatal(err)
				}
				data := []byte("{")
				if failure == "unsettled record" {
					data, err = json.Marshal(cleanup.SaveRecord{Machine: "web", Name: "web", Started: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)})
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(saves, "web-"+strings.Repeat("a", 32)), data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(cfg.e2bStorage, "44444444-4444-4444-8444-444444444444"), 0o700); err != nil {
					t.Fatal(err)
				}
				a.OpenTrim = func(string) (cleanup.TrimStorage, error) {
					t.Fatal("trimming opened storage despite save record")
					return nil, nil
				}
				cfg.trimScope = "e2e"
			}
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
			if strings.HasSuffix(failure, "record") {
				if _, err := os.Stat(filepath.Join(cfg.e2bStorage, "44444444-4444-4444-8444-444444444444")); err != nil {
					t.Fatal("unsettled record allowed deletion")
				}
				var out, errOut bytes.Buffer
				restarted := App{
					Stdout: &out, Stderr: &errOut, Query: a.Query, OpenTrim: a.OpenTrim,
					NewEngine: func() (engine.Engine, error) { return e, nil },
					Getenv: func(k string) string {
						if k == "HIBD_E2B_STORAGE" {
							return cfg.e2bStorage
						}
						return a.Getenv(k)
					},
				}
				if code := restarted.Run(t.Context(), []string{"cleanup"}); code != 1 || !strings.Contains(errOut.String(), "save") {
					t.Fatalf("process restart ignored record: exit %d, %s", code, errOut.String())
				}
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

func TestCleanupUnsettledSaveRetriesWithoutRepeatedProblem(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var logs []string
	s := sweeper{
		engine: fake.New(),
		clean: func(context.Context, context.Context, time.Time) error {
			return fmt.Errorf("%w: web began saving at %s", machines.ErrSaveUnsettled, now)
		},
		logf: func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) },
	}
	s.look(t.Context(), now)
	s.look(t.Context(), now.Add(time.Minute))
	if !s.next.Equal(now.Add(2*time.Minute)) || len(logs) != 1 {
		t.Fatalf("next %v, problems %v", s.next, logs)
	}
}
