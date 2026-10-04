// Package atomicfile writes files so that readers, and the disk after a crash or power cut, see
// the old file or the new one, never a mix.
package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// WriteFile replaces the file at path with data, with mode perm whatever the umask. A symlink
// at path is replaced, not written through. If WriteFile fails, path is unchanged, unless only
// the last step, syncing the directory, failed. Errors never hold data.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	return write(path, data, perm, os.Rename)
}

// WriteNewFile is WriteFile for a file that mustn't exist yet. If anything is at path, even a
// dangling symlink, it fails with an error matching fs.ErrExist.
func WriteNewFile(path string, data []byte, perm fs.FileMode) error {
	return write(path, data, perm, link)
}

type file interface {
	Name() string
	Chmod(fs.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

var (
	createTemp = func(dir, pattern string) (file, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	remove        = os.Remove
	syncDirectory = (*os.File).Sync
)

func write(path string, data []byte, perm fs.FileMode, publish func(tmp, path string) error) error {
	dir := filepath.Dir(path)
	f, err := createTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	err = fill(f, data, perm)
	if err == nil {
		err = publish(f.Name(), path)
	}
	if err != nil {
		_ = remove(f.Name())
		return err
	}
	return syncDir(dir)
}

func fill(f file, data []byte, perm fs.FileMode) error {
	err := f.Chmod(perm)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// link publishes tmp at path with a hard link, which unlike a rename fails if path exists.
func link(tmp, path string) error {
	if err := os.Link(tmp, path); err != nil {
		return err
	}
	return remove(tmp)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	err = syncDirectory(d)
	// Some filesystems can't sync a directory, and say so with EINVAL or ENOTSUP.
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
		return nil
	}
	return err
}
