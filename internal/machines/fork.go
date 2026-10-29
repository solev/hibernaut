package machines

import (
	"context"
	"errors"
	"fmt"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/room"
)

// MaxCopies is the most copies one Fork makes.
const MaxCopies = 10

// Fork makes count copies of machine, from 1 to MaxCopies, named MACHINE-k with the lowest free
// k. Each copy gets a fresh host key, Config.Setup, and machine's idle time and image. Fork
// stops at the first copy it can't make, and returns the copies made with the error. machine
// is saved as SaveImage saves, and left running or paused as it was. A task's machine is
// refused with task.ErrNoCopy before anything touches it.
// A failed save can take about 90 s longer while hibd removes what E2B left. The state lock stays
// held during that wait, so Create, Pause and Remove wait behind it. Dial and List don't take it.
func (m *Manager) Fork(ctx context.Context, machine string, count int) ([]hibapi.Machine, error) {
	if err := hibapi.CheckName(machine); err != nil {
		return nil, err
	}
	if count < 1 || count > MaxCopies {
		return nil, fmt.Errorf("bad copy count %d: use 1 to %d", count, MaxCopies)
	}
	if err := m.canSave(); err != nil {
		return nil, err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	em, err := m.lookup(ctx, machine)
	if err != nil {
		return nil, err
	}
	if err := refuseTaskCopy(em); err != nil {
		return nil, err
	}
	names, err := m.copyNames(ctx, machine, count)
	if err != nil {
		return nil, err
	}
	keys, err := m.authorizedKeys()
	if err != nil {
		return nil, err
	}
	running := max(1, count)
	if em.State == engine.Paused {
		running = max(2, count)
	}
	if err := m.checkPlan(ctx, machine, room.Plan{MemoryMB: em.MemoryMB, Running: running, Snapshots: 2 + count, Peak: max(2, count)}); err != nil {
		return nil, err
	}
	alias, err := m.forkAlias(ctx, em.ID)
	if err != nil {
		return nil, err
	}
	tmpl, err := m.saveOrigin(ctx, em, alias)
	if err != nil {
		return nil, err
	}
	made := make([]hibapi.Machine, 0, count)
	for i, name := range names {
		p := room.Plan{MemoryMB: em.MemoryMB, Running: count - i, Snapshots: count - i}
		if err := m.waitCopyRoom(ctx, name, p); err != nil {
			return made, forkFailure(machine, name, made, err)
		}
		if err := m.awake(); err != nil {
			return made, forkFailure(machine, name, made, err)
		}
		copy, err := m.makeCopy(ctx, em, tmpl.ID, name, keys)
		if err != nil {
			return made, forkFailure(machine, name, made, err)
		}
		made = append(made, copy)
	}
	return made, nil
}

// SweepForks deletes the templates Fork saved machines into, except those copies still use. It
// returns an error wrapping ErrBusy if it can't take the state lock at once.
func (m *Manager) SweepForks(ctx context.Context) error {
	if err := m.checkState(); err != nil {
		return err
	}
	unlock, err := m.tryLock(ctx)
	if err != nil {
		return fmt.Errorf("sweeping fork templates: %w", err)
	}
	defer unlock()
	tmpls, err := m.e.SavedTemplates(ctx)
	if err != nil {
		return safeError(err)
	}
	var failures []error
	for _, tmpl := range tmpls {
		if !images.IsForkTemplate(tmpl.Alias) {
			continue
		}
		if err := m.e.DeleteTemplate(ctx, tmpl.ID); err != nil &&
			!errors.Is(err, engine.ErrTemplateInUse) && !errors.Is(err, engine.ErrNoTemplate) {
			failures = append(failures, fmt.Errorf("removing fork template %s: %w", tmpl.Alias, safeError(err)))
		}
	}
	return safeError(errors.Join(failures...))
}
