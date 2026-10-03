package hibd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/vault"
)

var canaryNameRE = regexp.MustCompile(`^leakscan-canary-[0-9a-f]{12}$`)

func lockCanary(stateDir string) (*os.File, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "leakscan-canary.lock"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another canary scan is running")
		}
		return nil, fmt.Errorf("locking canary scan: %w", err)
	}
	return f, nil
}

func reconcileCanaries(ctx context.Context, cfg config, e engine.Engine) error {
	ms, err := e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running, engine.Paused}})
	if err != nil {
		return fmt.Errorf("finding leftover canaries: %w", err)
	}
	for _, m := range ms {
		if !canaryNameRE.MatchString(machines.Name(m)) {
			continue
		}
		if err := e.Kill(ctx, m.ID); err != nil && !errors.Is(err, engine.ErrNotFound) {
			return fmt.Errorf("removing leftover canary %s: %w", m.ID, err)
		}
	}
	f, err := vault.Load(cfg.vaultPath)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(f.Secrets(), func(s vault.Secret) bool { return canaryNameRE.MatchString(s.Name) }) {
		return nil
	}
	return vault.Update(cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, _ vault.Key) error {
		for _, s := range f.Secrets() {
			if canaryNameRE.MatchString(s.Name) {
				if err := f.Remove(s.Name); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
