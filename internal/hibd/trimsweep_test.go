package hibd

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

type trimSweepLog struct {
	syncBuffer
	problem string
	noticed chan struct{}
}

func (l *trimSweepLog) Write(p []byte) (int, error) {
	n, err := l.syncBuffer.Write(p)
	if strings.Contains(string(p), l.problem) {
		select {
		case l.noticed <- struct{}{}:
		default:
		}
	}
	return n, err
}

func fakeClockDaemon(t *testing.T, a *App, now time.Time, problem string) (*atomic.Value, chan time.Time, *trimSweepLog) {
	t.Helper()
	clock := &atomic.Value{}
	clock.Store(now)
	a.Now = func() time.Time { return clock.Load().(time.Time) }
	ticks := make(chan time.Time)
	a.DaemonTicks = ticks
	log := &trimSweepLog{problem: problem, noticed: make(chan struct{}, 1)}
	a.Stderr = log
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"daemon"}) }()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("daemon exited %d: %s", code, log.String())
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon didn't stop")
		}
	})
	t.Cleanup(stop)
	return clock, ticks, log
}

func awaitTrimProblem(t *testing.T, log *trimSweepLog) {
	t.Helper()
	select {
	case <-log.noticed:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon didn't report %s: %s", log.problem, log.String())
	}
}

func driveTrimRetry(t *testing.T, clock *atomic.Value, ticks chan time.Time, calls <-chan time.Time, at time.Time) {
	t.Helper()
	early := at.Add(-time.Second)
	clock.Store(early)
	select {
	case ticks <- early:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon didn't accept the early tick")
	}
	clock.Store(at)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-calls:
			if !got.Equal(at) {
				t.Fatalf("retry at %v, want %v", got, at)
			}
			return
		case ticks <- at:
		case <-timer.C:
			t.Fatal("daemon didn't retry")
		}
	}
}

func TestDaemonRetriesCleanupLockAfterAMinute(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	release, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), "cleanup.lock", "cleanup")
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	cleaned := make(chan time.Time, 4)
	a.Clean = func(_ context.Context, _ engine.Engine, at time.Time, _ bool, _ func(string, ...any)) error {
		cleaned <- at
		return nil
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	clock, ticks, log := fakeClockDaemon(t, a, now, "another cleanup is running")
	awaitTrimProblem(t, log)
	release()
	release = nil
	driveTrimRetry(t, clock, ticks, cleaned, now.Add(time.Minute))
	if strings.Count(log.String(), "another cleanup is running") != 1 {
		t.Fatal(log.String())
	}
}

func TestDaemonTrimCutoffsRetryMinuteAfterCompletionAndFinish(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	cleaned := make(chan time.Time, 4)
	var passes atomic.Int32
	finished := make(chan int32, 4)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	clock := &atomic.Value{}
	clock.Store(now)
	a.Clean = func(_ context.Context, _ engine.Engine, at time.Time, _ bool, logf func(string, ...any)) error {
		cleaned <- at
		if passes.Add(1) <= 2 {
			clock.Store(at.Add(trimTimeout))
			logf("trimmed 3 MiB in 1 files")
			return cleanup.ErrTrimTimeout
		}
		logf("nothing to trim")
		return nil
	}
	a.Now = func() time.Time {
		at := clock.Load().(time.Time)
		if n := passes.Load(); n > 0 {
			finished <- n
		}
		return at
	}
	ticks := make(chan time.Time)
	a.DaemonTicks = ticks
	log := &trimSweepLog{problem: "snapshot trimming ran out of time", noticed: make(chan struct{}, 1)}
	a.Stderr = log
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"daemon"}) }()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("daemon exited %d", code)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon didn't stop")
		}
	})
	t.Cleanup(stop)
	if at := awaitSweep(t, cleaned); !at.Equal(now) {
		t.Fatal(at)
	}
	awaitTrimProblem(t, log)
	<-finished
	driveTrimRetry(t, clock, ticks, cleaned, now.Add(3*time.Minute))
	<-finished
	driveTrimRetry(t, clock, ticks, cleaned, now.Add(6*time.Minute))
	<-finished
	stop()
	if passes.Load() != 3 || strings.Count(log.String(), "snapshot trimming ran out of time") != 1 || strings.Count(log.String(), "trimmed 3 MiB in 1 files") != 2 || !strings.Contains(log.String(), "nothing to trim") {
		t.Fatal(log.String())
	}
}
