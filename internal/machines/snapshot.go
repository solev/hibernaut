package machines

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/termsafe"
)

const (
	snapshotTimeout = 2 * time.Minute
	saveWindow      = 10 * time.Minute
	saveCallTimeout = saveWindow
)

type describedError struct {
	cause   error
	message string
}

func (e describedError) Error() string { return termsafe.String(e.message) }
func (e describedError) Unwrap() error { return e.cause }

func describe(err error, message string) error {
	return describedError{cause: err, message: message}
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	return describe(err, err.Error())
}

func (m *Manager) canSave() error {
	if err := m.awake(); err != nil {
		return err
	}
	if m.durable == nil {
		return errors.New("hibd can't confirm snapshots reached disk: configure Durable before saving or forking")
	}
	return m.checkState()
}

func (m *Manager) checkPlan(ctx context.Context, name string, p room.Plan) error {
	ems, host, err := m.roomNow(ctx)
	if err != nil {
		return safeError(err)
	}
	return safeError(host.CheckPlan(name, p, roomMachines(ems)))
}

// saveOrigin saves em as alias, and puts em back in the state it started in. With links on, the
// whole round trip runs behind em's fence, so no link wakes em or reaches it meanwhile.
func (m *Manager) saveOrigin(ctx context.Context, em engine.Machine, alias string) (engine.TemplateInfo, error) {
	if m.fenced == nil {
		return m.saveRoundTrip(ctx, em, alias)
	}
	why := "image save"
	if images.IsForkTemplate(alias) {
		why = "fork"
	}
	var tmpl engine.TemplateInfo
	err := m.fenced(ctx, em.ID, Name(em), why, func(ctx context.Context) error {
		var err error
		tmpl, err = m.saveRoundTrip(ctx, em, alias)
		return err
	})
	return tmpl, err
}

func (m *Manager) saveRoundTrip(ctx context.Context, em engine.Machine, alias string) (engine.TemplateInfo, error) {
	if err := m.prepareSnapshot(ctx, em); err != nil {
		return engine.TemplateInfo{}, err
	}
	if err := m.awake(); err != nil {
		return engine.TemplateInfo{}, m.restoreAfterFailure(ctx, em, err, true)
	}
	if err := ctx.Err(); err != nil {
		return engine.TemplateInfo{}, m.restoreAfterFailure(ctx, em, err, true)
	}
	key, record, err := m.startSave(em.ID, Name(em))
	if err != nil {
		return engine.TemplateInfo{}, m.restoreAfterFailure(ctx, em, safeError(err), true)
	}
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveCallTimeout)
	defer cancel()
	tmpl, err := m.e.SaveTemplate(call, em.ID, alias)
	recordErr := m.finishSave(key, record, err)
	if err != nil {
		return engine.TemplateInfo{}, errors.Join(m.failedSave(ctx, em, alias, err), m.saveEndFailure(em, record, recordErr))
	}
	if em.State == engine.Paused {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := m.e.Pause(rctx, em.ID); err != nil && !errors.Is(err, engine.ErrNotRunning) {
			cleanup := m.cleanAlias(ctx, alias, nil)
			state := m.readOriginState(ctx, em, true)
			if cleanup == nil {
				cause := describe(err, fmt.Sprintf("couldn't pause %s again: %v. %s Nothing was saved.", Name(em), err, state))
				return engine.TemplateInfo{}, errors.Join(cause, m.saveEndFailure(em, record, recordErr))
			}
			message := fmt.Sprintf("saved %s, but couldn't pause %s again: %v. %s", alias, Name(em), err, state)
			cause := describe(errors.Join(err, cleanup), message+aliasRemovalMessage(alias, cleanup))
			return tmpl, errors.Join(cause, m.saveEndFailure(em, record, recordErr))
		}
	}
	if failure := m.saveEndFailure(em, record, recordErr); failure != nil {
		return tmpl, describe(failure, fmt.Sprintf("saved %s, but %v", alias, failure))
	}
	return tmpl, nil
}

