package machines

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

const (
	aliasRemovalWait = 90 * time.Second
	aliasRemovalPoll = 5 * time.Second
)

func (m *Manager) cleanAlias(ctx context.Context, alias string, cause error) error {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aliasRemovalWait+cleanupTimeout)
	defer cancel()
	return m.deleteAlias(kctx, alias, cause)
}

func (m *Manager) deleteAlias(ctx context.Context, alias string, cause error) error {
	pctx, cancel := context.WithTimeout(ctx, aliasRemovalWait)
	defer cancel()
	retry := time.NewTicker(aliasRemovalPoll)
	defer retry.Stop()
	expired := func() error {
		return fmt.Errorf("removal of template %s is unconfirmed after 90s: %w", alias, pctx.Err())
	}
	for {
		if pctx.Err() != nil {
			return expired()
		}
		tmpl, err := m.e.Template(pctx, alias)
		if err == nil {
			dctx, stop := context.WithTimeout(ctx, cleanupTimeout)
			defer stop()
			err = m.e.DeleteTemplate(dctx, tmpl.ID)
			if errors.Is(err, engine.ErrNoTemplate) {
				return nil
			}
			return err
		}
		if pctx.Err() != nil {
			return expired()
		}
		if !errors.Is(err, engine.ErrNoTemplate) {
			return err
		}
		if saveCreatedNothing(cause) {
			return nil
		}
		select {
		case <-pctx.Done():
			return expired()
		case <-retry.C:
		}
	}
}
