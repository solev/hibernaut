package taskstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/solev/hibernaut/internal/statefile"
)

func privateFile(fi fs.FileInfo, owner statefile.Owner) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 ||
		int(st.Uid) != owner.UID || int(st.Gid) != owner.GID {
		return fmt.Errorf("%s: needs a private regular file owned by %d:%d",
			fi.Name(), owner.UID, owner.GID)
	}
	return nil
}

func openPrivate(root *os.Root, owner statefile.Owner, name string, flag int) (*os.File, error) {
	f, err := root.OpenFile(name, flag|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		err = privateFile(fi, owner)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func createPrivate(d statefile.Dir, root *os.Root, owner statefile.Owner, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			root.Remove(name)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return nil, err
	}
	euid := os.Geteuid()
	if d.EUID != nil {
		euid = d.EUID()
	}
	if euid == 0 {
		chown := d.Chown
		if chown == nil {
			chown = (*os.File).Chown
		}
		if err := chown(f, owner.UID, owner.GID); err != nil {
			return nil, err
		}
	}
	ok = true
	return f, nil
}

func syncDirectory(root *os.Root, name string) error {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	err = f.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
		return nil
	}
	return err
}
