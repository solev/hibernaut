package machines_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

// activity is an activity clock in memory that counts its touches.
type activity struct {
	mu      sync.Mutex
	at      map[string]time.Time
	touched map[string]int
}

func newActivity() *activity {
	return &activity{at: map[string]time.Time{}, touched: map[string]int{}}
}

func (a *activity) Active(id string) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.at[id]
}

func (a *activity) Touch(id string, t time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.touched[id]++
	if t.After(a.at[id]) {
		a.at[id] = t
	}
}

func (a *activity) touches(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.touched[id]
}

// hooked is the fake engine with Resume and Pause replaced where the test says so.
type hooked struct {
	*fake.Engine
	resume func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error)
	pause  func(ctx context.Context, id string) error
}

func (e hooked) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	if e.resume != nil {
		return e.resume(ctx, id, timeout)
	}
	return e.Engine.Resume(ctx, id, timeout)
}

func (e hooked) Pause(ctx context.Context, id string) error {
	if e.pause != nil {
		return e.pause(ctx, id)
	}
	return e.Engine.Pause(ctx, id)
}

// pausedApp makes a paused machine that pauses after idle, at the bubble's time.
func pausedApp(t *testing.T, v *env, idle time.Duration) string {
	t.Helper()
	app := v.createIdle(t, "app", idle)
	if err := v.m.Pause(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	return app.ID
}

func TestWakeHoldsTheMachineToItsLease(t *testing.T) {
	for _, slow := range []time.Duration{0, 40 * time.Second} {
		synctest.Test(t, func(t *testing.T) {
			v := newEnvAt(t, time.Now)
			id := pausedApp(t, v, 15*time.Minute)
			act := newActivity()
			m := imageManager(v, func(c *machines.Config) {
				c.Activity = act
				// The engine starts the clock after placement, so a slow resume ends past a
				// relative clamp.
				c.Engine = hooked{Engine: v.e, resume: func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
					time.Sleep(slow)
					return v.e.Resume(ctx, id, timeout)
				}}
			})
			lease := time.Now().Add(time.Minute)
			em, err := m.Wake(t.Context(), id, lease)
			if err != nil {
				t.Fatalf("Wake after %v = %v", slow, err)
			}
			got := v.get(t, id)
			if got.State != engine.Running || !got.EndAt.Equal(lease) || !em.EndAt.Equal(lease) {
				t.Errorf("after a %v resume: %s until %v (returned %v), want running until the lease %v", slow, got.State, got.EndAt, em.EndAt, lease)
			}
			if act.touches(id) != 0 {
				t.Error("a link wake fed the activity clock")
			}
		})
	}
}

func TestWakeChecksRoom(t *testing.T) {
	v := newEnv(t)
	id := pausedApp(t, v, time.Hour)
	full := func() (room.Host, error) {
		h, err := roomy()
		h.FreePages = 0
		return h, err
	}
	m := imageManager(v, func(c *machines.Config) { c.ReadRoom = full })
	if _, err := m.Wake(t.Context(), id, time.Now().Add(time.Minute)); !errors.Is(err, room.ErrFull) {
		t.Errorf("Wake on a full host = %v, want room.ErrFull", err)
	}
	if got := v.get(t, id); got.State != engine.Paused {
		t.Errorf("state = %s, want it left paused", got.State)
	}
	if _, err := v.m.Wake(t.Context(), id, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("Wake with room = %v", err)
	}
	// A running machine is returned as it is, whatever the room and however short the lease.
	before := v.get(t, id)
	em, err := m.Wake(t.Context(), id, time.Now().Add(time.Second))
	if err != nil || em.State != engine.Running || !v.get(t, id).EndAt.Equal(before.EndAt) {
		t.Errorf("Wake of a running machine = %+v, %v, want it untouched", em, err)
	}
}

func TestWakeSlowerThanTheLeaseIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnvAt(t, time.Now)
		id := pausedApp(t, v, time.Hour)
		m := imageManager(v, func(c *machines.Config) {
			c.Engine = hooked{Engine: v.e, resume: func(ctx context.Context, _ string, _ time.Duration) (engine.Machine, error) {
				<-ctx.Done()
				return engine.Machine{}, ctx.Err()
			}}
		})
		start := time.Now()
		_, err := m.Wake(t.Context(), id, start.Add(time.Minute))
		if !errors.Is(err, links.ErrLeaseExpired) {
			t.Errorf("Wake = %v, want ErrLeaseExpired", err)
		}
		if waited := time.Since(start); waited != time.Minute {
			t.Errorf("Wake gave up after %v, want the lease's minute", waited)
		}
		if got := v.get(t, id); got.State != engine.Paused {
			t.Errorf("state = %s, want paused", got.State)
		}
	})
}

