package machines

import (
	"context"
	"errors"
	"fmt"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
)

func (m *Manager) failedSave(ctx context.Context, em engine.Machine, alias string, cause error) error {
	restoration := m.restoreOrigin(ctx, em)
	cleanup := m.cleanAlias(ctx, alias, cause)
	state := m.readOriginState(ctx, em, true)
	message := saveFailureMessage(em, cause, restoration, state)
	if cleanup != nil {
		message += aliasRemovalMessage(alias, cleanup)
	} else {
		message += " Nothing was saved."
		if errors.Is(cause, engine.ErrCheckpoint) {
			message += " Try again."
		} else if !saveCreatedNothing(cause) {
			message += " Check it with: hib ls. Don't retry until you've checked."
		}
	}
	return describe(errors.Join(cause, restoration, cleanup), message)
}

func (m *Manager) restoreOrigin(ctx context.Context, em engine.Machine) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if em.State == engine.Running {
		_, err := m.e.Resume(rctx, em.ID, m.saveTimeout(em))
		return err
	}
	err := m.e.Pause(rctx, em.ID)
	if errors.Is(err, engine.ErrNotRunning) {
		return nil
	}
	return err
}

func saveCreatedNothing(cause error) bool {
	return errors.Is(cause, engine.ErrNotRunning) || errors.Is(cause, engine.ErrNotFound)
}

func saveFailureMessage(em engine.Machine, cause, restoration error, state string) string {
	name := Name(em)
	message := fmt.Sprintf("E2B couldn't confirm the save of %s: %v.", name, cause)
	if errors.Is(cause, engine.ErrCheckpoint) {
		message = fmt.Sprintf("E2B couldn't save %s (its checkpoint failed).", name)
		if restoration == nil && em.State == engine.Running && state == fmt.Sprintf("%s is running.", name) {
			return fmt.Sprintf("%s %s is running again as it was when the save began.", message, name)
		}
	} else if saveCreatedNothing(cause) {
		message = fmt.Sprintf("E2B couldn't save %s: %v.", name, cause)
	}
	if restoration != nil {
		action := fmt.Sprintf("resume %s", name)
		if em.State == engine.Paused {
			action = fmt.Sprintf("pause %s again", name)
		}
		message += fmt.Sprintf(" Couldn't %s: %v.", action, restoration)
	}
	return fmt.Sprintf("%s %s", message, state)
}

func aliasRemovalMessage(alias string, cleanup error) string {
	message := fmt.Sprintf(" Also couldn't remove template %s: %v. Its removal is unconfirmed.", alias, cleanup)
	if name, ok := images.SavedName(alias); ok {
		return fmt.Sprintf("%s The image template may still exist, hidden by E2B for a few minutes. Wait a few minutes, remove it with: hib image rm %s, then try again.", message, name)
	}
	return fmt.Sprintf("%s Fork template %s may be left.", message, alias)
}
