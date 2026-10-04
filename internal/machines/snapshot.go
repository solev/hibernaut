package machines

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/termsafe"
)

const snapshotTimeout = 2 * time.Minute

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

func (m *Manager) saveOrigin(ctx context.Context, em engine.Machine, alias string) (engine.TemplateInfo, error) {
	if err := m.prepareSnapshot(ctx, em); err != nil {
		return engine.TemplateInfo{}, err
	}
	if err := m.awake(); err != nil {
		return engine.TemplateInfo{}, m.restoreAfterFailure(ctx, em, err, true)
	}
	tmpl, err := m.e.SaveTemplate(ctx, em.ID, alias)
	if err != nil {
		return engine.TemplateInfo{}, m.failedSave(ctx, em, alias, err)
	}
	if em.State == engine.Paused {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := m.e.Pause(rctx, em.ID); err != nil && !errors.Is(err, engine.ErrNotRunning) {
			return tmpl, describe(err, fmt.Sprintf("saved %s, but couldn't pause %s again: %v. %s", alias, Name(em), err, m.readOriginState(rctx, em, true)))
		}
	}
	return tmpl, nil
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
		return describe(err, fmt.Sprintf("couldn't resume %s before saving: %v. %s Nothing was saved.", name, err, m.readOriginState(ctx, em, true)))
	}
	if err := Renew(ctx, m.e, resumed, timeout, m.now()); err != nil {
		return m.restoreAfterFailure(ctx, em, describe(err, fmt.Sprintf("couldn't renew %s after resuming: %v. Nothing was saved.", name, err)), true)
	}
	return nil
}

func (m *Manager) saveTimeout(em engine.Machine) time.Duration {
	idle, _ := Idle(em)
	return max(timeoutFor(idle), snapshotTimeout)
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

func (m *Manager) failedSave(ctx context.Context, em engine.Machine, alias string, cause error) error {
	cleanup := m.cleanAlias(ctx, alias)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	name := Name(em)
	outcome := "Nothing was saved."
	if cleanup != nil {
		outcome = "The new template may still exist."
	}
	var message string
	if errors.Is(cause, engine.ErrCheckpoint) {
		if em.State == engine.Running {
			if _, err := m.e.Resume(rctx, em.ID, m.saveTimeout(em)); err != nil {
				cause = errors.Join(cause, err)
				message = fmt.Sprintf("E2B couldn't save %s (its checkpoint failed). Couldn't resume %s: %v. %s %s Try again.", name, name, err, m.readOriginState(rctx, em, true), outcome)
			} else {
				message = fmt.Sprintf("E2B couldn't save %s (its checkpoint failed). %s is running again as it was when the save began. %s Try again.", name, name, outcome)
			}
		} else {
			cause = m.restoreAfterFailure(rctx, em, cause, true)
			message = fmt.Sprintf("E2B couldn't save %s (its checkpoint failed): %v. %s %s Try again.", name, cause, m.readOriginState(rctx, em, true), outcome)
		}
	} else {
		if em.State == engine.Paused {
			cause = m.restoreAfterFailure(rctx, em, cause, true)
		}
		message = fmt.Sprintf("E2B couldn't confirm the save of %s: %v. %s Check it with: hib ls. Don't retry until you've checked.", name, cause, m.readOriginState(rctx, em, true))
	}
	if cleanup != nil {
		cause = errors.Join(cause, cleanup)
		message += fmt.Sprintf(" Also couldn't remove template %s: %v.", alias, cleanup)
	}
	return describe(cause, message)
}

func (m *Manager) deleteAlias(ctx context.Context, alias string) error {
	tmpl, err := m.e.Template(ctx, alias)
	if errors.Is(err, engine.ErrNoTemplate) {
		return nil
	}
	if err != nil {
		return err
	}
	err = m.e.DeleteTemplate(ctx, tmpl.ID)
	if errors.Is(err, engine.ErrNoTemplate) {
		return nil
	}
	return err
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
