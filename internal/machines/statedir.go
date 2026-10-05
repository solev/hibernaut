package machines

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func (m *Manager) ensurePins(root *os.Root, owner stateOwner) error {
	return m.ensureDirectory(root, owner, "host-keys")
}

func (m *Manager) ensureDirectory(root *os.Root, owner stateOwner, name string) error {
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if m.euid() == 0 {
			err = m.makeOwnedDirectory(root, owner, name)
		} else {
			err = root.Mkdir(name, 0o700)
		}
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err = root.Lstat(name)
	}
	if err != nil {
		return err
	}
	pinsOwner, err := directoryOwner(fi, root.Name()+"/"+name, m.euid())
	if err != nil {
		return err
	}
	if pinsOwner.uid != owner.uid {
		return fmt.Errorf("state directory %s/%s: belongs to uid %d, not the state owner %d", root.Name(), name, pinsOwner.uid, owner.uid)
	}
	return nil
}

func (m *Manager) makeOwnedDirectory(root *os.Root, owner stateOwner, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil {
		return err
	}
	d, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := m.chownFile(d, owner.uid, owner.gid); err != nil {
		root.Remove(name)
		return err
	}
	return nil
}

func (m *Manager) openLock() (*os.File, error) {
	root, owner, err := m.stateRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var f *os.File
	if m.euid() == 0 {
		f, err = root.OpenFile("hibd.lock", os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if errors.Is(err, fs.ErrNotExist) {
			f, err = m.publishOwned(root, "hibd.lock", nil, owner, false)
			if errors.Is(err, fs.ErrExist) {
				f, err = root.OpenFile("hibd.lock", os.O_RDWR|syscall.O_NOFOLLOW, 0)
			}
		}
	} else {
		f, err = root.OpenFile("hibd.lock", os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if errors.Is(err, fs.ErrExist) {
			f, err = root.OpenFile("hibd.lock", os.O_RDWR|syscall.O_NOFOLLOW, 0)
		}
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || int(st.Uid) != owner.uid || fi.Mode().Perm()&0o022 != 0 {
		f.Close()
		return nil, fmt.Errorf("state lock %s/hibd.lock: needs a regular private file owned by uid %d", m.dir, owner.uid)
	}
	return f, nil
}
