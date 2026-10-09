package machines

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/links"
)

// Wake resumes machine id for a link and returns it as the engine now reports it. It works by
// sandbox ID, never by name, because a name can be removed and reused while a link waits.
//
// leaseUntil is absolute: the machine is resumed to run no later than it, and its real EndAt is
// read back and shortened when the engine started the clock after placement. A resume still
// pending when the lease passes is cancelled, and a machine that came up after its lease, with
// no activity of its own to keep it up, is paused again behind Config.Fence. Both return
// links.ErrLeaseExpired. A machine that is already running is returned as it is.
func (m *Manager) Wake(ctx context.Context, id string, leaseUntil time.Time) (engine.Machine, error) {
	if !idRE.MatchString(id) {
		return engine.Machine{}, fmt.Errorf("bad machine ID %q", id)
	}
	now := m.now()
	if !now.Before(leaseUntil) {
		return engine.Machine{}, fmt.Errorf("waking %s: %w", id, links.ErrLeaseExpired)
	}
	em, err := m.e.Get(ctx, id)
	if err != nil {
		return engine.Machine{}, fmt.Errorf("waking %s: %w", id, err)
	}
	if em.State == engine.Running {
		return em, nil
	}
	idle, ok := Idle(em)
	if !ok {
		return engine.Machine{}, fmt.Errorf("waking %s: %w with that ID", id, ErrNoMachine)
	}
	if err := m.checkRoom(ctx, cmp.Or(Name(em), id), em.MemoryMB, false); err != nil {
		return engine.Machine{}, err
	}

	left := leaseUntil.Sub(now)
	wctx, cancel := context.WithTimeoutCause(ctx, left, links.ErrLeaseExpired)
	defer cancel()
	_, err = m.e.Resume(wctx, id, min(timeoutFor(idle), left))
	if err != nil {
		cause := fmt.Errorf("waking %s: %w", id, safeError(err))
		if wctx.Err() == nil {
			return engine.Machine{}, cause
		}
		// The engine may have finished the resume after we stopped waiting for it.
		if errors.Is(context.Cause(wctx), links.ErrLeaseExpired) {
			cause = fmt.Errorf("waking %s: %w before it resumed", id, links.ErrLeaseExpired)
		}
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if _, err := m.settle(bg, id, leaseUntil); err != nil && !errors.Is(err, links.ErrLeaseExpired) {
			return engine.Machine{}, errors.Join(cause, err)
		}
		return engine.Machine{}, cause
	}
	bg, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancelSettle()
	return m.settle(bg, id, leaseUntil)
}

// settle holds a machine Wake resumed to its lease: it shortens an EndAt past the lease, and
// pauses the machine again once the lease has gone unless its own activity keeps it up.
func (m *Manager) settle(ctx context.Context, id string, leaseUntil time.Time) (engine.Machine, error) {
	em, err := m.e.Get(ctx, id)
	if err != nil {
		return engine.Machine{}, fmt.Errorf("waking %s: %w", id, err)
	}
	if em.State != engine.Running {
		return engine.Machine{}, fmt.Errorf("waking %s: %w", id, engine.ErrNotRunning)
	}
	now := m.now()
	if !now.Before(leaseUntil) {
		if m.activity != nil && links.Fresh(m.activity, id, now) {
			return em, nil
		}
		if err := m.pauseAgain(ctx, id); err != nil {
			return engine.Machine{}, fmt.Errorf("waking %s: %w, and pausing it again: %w", id, links.ErrLeaseExpired, err)
		}
		return engine.Machine{}, fmt.Errorf("waking %s: %w after it resumed, so it is paused again", id, links.ErrLeaseExpired)
	}
	if em.EndAt.After(leaseUntil) {
		left := leaseUntil.Sub(now)
		if err := m.e.SetTimeout(ctx, id, left); err != nil {
			return engine.Machine{}, fmt.Errorf("waking %s: holding it to its lease: %w", id, safeError(err))
		}
		em.EndAt = now.Add(left)
	}
	return em, nil
}

func (m *Manager) pauseAgain(ctx context.Context, id string) error {
	if m.fence != nil {
		release, err := m.fence(ctx, id, "a link wake outlived its lease")
		if err != nil {
			return err
		}
		defer release()
	}
	if err := m.e.Pause(ctx, id); err != nil && !errors.Is(err, engine.ErrNotRunning) {
		return safeError(err)
	}
	return nil
}
