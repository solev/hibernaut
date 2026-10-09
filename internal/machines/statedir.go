package machines

import (
	"os"
)

func (m *Manager) ensurePins(root *os.Root, owner stateOwner) error {
	return m.ensureDirectory(root, owner, "host-keys")
}

func (m *Manager) ensureDirectory(root *os.Root, owner stateOwner, name string) error {
	return m.stateDir().EnsureDirectory(root, owner.statefile(), name)
}

func (m *Manager) openLock() (*os.File, error) {
	return m.stateDir().OpenLock("hibd.lock")
}
