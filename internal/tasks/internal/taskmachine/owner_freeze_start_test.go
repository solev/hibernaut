package taskmachine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
)

func TestPauseFreezesAfterFailedSliceStart(t *testing.T) {
	for _, tt := range []struct {
		name     string
		startErr error
	}{
		{"exit", nil},
		{"exec", errors.New("slice start exec lost")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := t.Context()
			f.owner.inner = failedSliceStart{Engine: f.sim, startErr: tt.startErr}
			if err := f.owner.Pause(ctx, f.tm); err != nil {
				t.Fatal(err)
			}
			r, err := f.store.Run(ctx, f.run)
			if err != nil {
				t.Fatal(err)
			}
			if !f.sim.Frozen(f.tm) || !r.Freeze.Frozen || r.Freeze.Detail != "" {
				t.Errorf("slice frozen %v, row %+v: want frozen with no detail", f.sim.Frozen(f.tm), r.Freeze)
			}
			if m, err := f.sim.Get(ctx, f.tm); err != nil || m.State != engine.Paused {
				t.Errorf("pause left machine %s, %v: want paused", m.State, err)
			}
			if m, err := f.owner.Resume(ctx, f.tm, time.Minute); err != nil || m.State != engine.Running {
				t.Errorf("frozen slice resume = %s, %v: want running", m.State, err)
			}
			if !f.sim.Frozen(f.tm) {
				t.Error("resume did not keep the slice frozen")
			}
		})
	}
}

func TestPauseWithFailedSliceStartIsHeld(t *testing.T) {
	for _, tt := range []struct {
		name      string
		freezeErr error
		detail    string
	}{
		{"empty", nil, "freeze held nothing: hib-task.slice has no cgroup"},
		{"exec", errors.New("freeze exec lost"), "freeze failed: freeze exec lost"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := t.Context()
			f.sim.Reboot(f.tm)
			f.owner.inner = failedSliceStart{Engine: f.sim, freezeErr: tt.freezeErr}
			if err := f.owner.Pause(ctx, f.tm); err != nil {
				t.Fatal(err)
			}
			r, err := f.store.Run(ctx, f.run)
			if err != nil {
				t.Fatal(err)
			}
			if f.sim.Frozen(f.tm) || r.Freeze.Frozen {
				t.Errorf("failed freeze recorded as frozen: %+v", r.Freeze)
			}
			for _, want := range []string{"prepare freeze failed: systemctl exited 1: slice start failed", tt.detail} {
				if !strings.Contains(r.Freeze.Detail, want) {
					t.Errorf("freeze detail %q does not name %q", r.Freeze.Detail, want)
				}
			}
			if m, err := f.sim.Get(ctx, f.tm); err != nil || m.State != engine.Paused {
				t.Errorf("failed freeze left machine %s, %v: want paused", m.State, err)
			}
			if _, err := f.owner.Resume(ctx, f.tm, time.Minute); !errors.Is(err, task.ErrHeld) {
				t.Errorf("failed freeze allowed wake: %v", err)
			}
		})
	}
}

func TestPauseFreezeUsesOneTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		ctx := t.Context()
		f.owner.inner = hangingFreeze{f.sim}
		start := time.Now()
		if err := f.owner.Pause(ctx, f.tm); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != taskguest.ExecTimeout {
			t.Errorf("freeze took %s: want one timeout %s", elapsed, taskguest.ExecTimeout)
		}
		r, err := f.store.Run(ctx, f.run)
		if err != nil {
			t.Fatal(err)
		}
		if r.Freeze.Frozen || r.Freeze.Detail == "" {
			t.Errorf("timed-out freeze recorded as frozen or without detail: %+v", r.Freeze)
		}
		if m, err := f.sim.Get(ctx, f.tm); err != nil || m.State != engine.Paused {
			t.Errorf("timed-out freeze left machine %s, %v: want paused", m.State, err)
		}
	})
}

type failedSliceStart struct {
	engine.Engine
	startErr  error
	freezeErr error
}

func (e failedSliceStart) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	argv := append([]string{req.Cmd}, req.Args...)
	if slices.Equal(argv, []string{"systemctl", "start", taskguest.Slice}) {
		if e.startErr != nil {
			return engine.ExecResult{}, e.startErr
		}
		return engine.ExecResult{ExitCode: 1, Stderr: []byte("slice start failed")}, nil
	}
	if slices.Equal(argv, taskguest.Freeze()) && e.freezeErr != nil {
		return engine.ExecResult{}, e.freezeErr
	}
	return e.Engine.Exec(ctx, id, req)
}

type hangingFreeze struct{ engine.Engine }

func (e hangingFreeze) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	argv := append([]string{req.Cmd}, req.Args...)
	if slices.Equal(argv, []string{"systemctl", "start", taskguest.Slice}) ||
		slices.Equal(argv, taskguest.Freeze()) {
		<-ctx.Done()
		return engine.ExecResult{}, ctx.Err()
	}
	return e.Engine.Exec(ctx, id, req)
}
