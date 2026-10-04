package machines

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

func (m *Manager) retryCut(ctx context.Context, run func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || !errors.Is(err, engine.ErrConnectionLost) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if attempt == 3 {
			return fmt.Errorf("%w (tried 3 times)", err)
		}
		if err := m.waitCut(ctx, time.Duration(attempt)*time.Second); err != nil {
			return err
		}
	}
}

func waitCut(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
