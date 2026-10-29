package taskstore

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"syscall"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

// FileLogs is Logs as one file per run and stage under Files/logs, each the state directory
// owner's, mode 0600.
type FileLogs struct{ Dir statefile.Dir }

// Size is the file's length, or 0 when it is missing.
func (l FileLogs) Size(r task.RunID, s task.Stage) (int64, error) {
	name, err := logPath(r, s)
	if err != nil {
		return 0, err
	}
	root, owner, err := l.Dir.Root()
	if err != nil {
		return 0, err
	}
	defer root.Close()
	return logSize(root, owner, name)
}

// Append adds data at offset, which must be Size, or ErrStale. It syncs before it returns. It
// refuses past task.MaxLogBytes summed over the run's stages: the cap is per run, not per stage.
func (l FileLogs) Append(r task.RunID, s task.Stage, offset int64, data []byte) error {
	name, err := logPath(r, s)
	if err != nil {
		return err
	}
	root, owner, err := l.Dir.Root()
	if err != nil {
		return err
	}
	defer root.Close()
	dir := path.Dir(name)
	for _, name := range []string{Files, Files + "/logs", path.Dir(dir), dir} {
		if err := l.Dir.EnsureDirectory(root, owner, name); err != nil {
			return err
		}
		if err := syncDirectory(root, path.Dir(name)); err != nil {
			return err
		}
	}
	lock, err := root.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	f, err := createPrivate(l.Dir, root, owner, name)
	if errors.Is(err, fs.ErrExist) {
		f, err = openPrivate(root, owner, name, os.O_WRONLY)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != offset {
		return ErrStale
	}
	var total int64
	for _, stage := range task.Stages() {
		size, err := logSize(root, owner, path.Join(dir, string(stage)))
		if err != nil {
			return err
		}
		total += size
	}
	if int64(len(data)) > task.MaxLogBytes-total {
		return ErrLogFull
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDirectory(root, dir)
}

// Open reads stage s's output from offset, for hib task logs.
func (l FileLogs) Open(r task.RunID, s task.Stage, offset int64) (io.ReadCloser, error) {
	name, err := logPath(r, s)
	if err != nil {
		return nil, err
	}
	root, owner, err := l.Dir.Root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openPrivate(root, owner, name, os.O_RDONLY)
	if errors.Is(err, fs.ErrNotExist) && offset == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && (offset < 0 || offset > fi.Size()) {
		err = errors.New("offset past the end of the log")
	}
	if err == nil {
		_, err = f.Seek(offset, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func logPath(r task.RunID, s task.Stage) (string, error) {
	if _, err := task.ParseID(string(r.Task)); err != nil {
		return "", err
	}
	if r.N < 1 || !slices.Contains(task.Stages(), s) {
		return "", fmt.Errorf("invalid log run %s or stage %q", r, s)
	}
	return Files + "/logs/" + r.String() + "/" + string(s), nil
}

func logSize(root *os.Root, owner statefile.Owner, name string) (int64, error) {
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := privateFile(fi, owner); err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

var _ Logs = FileLogs{}
