package taskstore

import (
	"errors"
	"io"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

// FileLogs is Logs as one file per run and stage under Files/logs, each the state directory
// owner's, mode 0600.
type FileLogs struct{ Dir statefile.Dir }

var errLogsNotImplemented = errors.New("taskstore: FileLogs is not implemented")

// Size is the file's length, or 0 when it is missing.
func (l FileLogs) Size(r task.RunID, s task.Stage) (int64, error) {
	return 0, errLogsNotImplemented
	// TODO: Lstat Files/logs/TASK/N/STAGE. Missing is 0. Refuse a symlink or a file that isn't
	// a regular one of the directory owner's.
}

// Append adds data at offset, which must be Size, or ErrStale. It syncs before it returns. It
// refuses past task.MaxLogBytes summed over the run's stages: the cap is per run, not per stage.
func (l FileLogs) Append(r task.RunID, s task.Stage, offset int64, data []byte) error {
	return errLogsNotImplemented
	// TODO: open O_WRONLY|O_CREATE|O_NOFOLLOW mode 0600 through statefile, fchown to the
	// owner, fstat: size != offset is ErrStale. Sum every stage's size for the cap, write, fsync.
}

// Open reads stage s's output from offset, for hib task logs.
func (l FileLogs) Open(r task.RunID, s task.Stage, offset int64) (io.ReadCloser, error) {
	return nil, errLogsNotImplemented
	// TODO: open O_RDONLY|O_NOFOLLOW, check it is the owner's regular file, seek to offset.
}
