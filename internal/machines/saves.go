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
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

// ErrSaveUnsettled means a save record still inhibits snapshot deletion and trimming.
var ErrSaveUnsettled = errors.New("an image save has not settled")

var saveTokenRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

type saveEntryKind int

const (
	unexpectedSaveFile saveEntryKind = iota
	saveRecordFile
	savePublicationDebris
)

type saveFile struct {
	key, machine string
	info         fs.FileInfo
}

func saveMachine(key string) (string, bool) {
	id, token, ok := strings.Cut(key, "-")
	return id, ok && idRE.MatchString(id) && saveTokenRE.MatchString(token)
}

func classifySaveEntry(key string, info fs.FileInfo, owner, euid int) (saveEntryKind, string) {
	if id, ok := saveMachine(key); ok {
		return saveRecordFile, id
	}
	name, token, staged := strings.Cut(strings.TrimPrefix(key, "."), ".tmp-")
	id, record := saveMachine(name)
	st, stat := info.Sys().(*syscall.Stat_t)
	if strings.HasPrefix(key, ".") && staged && record && saveTokenRE.MatchString(token) &&
		info.Mode().IsRegular() && stat && (int(st.Uid) == owner || int(st.Uid) == euid) {
		return savePublicationDebris, id
	}
	id, _, _ = strings.Cut(strings.TrimPrefix(key, "."), "-")
	if !idRE.MatchString(id) {
		id = "unknown"
	}
	return unexpectedSaveFile, id
}

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
	r.Ended, r.Succeeded = m.now(), cause == nil
	return m.writeSave(key, r, true)
}

// CheckSaves settles save records against cleanup's locked listing. It requires TryCleanupRead.
func (m *Manager) CheckSaves(ctx context.Context, l cleanup.Listing, dry bool, logf func(string, ...any)) error {
	state, err := filepath.Abs(m.dir)
	if err != nil {
		return fmt.Errorf("%w: locating save records: %w", ErrSaveUnsettled, err)
	}
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
	var records, debris []saveFile
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := entry.Name()
		fi, err := saves.Lstat(key)
		if err != nil {
			return damagedSave(state, key, err)
		}
		kind, machine := classifySaveEntry(key, fi, owner.uid, m.euid())
		file := saveFile{key: key, machine: machine, info: fi}
		switch kind {
		case saveRecordFile:
			records = append(records, file)
		case savePublicationDebris:
			debris = append(debris, file)
		default:
			return fmt.Errorf("%w: unexpected save entry %s for machine %s. Hibd never writes this entry. Move it out of saves/ before cleanup",
				ErrSaveUnsettled, filepath.Join(state, "saves", key), machine)
		}
	}
	var remaining []fs.FileInfo
	for _, file := range debris {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dry {
			remaining = append(remaining, file.info)
			logf("would remove save publication debris %s for %s", filepath.Join(state, "saves", file.key), file.machine)
		} else {
			if err := removeSave(saves, file.key); err != nil {
				return fmt.Errorf("%w: removing save publication debris %s for %s: %w. Move it out of saves/ before cleanup",
					ErrSaveUnsettled, filepath.Join(state, "saves", file.key), file.machine, err)
			}
			logf("removed save publication debris %s for %s", filepath.Join(state, "saves", file.key), file.machine)
		}
	}
	bootID := m.bootID()
	var settled []string
	for _, file := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := readSave(saves, file.key, owner.uid, remaining)
		if err != nil {
			return damagedSave(state, file.key, err)
		}
		if !cleanup.SaveSettled(l, r, bootID, saveCallTimeout) {
			return fmt.Errorf("%w: %s (%s) began saving at %s. It clears once the machine runs or pauses, is removed, or the host restarts",
				ErrSaveUnsettled, r.Name, r.Machine, r.Started)
		}
		settled = append(settled, file.key)
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

func damagedSave(state, key string, cause error) error {
	id, _ := saveMachine(key)
	path := filepath.Join(state, "saves", key)
	damaged := filepath.Join(state, "saves-damaged")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	return fmt.Errorf("%w: save record %s for machine %s is unreadable: %w. Once the host has restarted, move exactly this file out of saves/: sudo mkdir -p %s && sudo mv %s %s",
		ErrSaveUnsettled, path, id, cause, quote(damaged), quote(path), quote(damaged+"/"))
}

func readSave(root *os.Root, key string, uid int, debris []fs.FileInfo) (cleanup.SaveRecord, error) {
	id, _ := saveMachine(key)
	r := cleanup.SaveRecord{Machine: id}
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
	if !ok || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || int(st.Uid) != uid || fi.Size() > 64<<10 {
		return r, errors.New("needs a private regular file owned by the state owner")
	}
	links := uint64(1)
	for _, info := range debris {
		if os.SameFile(fi, info) {
			links++
		}
	}
	if uint64(st.Nlink) != links {
		return r, fmt.Errorf("has %d links, expected %d after debris removal", st.Nlink, links)
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Machine != id || hibapi.CheckName(r.Name) != nil || r.Started.IsZero() || r.Succeeded && r.Ended.IsZero() {
		return r, errors.New("invalid machine, name or save times")
	}
	return r, nil
}
