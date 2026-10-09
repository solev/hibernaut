// Package statefile holds the file locking that hibd's state directory is built on. It sits
// below hibd so that packages hibd imports, such as links, can take the same locks without an
// import cycle. The bodies came from internal/hibd/lock.go unchanged; hibd still calls them
// under its old names.
package statefile

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

// WaitLock takes the lock, waiting until it is free or ctx is done. It is the cancellable form
// of TryLock, for the callers that have to queue rather than fail.
func WaitLock(ctx context.Context, stateDir, name, what string) (func(), error) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, ok, err := TryLock(stateDir, name, what)
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

// TryLock takes the lock if it is free. It reports ok false, with no error, when someone else
// holds it, so a caller can decide between waiting and giving up.
func TryLock(stateDir, name, what string) (unlock func(), ok bool, err error) {
	f, err := OpenLock(stateDir, name)
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

// Held reports whether someone holds the lock, without taking it. A shared lock conflicts with
// the exclusive lock TryLock takes and with nothing else, so failing to get it means a holder
// is alive. This is how a fence tells a running operation from one whose process has died: the
// lock goes away with the process, whatever killed it, so no staleness rule has to guess.
func Held(stateDir, name string) (bool, error) {
	f, err := OpenLock(stateDir, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("opening %s: %w", filepath.Join(stateDir, name), err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("testing %s: %w", filepath.Join(stateDir, name), err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// OpenLock opens the lock file, creating it if it isn't there.
//
// The state directory belongs to the login user, who could put a symlink at the lock's name.
// os.Root keeps a link from leaving the directory but follows one inside it, even with
// O_NOFOLLOW, unless the open has O_CREATE and O_EXCL. So an existing lock must be the regular
// file at its name.
func OpenLock(stateDir, name string) (*os.File, error) {
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