func TestWakePastTheLeasePausesAgainBehindTheFence(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			v := newEnvAt(t, time.Now)
			id := pausedApp(t, v, time.Hour)
			act := newActivity()
			var fenced, pausedFenced, released int
			m := imageManager(v, func(c *machines.Config) {
				c.Activity = act
				c.Fence = func(_ context.Context, fid, why string) (func(), error) {
					if fid != id || why == "" {
						t.Errorf("Fence(%q, %q)", fid, why)
					}
					fenced++
					return func() { released++ }, nil
				}
				// The engine finishes the resume after the caller stopped waiting for it.
				c.Engine = hooked{
					Engine: v.e,
					resume: func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
						if _, err := v.e.Resume(context.Background(), id, timeout); err != nil {
							return engine.Machine{}, err
						}
						<-ctx.Done()
						return engine.Machine{}, ctx.Err()
					},
					pause: func(ctx context.Context, id string) error {
						if fenced == 1 && released == 0 {
							pausedFenced++
						}
						return v.e.Pause(ctx, id)
					},
				}
			})
			lease := time.Now().Add(time.Minute)
			if fresh {
				act.Touch(id, lease.Add(-time.Second))
			}
			if _, err := m.Wake(t.Context(), id, lease); !errors.Is(err, links.ErrLeaseExpired) {
				t.Errorf("Wake = %v, want ErrLeaseExpired", err)
			}
			got := v.get(t, id)
			switch {
			case fresh && (got.State != engine.Running || fenced != 0):
				t.Errorf("a machine with its own activity: %s, fenced %d times, want it left running", got.State, fenced)
			case !fresh && (got.State != engine.Paused || pausedFenced != 1 || released != 1):
				t.Errorf("state %s, paused behind the fence %d times, released %d, want paused once behind it", got.State, pausedFenced, released)
			}
		})
	}
}

func TestWakeRefuses(t *testing.T) {
	v := newEnv(t)
	id := pausedApp(t, v, time.Hour)
	if _, err := v.m.Wake(t.Context(), id, time.Now().Add(-time.Second)); !errors.Is(err, links.ErrLeaseExpired) {
		t.Errorf("Wake past the lease = %v, want ErrLeaseExpired", err)
	}
	if _, err := v.m.Wake(t.Context(), "../app", time.Now().Add(time.Minute)); err == nil {
		t.Error("Wake of a bad ID = nil error")
	}
	if _, err := v.m.Wake(t.Context(), "nosuch", time.Now().Add(time.Minute)); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Wake of a missing ID = %v, want engine.ErrNotFound", err)
	}
	if got := v.get(t, id); got.State != engine.Paused {
		t.Errorf("state = %s, want paused", got.State)
	}
}

func TestSessionHeartbeatTicksForAMachineThatNeverPauses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnvAt(t, time.Now)
		app := v.createIdle(t, "app", 0)
		act := newActivity()
		m := imageManager(v, func(c *machines.Config) { c.Activity = act })
		c, err := m.Dial(t.Context(), "app")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		endAt := v.get(t, app.ID).EndAt
		start := time.Now()
		if act.touches(app.ID) != 1 || !act.Active(app.ID).Equal(start) {
			t.Fatalf("session start: %d touches, active %v", act.touches(app.ID), act.Active(app.ID))
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if n := act.touches(app.ID); n != 3 || !act.Active(app.ID).Equal(start.Add(time.Minute)) {
			t.Errorf("after a minute: %d touches, active %v, want 3 up to now", n, act.Active(app.ID))
		}
		if got := v.get(t, app.ID).EndAt; !got.Equal(endAt) {
			t.Errorf("EndAt moved from %v to %v: something renewed a machine that never pauses", endAt, got)
		}
		c.Close()
		closed := act.touches(app.ID)
		time.Sleep(time.Hour)
		synctest.Wait()
		if n := act.touches(app.ID); n != closed {
			t.Errorf("%d touches after Close", n-closed)
		}
	})
}

func TestCreateAndForkRecordActivity(t *testing.T) {
	v := newEnv(t, fake.WithExec(newCopyGuest().exec))
	act := newActivity()
	m := imageManager(v, func(c *machines.Config) { c.Activity = act })
	web, err := m.Create(t.Context(), "web", "dev", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if act.touches(web.ID) != 1 {
		t.Errorf("Create touched %d times, want once", act.touches(web.ID))
	}
	made, err := m.Fork(t.Context(), "web", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range made {
		if act.touches(copy.ID) != 1 {
			t.Errorf("copy %s touched %d times, want once", copy.Name, act.touches(copy.ID))
		}
	}
}
