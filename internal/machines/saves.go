package machines

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

// ErrSaveUnsettled means a save record still inhibits snapshot deletion and trimming.
var ErrSaveUnsettled = errors.New("an image save has not settled")

func readBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (m *Manager) startSave(id, name string) (string, cleanup.SaveRecord, error) {
	r := cleanup.SaveRecord{Machine: id, Name: name, Started: m.now(), BootID: m.bootID()}
	if !idRE.MatchString(id) {
		return "", r, fmt.Errorf("unexpected save machine ID %q", id)
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", r, err
	}
	key := fmt.Sprintf("%s-%x", id, token)
	if err := m.writeSave(key, r, false); err != nil {
		return "", r, fmt.Errorf("recording save of %s: %w", name, err)
	}
	return key, r, nil
}

func (m *Manager) writeSave(key string, r cleanup.SaveRecord, replace bool) error {
	root, owner, err := m.stateRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := m.ensureDirectory(root, owner, "saves"); err != nil {
		return err
	}
	if err := syncSaveDirectory(root); err != nil {
		return err
	}
	saves, err := root.OpenRoot("saves")
	if err != nil {
		return err
	}
	defer saves.Close()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := m.publishOwned(saves, key, data, owner, replace)
	if err != nil {
		return err
	}
	return f.Close()
}

func syncSaveDirectory(root *os.Root) error {
	f, err := root.Open(".")
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

func removeSave(root *os.Root, key string) error {
	if err := root.Remove(key); err != nil {
		return err
	}
	return syncSaveDirectory(root)
}

func (m *Manager) finishSave(key string, r cleanup.SaveRecord, cause error) error {
	if saveCreatedNothing(cause) && !errors.Is(cause, engine.ErrNoAnswer) {
		root, owner, err := m.stateRoot()
		if err != nil {
			return err
		}
		defer root.Close()
		if err := m.ensureDirectory(root, owner, "saves"); err != nil {
			return err
		}
		saves, err := root.OpenRoot("saves")
		if err != nil {
			return err
		}
		defer saves.Close()
		return removeSave(saves, key)
	}
	r.Ended, r.Answered = m.now(), !errors.Is(cause, engine.ErrNoAnswer)
	return m.writeSave(key, r, true)
}

// CheckSaves settles save records against cleanup's locked listing. It requires TryCleanupRead.
func (m *Manager) CheckSaves(ctx context.Context, l cleanup.Listing, dry bool, logf func(string, ...any)) error {
	root, owner, err := m.stateRoot()
	if err != nil {
		return fmt.Errorf("%w: reading save records: %w", ErrSaveUnsettled, err)
	}
	defer root.Close()
	if _, err := root.Lstat("saves"); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := m.ensureDirectory(root, owner, "saves"); err != nil {
		return fmt.Errorf("%w: reading save records: %w", ErrSaveUnsettled, err)
	}
	saves, err := root.OpenRoot("saves")
	if err != nil {
		return fmt.Errorf("%w: reading save records: %w", ErrSaveUnsettled, err)
	}
	defer saves.Close()
	entries, err := fs.ReadDir(saves.FS(), ".")
	if err != nil {
		return fmt.Errorf("%w: reading save records: %w", ErrSaveUnsettled, err)
	}
	bootID := m.bootID()
	var settled []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := readSave(saves, entry.Name(), owner.uid)
		if err != nil {
			return fmt.Errorf("%w: save record %s for %s begun %s is unreadable: %w. Repair the record before cleanup. A readable record clears once the machine runs or pauses, is removed, or the host restarts",
				ErrSaveUnsettled, entry.Name(), r.Machine, r.Started, err)
		}
		if !cleanup.SaveSettled(l, r, bootID, saveCallTimeout) {
			return fmt.Errorf("%w: %s (%s) began saving at %s. It clears once the machine runs or pauses, is removed, or the host restarts",
				ErrSaveUnsettled, r.Name, r.Machine, r.Started)
		}
		settled = append(settled, entry.Name())
	}
	for _, key := range settled {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dry {
			logf("would clear settled save record %s", key)
			continue
		}
		if err := removeSave(saves, key); err != nil {
			return fmt.Errorf("%w: clearing save record %s: %w", ErrSaveUnsettled, key, err)
		}
		logf("cleared settled save record %s", key)
	}
	return nil
}

func readSave(root *os.Root, key string, uid int) (cleanup.SaveRecord, error) {
	r := cleanup.SaveRecord{Machine: strings.SplitN(key, "-", 2)[0]}
	f, err := root.OpenFile(key, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return r, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return r, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || int(st.Uid) != uid || st.Nlink != 1 || fi.Size() > 64<<10 {
		return r, errors.New("needs a private regular file owned by the state owner")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if !idRE.MatchString(r.Machine) || hibapi.CheckName(r.Name) != nil || r.Started.IsZero() || r.Answered && r.Ended.IsZero() {
		return r, errors.New("invalid machine, name or save times")
	}
	return r, nil
}
