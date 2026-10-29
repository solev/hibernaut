package machines

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
)

type pauseAfterDialRead struct {
	engine.Engine
	once sync.Once
}

func (e *pauseAfterDialRead) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	ms, err := e.Engine.List(ctx, opts)
	if err == nil && len(opts.States) == 0 {
		e.once.Do(func() { err = e.Engine.Pause(ctx, "target") })
	}
	return ms, err
}

type missingTrafficToken struct{ engine.Engine }

func (e *missingTrafficToken) DialRunningWebSocket(context.Context, string, int) (net.Conn, error) {
	return nil, engine.ErrNoTrafficToken
}

func TestDialMissingTrafficTokenRequiresAdmission(t *testing.T) {
	m, e := sharedStateManager(t)
	e.Add(engine.Machine{ID: "target", State: engine.Running, MemoryMB: 2048,
		EndAt:    time.Now().Add(2 * DefaultIdle),
		Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
	if err := m.checkState(); err != nil {
		t.Fatal(err)
	}
	if err := m.writePin("target", mustReadPlacementKey(t, m)); err != nil {
		t.Fatal(err)
	}
	d := &refusingDial{Engine: e}
	m.e = &missingTrafficToken{Engine: d}
	unlock, err := m.lockRoom(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(unlock)
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	c, err := m.Dial(ctx, "target")
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) || d.dials != 0 {
		t.Fatalf("missing-token dial = %v, resumable dials=%d, want blocked admission", err, d.dials)
	}
	release()
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	c, err = m.Dial(ctx, "target")
	if err != nil || c == nil {
		t.Fatalf("missing-token admitted dial = %v, want a connection", err)
	}
	c.Close()
}

func TestDialAdmitsAPauseAfterItsRead(t *testing.T) {
	for _, missingPin := range []bool{false, true} {
		for _, full := range []bool{false, true} {
			name := "pinned"
			if missingPin {
				name = "repin"
			}
			if full {
				name += " full"
			} else {
				name += " room"
			}
			t.Run(name, func(t *testing.T) {
				m, e := sharedStateManager(t)
				e.Add(engine.Machine{ID: "target", State: engine.Running, MemoryMB: 2048,
					EndAt:    time.Now().Add(2 * DefaultIdle),
					Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
				if err := m.checkState(); err != nil {
					t.Fatal(err)
				}
				if !missingPin {
					if err := m.writePin("target", mustReadPlacementKey(t, m)); err != nil {
						t.Fatal(err)
					}
				}
				reads := 0
				m.readRoom = func() (room.Host, error) {
					assertPlacementLocked(t, m.dir)
					reads++
					if full {
						return placementHost(0), nil
					}
					return placementHost(8192), nil
				}
				d := &refusingDial{Engine: e}
				m.e = &pauseAfterDialRead{Engine: d}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				c, err := m.Dial(ctx, "target")
				if c != nil {
					c.Close()
				}
				wantState := engine.Running
				if full {
					wantState = engine.Paused
					if !errors.Is(err, room.ErrFull) {
						t.Errorf("Dial = %v, want the room error without resuming", err)
					}
					if d.dials != 0 {
						t.Errorf("full host received %d resumable dials, want none", d.dials)
					}
				} else if err != nil || c == nil {
					t.Errorf("Dial = %v, want a connection after admission", err)
				}
				if reads != 1 {
					t.Errorf("Dial took %d admitted room readings, want one", reads)
				}
				em, err := e.Get(ctx, "target")
				if err != nil || em.State != wantState {
					t.Errorf("Dial left target %s, %v, want %s", em.State, err, wantState)
				}
			})
		}
	}
}

type roomHolderAfterRepin struct {
	engine.Engine
	m       *Manager
	release func()
}

func TestDialRemovedAfterItsRead(t *testing.T) {
	for _, missingPin := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned", true: "repin"}[missingPin], func(t *testing.T) {
			m, e := sharedStateManager(t)
			e.Add(engine.Machine{ID: "target", State: engine.Running, MemoryMB: 2048,
				EndAt:    time.Now().Add(2 * DefaultIdle),
				Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
			if err := m.checkState(); err != nil {
				t.Fatal(err)
			}
			if !missingPin {
				if err := m.writePin("target", mustReadPlacementKey(t, m)); err != nil {
					t.Fatal(err)
				}
			}
			var once sync.Once
			m.e = &placementEngine{Engine: e, list: func(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
				ms, err := e.List(ctx, opts)
				if err == nil && len(opts.States) == 0 {
					once.Do(func() { err = e.Kill(ctx, "target") })
				}
				return ms, err
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			c, err := m.Dial(ctx, "target")
			if c != nil {
				c.Close()
			}
			if !errors.Is(err, engine.ErrNotFound) {
				t.Fatalf("removed machine Dial = %v, want ErrNotFound", err)
			}
		})
	}
}

func (e *roomHolderAfterRepin) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	result, err := e.Engine.Exec(ctx, id, req)
	if err == nil && e.release == nil {
		e.release, err = e.m.lockRoom(ctx)
	}
	return result, err
}

func TestDialPausedRepinAdmitsOnce(t *testing.T) {
	m, e := sharedStateManager(t)
	e.Add(engine.Machine{ID: "target", State: engine.Paused, MemoryMB: 2048,
		Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
	checks := 0
	admitted := &placementEngine{Engine: &refusingDial{Engine: e},
		list: func(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
			if len(opts.States) > 0 {
				checks++
			}

			return e.List(ctx, opts)
		}}
	wrapped := &roomHolderAfterRepin{Engine: admitted, m: m}
	m.e = wrapped
	defer func() {
		if wrapped.release != nil {
			wrapped.release()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	c, err := m.Dial(ctx, "target")
	if c != nil {
		c.Close()
	}
	if err != nil || c == nil {
		t.Fatalf("paused repin waited for a second admission: %v", err)
	}
	if checks != 1 {
		t.Fatalf("paused repin made %d room checks, want one", checks)
	}
	em, err := e.Get(t.Context(), "target")
	if err != nil || em.State != engine.Running {
		t.Fatalf("Dial left target %s, %v, want running", em.State, err)
	}
}
