package machines

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
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
