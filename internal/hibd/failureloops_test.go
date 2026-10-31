package hibd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

type failingWatcher struct {
	engine.Engine
	phase *atomic.Int32
}

func (e failingWatcher) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	if n := e.phase.Load(); n >= 0 {
		return nil, fmt.Errorf("watcher port %d", n)
	}
	return e.Engine.List(ctx, opts)
}

func TestMainLoopLogsFailuresForADay(t *testing.T) {
	for _, source := range []string{"watcher", "awake"} {
		t.Run(source, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var phase atomic.Int32
				e := failingWatcher{Engine: fake.New(), phase: &phase}
				a, _, _ := newApp(t, e)
				clock := &testClock{now: time.Now()}
				a.Now = clock.Now
				ticks := make(chan time.Time)
				a.DaemonTicks, a.TaskTicks = ticks, make(chan time.Time)
				a.codexEvery = 48 * time.Hour
				marker := a.Getenv("HIBD_QUIESCING")
				blocker := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(blocker, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				getenv := a.Getenv
				a.Getenv = func(k string) string {
					if k == "HIBD_QUIESCING" && source == "awake" && phase.Load() >= 0 {
						return filepath.Join(blocker, fmt.Sprintf("awake-%d", phase.Load()))
					}
					return getenv(k)
				}
				logs, stop := startDaemon(t, a)
				synctest.Wait()
				for n := int32(1); n < 5760; n++ {
					phase.Store(n)
					clock.add(15 * time.Second)
					sendTaskTick(t, ticks, clock.Now())
					synctest.Wait()
				}
				var failures []string
				for line := range strings.SplitSeq(logs.String(), "\n") {
					if strings.HasPrefix(line, "listing machines: watcher port ") ||
						strings.HasPrefix(line, "stat "+blocker) {
						failures = append(failures, line)
					}
				}
				if len(failures) != 144 {
					t.Fatalf("%s failed with new text every 15s and logged %d lines, want 144",
						source, len(failures))
				}
				if !strings.Contains(failures[143], "5720") ||
					!strings.HasSuffix(failures[143], " (39 more since the last line)") {
					t.Fatalf("last failure = %q, want latest text and 39 suppressed failures", failures[143])
				}
				phase.Store(-1)
				if err := os.WriteFile(marker, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					sendTaskTick(t, ticks, clock.Now())
					synctest.Wait()
				}
				if n := strings.Count(logs.String(), "the host is going to sleep: leaving machines alone\n"); n != 1 {
					t.Fatalf("quiescing logged %d state lines, want 1", n)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					sendTaskTick(t, ticks, clock.Now())
					synctest.Wait()
				}
				if n := strings.Count(logs.String(), "looking at machines again (39 more since the last line)\n"); n != 1 {
					t.Fatalf("watcher recovery logged %d counted lines, want 1; log tail %q", n,
						logs.String()[max(0, len(logs.String())-300):])
				}
				stop()
				before := logs.String()
				clock.add(10 * time.Minute)
				synctest.Wait()
				if logs.String() != before {
					t.Fatal("the main loop logged after stopping")
				}
			})
		})
	}
}

func TestSetupDeliveryLogsFailuresForADay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := fake.New()
		a, _, _ := newApp(t, e)
		withSecretConfig(t, a)
		cfg, err := loadConfig(a.Getenv)
		if err != nil {
			t.Fatal(err)
		}
		var logs lineLog
		d := newSetupDelivery(e, cfg, a.codexStore(cfg, nil), logs.logf)
		blocker := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for n := range 5760 {
			d.cfg.githubProfile = filepath.Join(blocker, fmt.Sprintf("profile-%d", n))
			d.look(t.Context())
			time.Sleep(15 * time.Second)
		}
		lines := logs.take()
		if len(lines) != 144 {
			t.Fatalf("setup failed with new text every 15s and logged %d lines, want 144", len(lines))
		}
		if !strings.Contains(lines[143], "profile-5720") ||
			!strings.HasSuffix(lines[143], " (39 more since the last line)") {
			t.Fatalf("last setup failure = %q, want latest text and count", lines[143])
		}
		d.cfg.githubProfile = filepath.Join(t.TempDir(), "missing")
		d.look(t.Context())
		d.look(t.Context())
		want := []string{"loading the machine setup: recovered (39 more since the last line)"}
		if got := logs.take(); !slices.Equal(got, want) {
			t.Fatalf("setup recovery = %q, want %q", got, want)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		d.cfg.githubProfile = filepath.Join(blocker, "after-stop")
		d.look(ctx)
		if got := logs.take(); len(got) != 0 {
			t.Fatalf("setup logged after shutdown: %q", got)
		}
	})
}

