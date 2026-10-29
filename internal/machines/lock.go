package machines

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

func (m *Manager) tryLock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := m.openLock()
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("locking %s: %w", f.Name(), err)
	}
	return func() { f.Close() }, nil
}

// TryCleanupRead holds the state lock without waiting. Release it after reading cleanup inputs.
func (m *Manager) TryCleanupRead(ctx context.Context) (func(), error) {
	return m.tryLock(ctx)
}

func (m *Manager) lockRoom(ctx context.Context) (func(), error) {
	return m.stateDir().WaitLock(ctx, RoomLock, "machine admission")
}
