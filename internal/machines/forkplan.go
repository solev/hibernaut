package machines

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/room"
)

const (
	copyRoomWait = 15 * time.Second
	roomRetry    = 250 * time.Millisecond
)

func (m *Manager) copyNames(ctx context.Context, origin string, count int) ([]string, error) {
	ems, err := m.e.List(ctx, engine.ListOptions{})
	if err != nil {
		return nil, safeError(err)
	}
	taken := map[string]bool{}
	for _, em := range ems {
		taken[Name(em)] = true
	}
	names := make([]string, 0, count)
	for k := 1; len(names) < count; k++ {
		name := fmt.Sprintf("%s-%d", origin, k)
		if err := hibapi.CheckName(name); err != nil {
			return nil, fmt.Errorf("can't name a copy of %s: %w. Use a shorter machine name", origin, err)
		}
		if !taken[name] {
			names = append(names, name)
		}
	}
	return names, nil
}

func (m *Manager) forkAlias(ctx context.Context, id string) (string, error) {
	if _, err := m.pinPath(id); err != nil {
		return "", err
	}
	for at := m.now(); ; at = at.Add(time.Millisecond) {
		alias := images.ForkTemplate(id, at)
		_, err := m.e.Template(ctx, alias)
		if errors.Is(err, engine.ErrNoTemplate) {
			return alias, nil
		}
		if err != nil {
			return "", safeError(err)
		}
	}
}

func (m *Manager) waitCopyRoom(ctx context.Context, name string, p room.Plan) error {
	wctx, cancel := context.WithTimeout(ctx, copyRoomWait)
	defer cancel()
	retry := time.NewTicker(roomRetry)
	defer retry.Stop()
	var full error
	expired := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if full != nil {
			return fmt.Errorf("%w (waited 15s for room)", full)
		}
		return wctx.Err()
	}
	for {
		if wctx.Err() != nil {
			return expired()
		}
		if err := m.awake(); err != nil {
			return err
		}
		err := m.checkPlan(wctx, name, p)
		if wctx.Err() != nil {
			return expired()
		}
		if !errors.Is(err, room.ErrFull) {
			return err
		}
		full = err
		select {
		case <-wctx.Done():
			return expired()
		case <-retry.C:
		}
	}
}

func (m *Manager) makeCopy(ctx context.Context, origin engine.Machine, template, name string, keys []byte) (hibapi.Machine, error) {
	idle, _ := Idle(origin)
	labels := map[string]string{
		labelName: name, labelImage: origin.Metadata[labelImage],
		labelIdle: origin.Metadata[labelIdle], labelFork: Name(origin),
	}
	if size, ok := machineSize(origin); ok {
		labels[labelSize] = size.Name()
	}
	em, err := m.e.Create(ctx, engine.CreateOptions{Template: template, Timeout: timeoutFor(idle), Metadata: labels})
	if err != nil {
		return hibapi.Machine{}, m.failedCreate(ctx, name, fmt.Errorf("creating %s: %w", name, safeError(err)))
	}
	if err := m.setupCopy(ctx, em.ID, keys); err != nil {
		return hibapi.Machine{}, m.killFailedSetup(ctx, name, em.ID, err)
	}
	m.touch(em.ID)
	return toAPI(em), nil
}

func forkFailure(origin, name string, made []hibapi.Machine, cause error) error {
	prefix := ""
	if len(made) > 0 {
		names := make([]string, len(made))
		for i, em := range made {
			names[i] = em.Name
		}
		prefix = fmt.Sprintf("made %s from %s. ", strings.Join(names, " and "), origin)
	}
	return describe(cause, fmt.Sprintf("%scouldn't make %s: %v", prefix, name, cause))
}
