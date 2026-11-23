package taskstore

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

type stagedInput struct {
	name string
	hash task.Hash
}

const stagingLock = "tasks-staging.lock"

func holdStagingInputs(d statefile.Dir) (*os.File, error) {
	lock, err := d.OpenLock(stagingLock)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

func sweepInputs(d statefile.Dir, root *os.Root, owner statefile.Owner) error {
	unlock, ok, err := d.TryLock(stagingLock, "staged input cleanup")
	if err != nil || !ok {
		return err
	}
	defer unlock()
	dir, err := root.OpenFile(Files+"/staging", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	var errs []error
	removed := false
	for _, entry := range entries {
		name := Files + "/staging/" + entry.Name()
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !fi.Mode().IsRegular() || !ok || int(st.Uid) != owner.UID {
			continue
		}
		if err := root.Remove(name); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		} else {
			removed = true
		}
	}
	if removed {
		errs = append(errs, syncDirectory(root, Files+"/staging"))
	}
	return errors.Join(errs...)
}

type inputBudget int64

func (b *inputBudget) take(sp task.Spec) error {
	for _, f := range sp.Inputs {
		if f.Size > int64(*b) {
			return task.ErrInputFull
		}
		*b -= inputBudget(f.Size)
	}
	return nil
}

func stageInputs(d statefile.Dir, root *os.Root, owner statefile.Owner, sp *task.Spec,
	inputs io.Reader) (staged []stagedInput, err error) {
	defer func() {
		if err != nil {
			for _, f := range staged {
				root.Remove(f.name)
			}
		}
	}()
	for i, input := range sp.Inputs {
		if err := task.CheckInputName(input.Name); err != nil {
			return staged, err
		}
		if input.Size < 0 {
			return staged, fmt.Errorf("input %s: negative size", input.Name)
		}
		name := Files + "/staging/" + rand.Text()
		f, err := createPrivate(d, root, owner, name)
		if err != nil {
			return staged, err
		}
		staged = append(staged, stagedInput{name: name})
		h := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(f, h), inputs, input.Size)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return staged, fmt.Errorf("staging %s: %w", input.Name, err)
		}
		hash := task.Hash(h.Sum(nil))
		sp.Inputs[i].Hash = hash
		staged[len(staged)-1].hash = hash
	}
	return staged, nil
}

func publishInputs(root *os.Root, owner statefile.Owner, staged []stagedInput) error {
	for _, f := range staged {
		name := Files + "/blobs/" + f.hash.String()
		fi, err := root.Lstat(name)
		if err == nil {
			if err := privateFile(fi, owner); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := root.Rename(f.name, name); err != nil {
			return err
		}
	}
	return syncDirectory(root, Files+"/blobs")
}
