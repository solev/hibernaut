package machines

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

type cutExecEngine struct {
	engine.Engine
	exec func(context.Context) (engine.ExecResult, error)
}

func (e *cutExecEngine) Exec(ctx context.Context, _ string, _ engine.ExecRequest) (engine.ExecResult, error) {
	return e.exec(ctx)
}

func TestCreateCancellationDuringCutBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, e := sharedStateManager(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tries, waits := 0, 0
		m.e = &cutExecEngine{Engine: e, exec: func(context.Context) (engine.ExecResult, error) {
			tries++
			return engine.ExecResult{}, engine.ErrConnectionLost
		}}
		m.waitCut = func(ctx context.Context, delay time.Duration) error {
			waits++
			cancel()
			return waitCut(ctx, delay)
		}
		start := time.Now()
		_, err := m.Create(ctx, "web", "dev", DefaultIdle)
		if !errors.Is(err, context.Canceled) || tries != 1 || waits != 1 {
			t.Errorf("Create error=%v tries=%d waits=%d, want cancellation after one try", err, tries, waits)
		}
		if time.Since(start) != 0 {
			t.Error("cancellation waited for the backoff timer")
		}
	})
}

func TestCreateCutTriesShareOneDeadline(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "exhausted", true: "expired"}[expire], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, e := sharedStateManager(t)
				start := time.Now()
				wantDeadline := start.Add(2 * time.Minute)
				tries := 0
				var waits []time.Duration
				m.e = &cutExecEngine{Engine: e, exec: func(ctx context.Context) (engine.ExecResult, error) {
					tries++
					if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(wantDeadline) {
						t.Errorf("try %d received a different deadline", tries)
					}
					if expire && tries == 3 {
						<-ctx.Done()
						return engine.ExecResult{}, ctx.Err()
					}
					return engine.ExecResult{}, engine.ErrConnectionLost
				}}
				m.waitCut = func(ctx context.Context, delay time.Duration) error {
					waits = append(waits, delay)
					if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(wantDeadline) {
						t.Error("backoff received a different deadline")
					}
					return waitCut(ctx, delay)
				}
				_, err := m.Create(t.Context(), "web", "dev", DefaultIdle)
				wantErr, elapsed := engine.ErrConnectionLost, 3*time.Second
				if expire {
					wantErr, elapsed = context.DeadlineExceeded, 2*time.Minute
				}
				if !errors.Is(err, wantErr) || tries != 3 || !slices.Equal(waits, []time.Duration{time.Second, 2 * time.Second}) {
					t.Errorf("Create error=%v tries=%d waits=%v", err, tries, waits)
				}
				if time.Since(start) != elapsed {
					t.Errorf("elapsed=%v, want %v", time.Since(start), elapsed)
				}
			})
		})
	}
}
