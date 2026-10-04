package machines

import "context"

func (m *Manager) cleanAlias(ctx context.Context, alias string) error {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	return m.deleteAlias(kctx, alias)
}
