package machines

import (
	"os"

	"github.com/solev/hibernaut/internal/statefile"
)

type stateOwner struct {
	uid, gid int
}

func ownerOf(o statefile.Owner) stateOwner { return stateOwner{uid: o.UID, gid: o.GID} }

func (o stateOwner) statefile() statefile.Owner { return statefile.Owner{UID: o.uid, GID: o.gid} }

// stateDir is the manager's state directory as statefile sees it. It's built on every call so
// that tests which swap m.euid or m.chownFile reach the helpers.
func (m *Manager) stateDir() statefile.Dir {
	return statefile.Dir{Path: m.dir, EUID: m.euid, Chown: m.chownFile}
}

func (m *Manager) stateRoot() (*os.Root, stateOwner, error) {
	root, owner, err := m.stateDir().Root()
	return root, ownerOf(owner), err
}

func (m *Manager) publishOwned(root *os.Root, name string, data []byte, owner stateOwner, replace bool) (*os.File, error) {
	return m.stateDir().PublishOwned(root, name, data, owner.statefile(), replace)
}

func (m *Manager) writeOwnedPin(id string, data []byte) error {
	root, owner, err := m.stateRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := m.ensurePins(root, owner); err != nil {
		return err
	}
	pins, err := root.OpenRoot("host-keys")
	if err != nil {
		return err
	}
	defer pins.Close()
	f, err := m.publishOwned(pins, id, data, owner, true)
	if err != nil {
		return err
	}
	return f.Close()
}
