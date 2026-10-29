// Package taskstore keeps the task subsystem's records: tasks, runs, each run's checkpoint log,
// requests, freeze rows, receipts and the daemon's instance, plus the input blobs and the host's
// copies of run logs. It is shared by the daemon, every one-shot hibd process and the proxy,
// each with its own connection.
//
// Every write is one short transaction. No method takes a callback, so no transaction can span
// a machine, E2B or lock call, and the package imports nothing that could make one: a test
// checks its imports.
package taskstore

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

// Paths under the state directory.
const (
	// DB is the database. -wal and -shm sit beside it, and all three belong to the state
	// directory's owner.
	DB = "tasks.db"
	// Files holds staging, blobs and logs.
	Files = "tasks"
)

// Errors beside those in package task.
var (
	// ErrStale means another writer appended to the run first. The caller's view is old, so it
	// stops acting on the run.
	ErrStale = errors.New("another writer appended to the run")
	// ErrBusy means the database stayed locked past the busy timeout. hibd answers BUSY, which
	// is retryable.
	ErrBusy = errors.New("the task database is busy")
	// ErrNoRun means no run has that ID.
	ErrNoRun = errors.New("no such run")
	// ErrNotLive means a request found no live run to act on.
	ErrNotLive = errors.New("the task has no live run")
	// ErrNoReceipt means no submission used that request ID.
	ErrNoReceipt = errors.New("no submission with that request ID")
)

// NewRequest is a cancel, rescue or rm as a CLI one-shot asks for it.
type NewRequest struct {
	// RequestID makes the request safe to retry: the same ID returns the first row.
	RequestID string
	Task      task.ID
	Kind      task.RequestKind
	Actor     task.Actor
	At        time.Time
}

// Store is the task records. Views it returns are merged at read time and leave
// task.Run.Machine for the caller to fill in from E2B.
type Store interface {
	// Submit saves a new task, or a follow-up run of one, with its inputs, then answers. inputs
	// streams each of sub.Spec.Inputs' bytes in order, Size bytes each. Submit stages them,
	// hashes and syncs them, and publishes them as blobs before the run's rows commit, so a
	// committed run always has its inputs. It fills each input's Hash.
	//
	// The same RequestID with the same digest returns the first receipt, with a different one
	// task.ErrConflict. It fails with task.ErrQueueFull when task.MaxQueuedRuns runs are queued,
	// and for a follow-up with task.ErrNoTask or task.ErrRunLive.
	Submit(ctx context.Context, sub task.Submission, inputs io.Reader) (task.Receipt, error)
	// Receipt returns the answer a submission got, or ErrNoReceipt.
	Receipt(ctx context.Context, requestID string) (task.Receipt, error)
	// Request records a request against the task's live run, or ErrNotLive. An rm needs no live
	// run.
	Request(ctx context.Context, q NewRequest) (task.Request, error)

	// Task returns a task with all its runs, or task.ErrNoTask.
	Task(ctx context.Context, id task.ID) (task.Task, error)
	// Tasks returns every task not yet removed, newest first.
	Tasks(ctx context.Context) ([]task.Task, error)
	// Run returns one run, or ErrNoRun.
	Run(ctx context.Context, id task.RunID) (task.Run, error)
	// Unfinished returns every run whose keep step hasn't closed, oldest first.
	Unfinished(ctx context.Context) ([]task.Run, error)

	// Append adds c to its run's log. c.Seq must be the next sequence number, or Append fails
	// with ErrStale, and task.CheckNext must allow c.Body. Appending a checkpoint that is
	// already there, same sequence and body, succeeds, so a retry after a lost commit is safe.
	Append(ctx context.Context, c task.Checkpoint) error
	// RecordFreeze adds a freeze row for the task's machine.
	RecordFreeze(ctx context.Context, id task.ID, f task.Freeze) error
	// Retire records a task's removal: begun when r.Done is zero, finished otherwise.
	Retire(ctx context.Context, id task.ID, r task.Removal) error

	// StartDaemon records inst as the daemon now running. Intents other instances left open
	// show as recovering until inst closes them.
	StartDaemon(ctx context.Context, inst task.Instance, at time.Time) error
	// Daemon returns the instance StartDaemon last recorded.
	Daemon(ctx context.Context) (task.Instance, error)

	// Blob opens an input by hash.
	Blob(h task.Hash) (io.ReadCloser, error)
	// Close closes the store.
	Close() error
}

// Logs is the host's copy of each run's stage output, one file per stage under Files. The
// length of the file is the only record of how much has been copied.
type Logs interface {
	// Size is how many bytes of stage s's output the host has.
	Size(r task.RunID, s task.Stage) (int64, error)
	// Append adds data at offset, which must be Size, or it fails with ErrStale. It syncs before
	// it returns.
	Append(r task.RunID, s task.Stage, offset int64, data []byte) error
	// Open reads stage s's output from offset, for hib task logs.
	Open(r task.RunID, s task.Stage, offset int64) (io.ReadCloser, error)
}
