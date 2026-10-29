package taskstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	// The driver is modernc.org/sqlite, imported as _ "modernc.org/sqlite", which registers
	// "sqlite" and keeps hibd CGO_ENABLED=0. Adding it is the first step of M13a.

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

// SQLite is the Store on tasks.db.
type SQLite struct {
	db *sql.DB
}

// Open opens the store in the state directory, creating it if it isn't there.
//
// tasks.db belongs to the state directory's owner, the login user, mode 0600, whichever process
// opens first. Open makes it if it is missing, so SQLite running as root never creates it, and
// refuses one that is a symlink, not a regular file or someone else's. SQLite does the rest
// itself: it opens every file with O_NOFOLLOW, and as root it gives -wal and -shm the owner of
// the database file.
// The daemon keeps its connection for its whole life, so -wal and -shm stay in place.
func Open(ctx context.Context, dir statefile.Dir) (*SQLite, error) {
	return nil, errors.New("taskstore: Open is not implemented")
	// TODO:
	//   root, owner, err := dir.Root()
	//   tasks.db only: Lstat. Missing means open O_CREAT|O_EXCL|O_NOFOLLOW mode 0600 and fchown
	//     to owner, never publish by rename. Otherwise refuse unless a regular file of owner's
	//     with mode 0600. -wal and -shm need no pre-creation: upstream os_unix.c opens every file
	//     with O_NOFOLLOW (unixOpen and the -shm open) and, as root, chowns -wal, the journal
	//     and -shm to the database file's owner (robustFchown).
	//   dir.EnsureDirectory for Files, Files/staging, Files/blobs and Files/logs
	//   db, err := sql.Open("sqlite", "file:"+path.Join(dir.Path, DB)) with SKETCH.md's pragmas
	//   create the tables, then refuse a -wal or -shm that isn't owner's: the modernc spike
	//   proves the transpiled driver keeps both behaviors, and this check keeps proving it
}

// Append runs inside one immediate transaction: read the run's log, check the sequence and
// task.CheckNext, insert. The primary key on (task, n, seq) is the fence between two writers
// of one run, so a second daemon or a stale executor gets ErrStale and never a fork in the log.
func (s *SQLite) Append(ctx context.Context, c task.Checkpoint) error {
	body, err := json.Marshal(c.Body)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return busy(err)
	}
	defer tx.Rollback()
	log, err := readLog(ctx, tx, c.Run)
	if err != nil {
		return err
	}
	if c.Seq <= len(log) && c.Seq >= 1 {
		old, _ := json.Marshal(log[c.Seq-1].Body)
		if log[c.Seq-1].Step() == c.Step() && log[c.Seq-1].Kind() == c.Kind() &&
			string(old) == string(body) {
			return nil
		}
		return ErrStale
	}
	if c.Seq != len(log)+1 {
		return ErrStale
	}
	sp, err := readSpec(ctx, tx, c.Run)
	if err != nil {
		return err
	}
	if err := task.CheckNext(c.Run, sp, log, c.Body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (task, n, seq, step, kind, at, by,
		body) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.Run.Task, c.Run.N, c.Seq, c.Step(), c.Kind(),
		c.At.UnixMilli(), c.By, body); err != nil {
		return busy(err)
	}
	return busy(tx.Commit())
}

