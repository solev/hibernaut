package hibd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
)

func TestDaemonSweepsForkTemplatesWhenCopiesDisappear(t *testing.T) {
	a, e, out, _ := imageApp(t)
	for _, args := range [][]string{{"new", "web", "-json"}, {"fork", "web", "-json"}} {
		if code, _, err := runJSON(t, a, out, args...); code != 0 || err != nil {
			t.Fatalf("%v = %d, %v", args, code, err)
		}
	}
	saved, err := e.SavedTemplates(t.Context())
	if err != nil || len(saved) != 1 || !images.IsForkTemplate(saved[0].Alias) {
		t.Fatalf("templates = %+v, %v", saved, err)
	}
	alias := saved[0].Alias
	ticks := make(chan time.Time)
	a.DaemonTicks = ticks
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	cleaned := make(chan time.Time, 2)
	a.Clean = func(_ context.Context, _ engine.Engine, at time.Time, _ bool, _ func(string, ...any)) error {
		cleaned <- at
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"daemon"}) }()
	t.Cleanup(func() {
		cancel()
		if code := <-done; code != 0 {
			t.Errorf("daemon exit %d", code)
		}
	})
	awaitSweep(t, cleaned)
	if _, err := e.Template(t.Context(), alias); err != nil {
		t.Fatalf("swept an in-use fork template: %v", err)
	}
	if code, _, err := runJSON(t, a, out, "rm", "web-1", "-json"); code != 0 || err != nil {
		t.Fatalf("remove copy = %d, %v", code, err)
	}
	ticks <- now.Add(time.Second)
	if at := awaitSweep(t, cleaned); !at.Equal(now.Add(time.Second)) {
		t.Fatalf("cleanup at %v, want the removal tick", at)
	}
	if _, err := e.Template(t.Context(), alias); !errors.Is(err, engine.ErrNoTemplate) {
		t.Fatalf("unused fork template = %v, want removed", err)
	}
}

func awaitSweep(t *testing.T, cleaned <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-cleaned:
		return at
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not finish its sweep")
		return time.Time{}
	}
}

type sweepLog struct {
	*syncBuffer
	busy chan struct{}
}

func (l *sweepLog) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "state lock is held") {
		select {
		case l.busy <- struct{}{}:
		default:
		}
	}
	return l.syncBuffer.Write(p)
}

func TestDaemonRetriesBusyForkSweepAfterAMinute(t *testing.T) {
	a, _, out, _ := imageApp(t)
	if code, _, err := runJSON(t, a, out, "new", "web", "-json"); code != 0 || err != nil {
		t.Fatalf("new = %d, %v", code, err)
	}
	inSnapshot, release := make(chan struct{}), make(chan struct{})
	a.Query = func(_ context.Context, sql string) ([][]string, error) {
		if !strings.HasPrefix(sql, "SELECT status_group") {
			close(inSnapshot)
			<-release
			return [][]string{{leakBuildA}}, nil
		}
		return [][]string{{"ready"}}, nil
	}
	forkDone := make(chan int, 1)
	go func() { forkDone <- a.Run(t.Context(), []string{"fork", "web", "-json"}) }()
	<-inSnapshot
	logs := &sweepLog{syncBuffer: &syncBuffer{}, busy: make(chan struct{}, 1)}
	daemon := *a
	daemon.Stderr = logs
	ticks := make(chan time.Time)
	daemon.DaemonTicks = ticks
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	daemon.Now = func() time.Time { return now }
	cleaned := make(chan time.Time, 2)
	daemon.Clean = func(_ context.Context, _ engine.Engine, at time.Time, _ bool, _ func(string, ...any)) error {
		cleaned <- at
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- daemon.Run(ctx, []string{"daemon"}) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-logs.busy:
	case <-time.After(5 * time.Second):
		close(release)
		<-forkDone
		t.Fatal("daemon did not report the busy fork")
	}
	close(release)
	if code := <-forkDone; code != 0 {
		t.Fatalf("fork = %d; stdout %s", code, out)
	}
	ticks <- now.Add(59 * time.Second)
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		select {
		case at := <-cleaned:
			if !at.Equal(now.Add(time.Minute)) {
				t.Fatalf("retry at %v, want a minute later", at)
			}
			return
		case ticks <- now.Add(time.Minute):
		case <-wait.Done():
			t.Fatalf("daemon did not retry after a minute; log %s", logs)
		}
	}
}
