package machines

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

type stateOwner struct {
	uid, gid int
}

func directoryOwner(fi fs.FileInfo, name string, uid int) (stateOwner, error) {
	if !fi.IsDir() {
		return stateOwner{}, fmt.Errorf("state directory %s: not a directory", name)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return stateOwner{}, fmt.Errorf("state directory %s: can't tell its owner", name)
	}
	if uid != 0 && int(st.Uid) != uid {
		return stateOwner{}, fmt.Errorf("state directory %s: belongs to uid %d, not %d", name, st.Uid, uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return stateOwner{}, fmt.Errorf("state directory %s: others can write to it (%v)", name, fi.Mode().Perm())
	}
	return stateOwner{uid: int(st.Uid), gid: int(st.Gid)}, nil
}

func (m *Manager) stateRoot() (*os.Root, stateOwner, error) {
	if err := checkDir(m.dir, m.euid()); err != nil {
		return nil, stateOwner{}, err
	}
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return nil, stateOwner{}, err
	}
	fi, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, stateOwner{}, err
	}
	owner, err := directoryOwner(fi, m.dir, m.euid())
	if err != nil {
		root.Close()
		return nil, stateOwner{}, err
	}
	return root, owner, nil
}

func (m *Manager) publishOwned(root *os.Root, name string, data []byte, owner stateOwner, replace bool) (published *os.File, err error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	tmp := fmt.Sprintf(".%s.tmp-%x", name, token)
	f, err := root.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() {
		if published == nil {
			f.Close()
		}
		root.Remove(tmp)
	}()
	if err := f.Chmod(0o600); err != nil {
		return nil, err
	}
	if err := m.chownFile(f, owner.uid, owner.gid); err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if replace {
		err = root.Rename(tmp, name)
	} else {
		err = root.Link(tmp, name)
	}
	if err != nil {
		return nil, err
	}
	if !replace {
		if err := root.Remove(tmp); err != nil {
			return nil, err
		}
	}
	d, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, errors.ErrUnsupported) {
		return nil, err
	}
	return f, nil
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