// readSpec reads run r's spec, which CheckNext needs to know the steps the run skips.
func readSpec(ctx context.Context, tx *sql.Tx, r task.RunID) (task.Spec, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT spec FROM runs WHERE task = ? AND n = ?`, r.Task,
		r.N).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Spec{}, ErrNoRun
	}
	if err != nil {
		return task.Spec{}, busy(err)
	}
	var sp task.Spec
	if err := json.Unmarshal([]byte(raw), &sp); err != nil {
		return task.Spec{}, fmt.Errorf("run %s: reading its spec: %w", r, err)
	}
	return sp, nil
}

func readLog(ctx context.Context, tx *sql.Tx, r task.RunID) ([]task.Checkpoint, error) {
	rows, err := tx.QueryContext(ctx, `SELECT seq, step, kind, at, by, body FROM checkpoints
		WHERE task = ? AND n = ? ORDER BY seq`, r.Task, r.N)
	if err != nil {
		return nil, busy(err)
	}
	defer rows.Close()
	var out []task.Checkpoint
	for rows.Next() {
		var (
			c          task.Checkpoint
			step, kind string
			at         int64
			body       []byte
		)
		if err := rows.Scan(&c.Seq, &step, &kind, &at, &c.By, &body); err != nil {
			return nil, err
		}
		c.Run, c.At = r, time.UnixMilli(at).UTC()
		if c.Body, err = task.DecodeBody(task.Step(step), task.Kind(kind), body); err != nil {
			return nil, fmt.Errorf("run %s checkpoint %d: %w", r, c.Seq, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// busy maps SQLite's SQLITE_BUSY to ErrBusy.
func busy(err error) error {
	// TODO: errors.As(err, *sqlite.Error) with Code() == sqlite3.SQLITE_BUSY wraps ErrBusy
	return err
}

// Submit stages, hashes and syncs every input under Files/staging, then in one transaction
// checks the receipt, the queue and a follow-up's task, inserts the task, run, inputs and
// receipt, and renames the staged inputs into Files/blobs before it commits. A crash after the
// renames and before the commit leaves blobs no row references, which cleanup removes.
func (s *SQLite) Submit(ctx context.Context, sub task.Submission, inputs io.Reader) (task.Receipt, error) {
	return task.Receipt{}, errors.New("taskstore: Submit is not implemented")
	// TODO:
	//   for each input: CheckInputName, copy Size bytes into an owned staging file while hashing,
	//   fsync it, and fill sub.Spec.Inputs[i].Hash
	//   digest := sub.Digest()
	//   BEGIN IMMEDIATE
	//     a receipt for sub.RequestID: same digest returns it, else task.ErrConflict
	//     count Unfinished runs that are Queued, as task.Run derives it: refuse at MaxQueuedRuns
	//     a follow-up: the task exists and isn't removed, and its last run has ended
	//     a new task: task.NewID until the insert doesn't collide
	//     INSERT tasks, runs, inputs, receipts
	//     rename each staged file to Files/blobs/HASH, a no-op when the blob is there, fsync dir
	//   COMMIT
}

// Unfinished reads every run with no keep result, oldest first, as merged views.
func (s *SQLite) Unfinished(ctx context.Context) ([]task.Run, error) {
	return nil, errors.New("taskstore: Unfinished is not implemented")
	// TODO:
	//   SELECT task, n FROM runs r WHERE NOT EXISTS (SELECT 1 FROM checkpoints c WHERE
	//   c.task = r.task AND c.n = r.n AND c.step = 'keep' AND c.kind = 'result')
	//   ORDER BY submitted_at, then view each in one read transaction
}

func (s *SQLite) Receipt(ctx context.Context, requestID string) (task.Receipt, error) {
	return task.Receipt{}, errors.New("taskstore: Receipt is not implemented")
}

func (s *SQLite) Request(ctx context.Context, q NewRequest) (task.Request, error) {
	return task.Request{}, errors.New("taskstore: Request is not implemented")
	// TODO: refuse an invalid q.Kind, BEGIN IMMEDIATE, return the row for q.RequestID if there
	// is one, find the task's live run as Task().Live derives it, ErrNotLive unless an rm,
	// INSERT, COMMIT
}

func (s *SQLite) Task(ctx context.Context, id task.ID) (task.Task, error) {
	return task.Task{}, errors.New("taskstore: Task is not implemented")
	// TODO: one read transaction over tasks, runs, inputs, checkpoints, requests and the latest
	// freeze, merged the way Memory.view merges them
}

func (s *SQLite) Tasks(ctx context.Context) ([]task.Task, error) {
	return nil, errors.New("taskstore: Tasks is not implemented")
}

func (s *SQLite) Run(ctx context.Context, id task.RunID) (task.Run, error) {
	return task.Run{}, errors.New("taskstore: Run is not implemented")
}

func (s *SQLite) RecordFreeze(ctx context.Context, id task.ID, f task.Freeze) error {
	return errors.New("taskstore: RecordFreeze is not implemented")
	// TODO: INSERT INTO freezes SELECT ?, COALESCE(MAX(seq), 0) + 1, ... WHERE task = ?
}

func (s *SQLite) Retire(ctx context.Context, id task.ID, r task.Removal) error {
	return errors.New("taskstore: Retire is not implemented")
}

func (s *SQLite) StartDaemon(ctx context.Context, inst task.Instance, at time.Time) error {
	return errors.New("taskstore: StartDaemon is not implemented")
	// TODO: INSERT INTO daemon VALUES (1, ?, ?) ON CONFLICT (one) DO UPDATE
}

func (s *SQLite) Daemon(ctx context.Context) (task.Instance, error) {
	return "", errors.New("taskstore: Daemon is not implemented")
}

func (s *SQLite) Blob(h task.Hash) (io.ReadCloser, error) {
	return nil, errors.New("taskstore: Blob is not implemented")
	// TODO: open Files/blobs/HASH through the state directory's os.Root, refusing symlinks
}

func (s *SQLite) Close() error { return s.db.Close() }

var _ Store = (*SQLite)(nil)
