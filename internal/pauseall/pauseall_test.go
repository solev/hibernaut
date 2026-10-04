package pauseall_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/pauseall"
)

var fastRetry = pauseall.Options{RetryDelay: time.Millisecond}

func add(e *fake.Engine, id string, st engine.State) {
	e.Add(engine.Machine{ID: id, Template: "base", State: st, StartedAt: time.Now()})
}

func states(t *testing.T, e engine.Engine) map[string]engine.State {
	t.Helper()
	ms, err := e.List(context.Background(), engine.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]engine.State{}
	for _, m := range ms {
		out[m.ID] = m.State
	}
	return out
}

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) hook(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, id)
	return nil
}

func TestPausesRunningAndSkipsPaused(t *testing.T) {
	var rec recorder
	e := fake.New(fake.WithPauseHook(rec.hook))
	add(e, "run1", engine.Running)
	add(e, "run2", engine.Running)
	add(e, "run3", engine.Running)
	add(e, "zzz1", engine.Paused)
	add(e, "zzz2", engine.Paused)

	paused, err := pauseall.Run(t.Context(), e, fastRetry)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	slices.Sort(paused)
	if want := []string{"run1", "run2", "run3"}; !slices.Equal(paused, want) {
		t.Errorf("paused = %v, want %v", paused, want)
	}
	for id, st := range states(t, e) {
		if st != engine.Paused {
			t.Errorf("%s is %s, want paused", id, st)
		}
	}
	slices.Sort(rec.calls)
	if want := []string{"run1", "run2", "run3"}; !slices.Equal(rec.calls, want) {
		t.Errorf("Pause called for %v, want only the running machines %v", rec.calls, want)
	}
}

func TestNothingRunning(t *testing.T) {
	e := fake.New()
	add(e, "zzz1", engine.Paused)
	paused, err := pauseall.Run(t.Context(), e, fastRetry)
	if err != nil || len(paused) != 0 {
		t.Errorf("Run = %v, %v; want nothing paused and no error", paused, err)
	}
}

func TestPausesMachinesThatStartMeanwhile(t *testing.T) {
	var e *fake.Engine
	var once sync.Once
	e = fake.New(fake.WithPauseHook(func(ctx context.Context, id string) error {

		once.Do(func() { add(e, "late", engine.Running) })
		return nil
	}))
	add(e, "early", engine.Running)

	paused, err := pauseall.Run(t.Context(), e, fastRetry)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	slices.Sort(paused)
	if want := []string{"early", "late"}; !slices.Equal(paused, want) {
		t.Errorf("paused = %v, want %v", paused, want)
	}
}

func TestRetriesFailedPause(t *testing.T) {
	var mu sync.Mutex
	failures := map[string]int{"flaky": 2}
	e := fake.New(fake.WithPauseHook(func(_ context.Context, id string) error {
		mu.Lock()
		defer mu.Unlock()
		if failures[id] > 0 {
			failures[id]--
			return errors.New("node busy, please retry")
		}
		return nil
	}))
	add(e, "flaky", engine.Running)
	add(e, "fine", engine.Running)

	paused, err := pauseall.Run(t.Context(), e, fastRetry)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	slices.Sort(paused)
	if want := []string{"fine", "flaky"}; !slices.Equal(paused, want) {
		t.Errorf("paused = %v, want %v", paused, want)
	}
}

func TestSkipsMachinesPausedOrKilledMeanwhile(t *testing.T) {
	e := fake.New()
	add(e, "gone", engine.Running)
	add(e, "done", engine.Running)

	racy := &raceEngine{Engine: e, beforePause: func(id string) {
		switch id {
		case "gone":
			_ = e.Kill(context.Background(), id)
		case "done":
			_ = e.Pause(context.Background(), id)
		}
	}}

	paused, err := pauseall.Run(t.Context(), racy, fastRetry)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(paused) != 0 {
		t.Errorf("paused = %v, want none (others got there first)", paused)
	}
}

func TestGivesUpWhenContextEnds(t *testing.T) {
	stuckStarted := make(chan struct{})
	e := fake.New(fake.WithPauseHook(func(ctx context.Context, id string) error {
		if id != "stuck" {
			return nil
		}
		close(stuckStarted)
		<-ctx.Done()
		return ctx.Err()
	}))
	add(e, "stuck", engine.Running)
	add(e, "quick", engine.Running)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		paused []string
		err    error
	}
	res := make(chan result)
	go func() {
		p, err := pauseall.Run(ctx, e, fastRetry)
		res <- result{p, err}
	}()
	<-stuckStarted
	for deadline := time.Now().Add(5 * time.Second); states(t, e)["quick"] != engine.Paused; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("quick wasn't paused while stuck was pausing")
		}
	}
	cancel()
	r := <-res

	if r.err == nil {
		t.Fatal("Run returned no error with a machine still running")
	}
	if !errors.Is(r.err, context.Canceled) || !strings.Contains(r.err.Error(), "stuck") {
		t.Errorf("error = %v, want context.Canceled naming the stuck machine", r.err)
	}
	if got := states(t, e); got["stuck"] != engine.Running || got["quick"] != engine.Paused {
		t.Errorf("states = %v, want stuck running and quick paused", got)
	}
}

func TestRetriesWhenListFails(t *testing.T) {
	e := fake.New()
	add(e, "m1", engine.Running)
	flaky := &raceEngine{Engine: e, listFailures: 2}

	paused, err := pauseall.Run(t.Context(), flaky, fastRetry)
	if err != nil || !slices.Equal(paused, []string{"m1"}) {
		t.Errorf("Run = %v, %v; want [m1] after the list recovers", paused, err)
	}
}

func TestGivesUpWhenListNeverWorks(t *testing.T) {
	e := fake.New()
	failing := &raceEngine{Engine: e, listFailures: -1}
	ctx, cancel := context.WithCancel(t.Context())
	failing.onList = func(n int) {
		if n == 3 {
			cancel()
		}
	}

	_, err := pauseall.Run(ctx, failing, fastRetry)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "list running machines") {
		t.Errorf("Run = %v, want context.Canceled with the last list error", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	release := make(chan struct{})
	e := fake.New(fake.WithPauseHook(func(ctx context.Context, id string) error {
		mu.Lock()
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	}))
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		add(e, id, engine.Running)
	}

	done := make(chan error)
	go func() {
		_, err := pauseall.Run(t.Context(), e, pauseall.Options{Concurrency: 2, RetryDelay: time.Millisecond})
		done <- err
	}()

	for range 5 {
		release <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if maxInFlight > 2 {
		t.Errorf("max concurrent pauses = %d, want at most 2", maxInFlight)
	}
}

type raceEngine struct {
	engine.Engine
	beforePause  func(id string)
	listFailures int
	onList       func(n int)

	mu    sync.Mutex
	lists int
}

func (r *raceEngine) Pause(ctx context.Context, id string) error {
	if r.beforePause != nil {
		r.beforePause(id)
	}
	return r.Engine.Pause(ctx, id)
}

func (r *raceEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	r.mu.Lock()
	r.lists++
	n := r.lists
	fail := r.listFailures != 0
	if r.listFailures > 0 {
		r.listFailures--
	}
	r.mu.Unlock()
	if r.onList != nil {
		r.onList(n)
	}
	if fail {
		return nil, errors.New("connection refused")
	}
	return r.Engine.List(ctx, opts)
}
