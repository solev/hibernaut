// Package pauseall pauses every running machine (spec §5, The host).
// Running machines live in RAM; paused ones are snapshots on disk.
package pauseall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

// Options tune Run. The zero value is ready to use.
type Options struct {
	// Concurrency is how many machines pause at once. Zero means 4.
	Concurrency int
	// RetryDelay is the pause between rounds after a failure. Zero means one second.
	RetryDelay time.Duration
	// Logf receives progress lines. Nil discards them.
	Logf func(format string, args ...any)
}

// Run pauses every running machine, including machines that start or wake meanwhile.
// When ctx is done first, it returns an error naming the machines it couldn't pause.
//
// It returns the IDs it paused, in the order they finished.
func Run(ctx context.Context, e engine.Engine, opts Options) ([]string, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = time.Second
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	var paused, stuck []string
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return paused, giveUp(err, stuck, lastErr)
		}
		ms, err := e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
		if err != nil {
			lastErr = fmt.Errorf("list running machines: %w", err)
			logf("%v", lastErr)
			wait(ctx, opts.RetryDelay)
			continue
		}
		if len(ms) == 0 {
			return paused, nil
		}

		ids := make([]string, len(ms))
		for i, m := range ms {
			ids[i] = m.ID
		}
		logf("pausing %d machine(s): %s", len(ids), strings.Join(ids, ", "))
		done, failed := pauseEach(ctx, e, ids, opts.Concurrency, logf)
		paused = append(paused, done...)
		stuck = failed
		if len(failed) > 0 {
			lastErr = fmt.Errorf("pause %s: failed", strings.Join(failed, ", "))
			wait(ctx, opts.RetryDelay)
		}
	}
}

func pauseEach(ctx context.Context, e engine.Engine, ids []string, concurrency int, logf func(string, ...any)) (done, failed []string) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, id := range ids {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				failed = append(failed, id)
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			start := time.Now()
			err := e.Pause(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				logf("paused %s in %v", id, time.Since(start).Round(time.Millisecond))
				done = append(done, id)
			case errors.Is(err, engine.ErrNotRunning), errors.Is(err, engine.ErrNotFound):
				logf("skipped %s: %v", id, err)
			default:
				logf("pause %s: %v", id, err)
				failed = append(failed, id)
			}
		})
	}
	wg.Wait()
	slices.Sort(failed)
	return done, failed
}

func giveUp(ctxErr error, stuck []string, lastErr error) error {
	if len(stuck) > 0 {
		return fmt.Errorf("gave up with %d machine(s) still running: %s: %w", len(stuck), strings.Join(stuck, ", "), ctxErr)
	}
	if lastErr != nil {
		return fmt.Errorf("gave up before confirming no machine is running: %w (last error: %v)", ctxErr, lastErr)
	}
	return fmt.Errorf("gave up before confirming no machine is running: %w", ctxErr)
}

func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
