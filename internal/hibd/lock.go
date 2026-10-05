package hibd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func waitLock(ctx context.Context, stateDir, name, what string) (func(), error) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, ok, err := tryLock(stateDir, name, what)
		if err != nil || ok {
			return unlock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

func tryLock(stateDir, name, what string) (unlock func(), ok bool, err error) {
	f, err := openLock(stateDir, name)
	if err != nil {
		return nil, false, fmt.Errorf("opening the %s lock: %w", what, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("locking the %s: %w", what, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// The state directory belongs to the login user, who could put a symlink at the lock's name.
// os.Root keeps a link from leaving the directory but follows one inside it, even with
// O_NOFOLLOW, unless the open has O_CREATE and O_EXCL. So an existing lock must be the regular
// file at its name.
func openLock(stateDir, name string) (*os.File, error) {
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if !errors.Is(err, fs.ErrExist) {
		return f, err
	}
	notAFile := fmt.Errorf("%s isn't a regular file", filepath.Join(stateDir, name))
	named, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !named.Mode().IsRegular() {
		return nil, notAFile
	}
	f, err = root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if opened, err := f.Stat(); err != nil || !os.SameFile(named, opened) {
		f.Close()
		return nil, cmp.Or(err, notAFile)
	}
	return f, nil
}