func TestCodexRefresherLogsFailuresForADay(t *testing.T) {
	a, _, _, _ := newCodexApp(t, nil, testCodexTokens())
	connectTestCodex(t, a, testCodexTokens())
	p, err := codexStoreFor(a).Load()
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		a.Now = time.Now
		var logs lineLog
		r := &codexRefresher{store: codexStoreFor(a), logf: logs.logf, seen: p}
		save := func() {
			t.Helper()
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(a.Getenv("HIBD_STATE_DIR"), codex.ProfileFile), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		base := time.Now()
		p.Failures = 1
		for range 1440 {
			p.RetryAt = time.Now().Add(time.Minute)
			save()
			r.look(t.Context())
			time.Sleep(time.Minute)
		}
		lines := logs.take()
		if len(lines) != 144 {
			t.Fatalf("codex failed with new text every minute and logged %d lines, want 144", len(lines))
		}
		want := "refreshing failed. Trying again after " +
			base.Add(1431*time.Minute).UTC().Format(time.RFC3339) + " (9 more since the last line)"
		if lines[143] != want {
			t.Fatalf("last codex failure = %q, want %q", lines[143], want)
		}
		p.Health, p.Problem = codex.HealthDead, "run hib connect codex"
		save()
		r.look(t.Context())
		r.look(t.Context())
		p.Problem = "the login needs attention: run hib connect codex"
		save()
		r.look(t.Context())
		wantStates := []string{"the login is dead: run hib connect codex",
			"the login is dead: the login needs attention: run hib connect codex"}
		if got := logs.take(); !slices.Equal(got, wantStates) {
			t.Fatalf("codex state changes = %q, want %q", got, wantStates)
		}
		p.Health, p.Problem, p.Failures = codex.HealthOK, "", 0
		save()
		r.look(t.Context())
		r.look(t.Context())
		if got, want := logs.take(), []string{"the login is ok (9 more since the last line)"}; !slices.Equal(got, want) {
			t.Fatalf("codex recovery = %q, want %q", got, want)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		p.Failures = 1
		save()
		time.Sleep(10 * time.Minute)
		r.look(ctx)
		if got := logs.take(); len(got) != 0 {
			t.Fatalf("codex logged after shutdown: %q", got)
		}
	})
}

func TestSetupDeliveryKeepsKeysIndependentAndForgetsAbsentMachines(t *testing.T) {
	e := fake.New()
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	var logs lineLog
	d := newSetupDelivery(e, cfg, a.codexStore(cfg, nil), logs.logf)
	d.report(t.Context(), "updating gone", errors.New("first"))
	d.report(t.Context(), "listing machines", errors.New("second"))
	d.look(t.Context())
	d.report(t.Context(), "updating gone", errors.New("third"))
	if got, want := logs.take(), []string{"updating gone: first", "listing machines: second",
		"listing machines: recovered", "updating gone: third"}; !slices.Equal(got, want) {
		t.Fatalf("independent keys and absent-machine replacement logs = %q, want %q", got, want)
	}
}

func TestMainLoopLogsRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var phase atomic.Int32
		a, _, _ := newApp(t, failingWatcher{Engine: fake.New(), phase: &phase})
		ticks := make(chan time.Time)
		a.DaemonTicks, a.TaskTicks = ticks, make(chan time.Time)
		a.codexEvery = 48 * time.Hour
		logs, stop := startDaemon(t, a)
		synctest.Wait()
		phase.Store(1)
		time.Sleep(15 * time.Second)
		sendTaskTick(t, ticks, time.Now())
		synctest.Wait()
		phase.Store(-1)
		sendTaskTick(t, ticks, time.Now())
		synctest.Wait()
		sendTaskTick(t, ticks, time.Now())
		synctest.Wait()
		stop()
		if n := strings.Count(logs.String(), "looking at machines again (1 more since the last line)\n"); n != 1 {
			t.Fatalf("watcher recovery logged %d counted lines, want 1; log %q", n, logs.String())
		}
	})
}

func TestSetupDeliveryUsesItsClockAndChecksLifeAfterReadingIt(t *testing.T) {
	e := fake.New()
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	var logs lineLog
	d := newSetupDelivery(e, cfg, a.codexStore(cfg, nil), logs.logf)
	clock := &testClock{now: time.Time{}}
	d.now = clock.Now
	d.report(t.Context(), "listing machines", errors.New("first"))
	clock.add(5 * time.Second)
	d.report(t.Context(), "listing machines", errors.New("second"))
	clock.add(10*time.Minute - 5*time.Second)
	d.report(t.Context(), "listing machines", errors.New("third"))
	if got, want := logs.take(), []string{"listing machines: first",
		"listing machines: third (1 more since the last line)"}; !slices.Equal(got, want) {
		t.Fatalf("injected clock logs = %q, want %q", got, want)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.now = func() time.Time {
		cancel()
		return clock.Now().Add(10 * time.Minute)
	}
	d.report(ctx, "listing machines", errors.New("after shutdown"))
	if got := logs.take(); len(got) != 0 {
		t.Fatalf("setup logged after its clock ended daemon life: %q", got)
	}
}