func (m *Manager) saveEndFailure(em engine.Machine, record cleanup.SaveRecord, cause error) error {
	if cause == nil {
		return nil
	}
	now := m.now()
	message := fmt.Sprintf("recording the save's end failed for %s: %v. ", Name(em), cause)
	after := record.Started.Add(saveCallTimeout)
	if !now.Before(after) {
		message += fmt.Sprintf("Snapshot cleanup needs a pause after %s. ", after.UTC().Format(time.RFC3339))
	}
	return describe(cause, message+saveRecovery(record, em.State == engine.Running, now))
}

func (m *Manager) prepareSnapshot(ctx context.Context, em engine.Machine) error {
	name := Name(em)
	timeout := m.saveTimeout(em)
	if em.State == engine.Running {
		if err := Renew(ctx, m.e, em, timeout, m.now()); err != nil {
			return describe(err, fmt.Sprintf("couldn't renew %s before saving: %v. %s Nothing was saved.", name, err, m.readOriginState(ctx, em, false)))
		}
		if err := m.e.Pause(ctx, em.ID); err != nil {
			return describe(err, fmt.Sprintf("couldn't pause %s before saving: %v. %s Nothing was saved.", name, err, m.readOriginState(ctx, em, false)))
		}
	}
	dctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	err := m.durable(dctx, em.ID)
	cancel()
	if err != nil {
		return m.restoreAfterFailure(ctx, em, describe(err, fmt.Sprintf("hibd couldn't confirm %s's snapshot reached disk: %v. Nothing was saved.", name, err)), false)
	}
	resumed, err := m.e.Resume(ctx, em.ID, timeout)
	if err != nil {
		if em.State == engine.Paused {
			if restoration := m.restoreOrigin(ctx, em); restoration != nil {
				err = errors.Join(err, fmt.Errorf("couldn't pause %s again: %w", name, restoration))
			}
		}
		return describe(err, fmt.Sprintf("couldn't resume %s before saving: %v. %s Nothing was saved.", name, err, m.readOriginState(ctx, em, true)))
	}
	if err := Renew(ctx, m.e, resumed, timeout, m.now()); err != nil {
		return m.restoreAfterFailure(ctx, em, describe(err, fmt.Sprintf("couldn't renew %s after resuming: %v. Nothing was saved.", name, err)), true)
	}
	return nil
}

func (m *Manager) saveTimeout(em engine.Machine) time.Duration {
	idle, _ := Idle(em)
	return max(timeoutFor(idle), saveWindow)
}

func (m *Manager) restoreAfterFailure(ctx context.Context, em engine.Machine, cause error, safeSnapshot bool) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if em.State == engine.Running {
		if _, err := m.e.Resume(rctx, em.ID, m.saveTimeout(em)); err != nil {
			return describe(errors.Join(cause, err), fmt.Sprintf("%v Couldn't resume %s: %v. %s", cause, Name(em), err, m.readOriginState(rctx, em, safeSnapshot)))
		}
	} else if err := m.e.Pause(rctx, em.ID); err != nil && !errors.Is(err, engine.ErrNotRunning) {
		return describe(errors.Join(cause, err), fmt.Sprintf("%v Couldn't pause %s again: %v. %s", cause, Name(em), err, m.readOriginState(rctx, em, safeSnapshot)))
	}
	return cause
}

func (m *Manager) readOriginState(ctx context.Context, em engine.Machine, safeSnapshot bool) string {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	return m.originState(rctx, em, safeSnapshot)
}

func (m *Manager) originState(ctx context.Context, em engine.Machine, safeSnapshot bool) string {
	current, err := m.e.Get(ctx, em.ID)
	name := Name(em)
	if errors.Is(err, engine.ErrNotFound) {
		return fmt.Sprintf("%s is gone. Check it with: hib ls.", name)
	}
	if err != nil {
		return fmt.Sprintf("%s's state is unknown: %v. Check it with: hib ls.", name, err)
	}
	if current.State == engine.Paused {
		if !safeSnapshot {
			return fmt.Sprintf("%s is paused. Its snapshot hasn't been confirmed on disk. Check it with: hib ls.", name)
		}
		return fmt.Sprintf("%s is paused at a safe snapshot. Wake it with: ssh %s.hib.", name, name)
	}
	return fmt.Sprintf("%s is %s.", name, current.State)
}
