package machines

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/solev/hibernaut/internal/engine"
)

func (m *Manager) setupCopy(ctx context.Context, id string, keys []byte) error {
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	res, err := m.e.Exec(ctx, id, engine.ExecRequest{
		Cmd: "rm", User: "root",
		Args: []string{"-f", "/etc/hibernaut/ssh/ssh_host_ed25519_key", "/etc/hibernaut/ssh/ssh_host_ed25519_key.pub"},
	})
	if err != nil {
		return fmt.Errorf("removing the copied host key: %w", safeError(err))
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("removing the copied host key exited %d: %q", res.ExitCode, tail(res.Stderr, 1024))
	}
	if _, err := m.initMachine(ctx, id, keys); err != nil {
		return err
	}
	return safeError(m.setup.Write(ctx, m.e, id))
}

func (m *Manager) failedCreate(ctx context.Context, name string, cause error) error {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	em, err := m.lookup(kctx, name)
	if errors.Is(err, ErrNoMachine) {
		return cause
	}
	if err != nil {
		return describe(errors.Join(cause, err), fmt.Sprintf("%v. Also couldn't check whether %s started: %v. Check with: hib ls. Remove it with: hib rm %s", cause, name, err, name))
	}
	if err := m.killCopy(kctx, em.ID); err != nil {
		return m.copyLeftBehind(cause, name, em.ID, err)
	}
	return cause
}

func (m *Manager) killCopy(ctx context.Context, id string) error {
	if err := m.e.Kill(ctx, id); err != nil && !errors.Is(err, engine.ErrNotFound) {
		return err
	}
	path, err := m.pinPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) copyLeftBehind(cause error, name, id string, err error) error {
	return describe(errors.Join(cause, err), fmt.Sprintf("%v. Also couldn't remove %s (ID %s): %v. Remove it with: hib rm %s", cause, name, id, err, name))
}
