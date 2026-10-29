package machines

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
)

// retryCut runs run again while its error wraps engine.ErrConnectionLost, up to 3 tries with
// waits of 1 s then 2 s, all within one initTimeout. It returns run's own error, so a failure
// names the step that failed, and a finished run counts even if ctx ends right after it.
func (m *Manager) retryCut(ctx context.Context, run func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		err := run(ctx)
		if err == nil || !errors.Is(err, engine.ErrConnectionLost) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if attempt == 3 {
			return fmt.Errorf("%w (tried 3 times)", err)
		}
		if werr := m.waitCut(ctx, time.Duration(attempt)*time.Second); werr != nil {
			return fmt.Errorf("%w. Stopped retrying: %w", err, werr)
		}
	}
}

// startFailedFor is how long retryResume keeps trying a machine that failed to start. E2B's API
// learns that an orchestrator restarted at its next node sync, every 20 s, and until then it
// fails every start there.
const startFailedFor = 30 * time.Second

// retryResume runs resume, which resumes the paused machine called name, again every 2 s while
// the engine turns it down for now, until ctx ends, and while the machine fails to start, for
// startFailedFor after the first time. It stops once the host has no room left for the
// machine's memoryMB.
func (m *Manager) retryResume(ctx context.Context, name string, memoryMB int, resume func(context.Context) error) error {
	var failedAt time.Time
	for attempt := 1; ; attempt++ {
		unlock, err := m.checkRoom(ctx, name, memoryMB, false)
		if err != nil {
			return err
		}
		err = resume(ctx)
		unlock()
		switch {
		case err == nil:
			return nil
		case errors.Is(err, engine.ErrStartFailed):
			if failedAt.IsZero() {
				failedAt = m.now()
			} else if m.now().Sub(failedAt) >= startFailedFor {
				return fmt.Errorf("%w (tried %d times)", err, attempt)
			}
		case !errors.Is(err, engine.ErrNotPlaced):
			if attempt == 1 {
				return err
			}
			return fmt.Errorf("%w (tried %d times)", err, attempt)
		}
		if rerr := m.roomError(ctx, name, memoryMB, false); errors.Is(rerr, room.ErrFull) {
			return rerr
		}
		if werr := m.waitCut(ctx, 2*time.Second); werr != nil {
			return fmt.Errorf("%w (tried %d times). Stopped retrying: %w", err, attempt, werr)
		}
	}
}

// waitCut waits for delay. It returns ctx's error if ctx ends first, or has ended by then.
func waitCut(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	return ctx.Err()
}
