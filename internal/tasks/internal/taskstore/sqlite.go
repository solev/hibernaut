package taskstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

// Pragmas every connection sets. WAL lets readers in one-shots run beside the daemon's writes,
// FULL syncs each commit, and a short busy timeout turns a long wait into ErrBusy. Temporary
// tables stay in memory, so hibd.service's PrivateTmp never matters. _txlock=immediate takes the
// write lock at BEGIN, so a write transaction never upgrades and deadlocks.
const dsnOptions = "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)" +
	"&_pragma=foreign_keys(1)&_pragma=busy_timeout(2000)&_pragma=temp_store(MEMORY)" +
	"&_txlock=immediate"

// schema is the whole database. Phases, outcomes and stages appear nowhere in it: they are
// derived from checkpoints. Steps, kinds and request kinds are task's enums, checked in Go by
// task.CheckNext, task.DecodeBody and RequestKind.Valid, never copied into a CHECK. Machines
// appear only as the sandbox ID in a machine result, since E2B is the catalog.
const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id              TEXT PRIMARY KEY,
	owner           TEXT NOT NULL,
	actor           TEXT NOT NULL,
	source          TEXT NOT NULL,
	created_at      INTEGER NOT NULL,
	removal_why     TEXT,
	removal_request INTEGER,
	removal_at      INTEGER,
	removed_at      INTEGER
) STRICT;

CREATE TABLE IF NOT EXISTS runs (
	task         TEXT NOT NULL REFERENCES tasks(id),
	n            INTEGER NOT NULL CHECK (n >= 1),
	spec         TEXT NOT NULL,
	actor        TEXT NOT NULL,
	source       TEXT NOT NULL,
	submitted_at INTEGER NOT NULL,
	PRIMARY KEY (task, n)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS inputs (
	task TEXT NOT NULL,
	n    INTEGER NOT NULL,
	pos  INTEGER NOT NULL,
	name TEXT NOT NULL,
	size INTEGER NOT NULL,
	hash BLOB NOT NULL,
	PRIMARY KEY (task, n, pos),
	FOREIGN KEY (task, n) REFERENCES runs(task, n)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS inputs_hash ON inputs(hash);

CREATE TABLE IF NOT EXISTS checkpoints (
	task TEXT NOT NULL,
	n    INTEGER NOT NULL,
	seq  INTEGER NOT NULL CHECK (seq >= 1),
	step TEXT NOT NULL,
	kind TEXT NOT NULL,
	at   INTEGER NOT NULL,
	by   TEXT NOT NULL,
	body TEXT NOT NULL,
	PRIMARY KEY (task, n, seq),
	FOREIGN KEY (task, n) REFERENCES runs(task, n)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS requests (
	id         INTEGER PRIMARY KEY,
	request_id TEXT UNIQUE,
	task       TEXT NOT NULL REFERENCES tasks(id),
	n          INTEGER NOT NULL,
	kind       TEXT NOT NULL,
	at         INTEGER NOT NULL,
	actor      TEXT NOT NULL,
	source     TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS requests_task ON requests(task, n);

CREATE TABLE IF NOT EXISTS freezes (
	task   TEXT NOT NULL,
	seq    INTEGER NOT NULL,
	at     INTEGER NOT NULL,
	frozen INTEGER NOT NULL,
	detail TEXT NOT NULL,
	by     TEXT NOT NULL,
	PRIMARY KEY (task, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS receipts (
	request_id TEXT PRIMARY KEY,
	digest     BLOB NOT NULL,
	task       TEXT NOT NULL,
	n          INTEGER NOT NULL,
	at         INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS daemon (
	one        INTEGER PRIMARY KEY CHECK (one = 1),
	instance   TEXT NOT NULL,
	started_at INTEGER NOT NULL
) STRICT;
`

// SQLite is the Store on tasks.db.
type SQLite struct {
	db  *sql.DB
	dir statefile.Dir
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
	root, owner, err := dir.Root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	fi, err := root.Lstat(DB)
	if errors.Is(err, fs.ErrNotExist) {
		f, createErr := createPrivate(dir, root, owner, DB)
		if createErr != nil && !errors.Is(createErr, fs.ErrExist) {
			return nil, createErr
		}
		if f != nil {
			err = f.Close()
			if err != nil {
				return nil, err
			}
		}
		fi, err = root.Lstat(DB)
	}
	if err != nil {
		return nil, err
	}
	if err := privateFile(fi, owner); err != nil {
		return nil, err
	}
	for _, name := range []string{Files, Files + "/staging", Files + "/blobs", Files + "/logs"} {
		if err := dir.EnsureDirectory(root, owner, name); err != nil {
			return nil, err
		}
	}
	path, err := filepath.Abs(filepath.Join(dir.Path, DB))
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+dsnOptions)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, busy(err)
	}
	for _, name := range []string{DB, DB + "-wal", DB + "-shm"} {
		fi, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if err := privateFile(fi, owner); err != nil {
			return nil, err
		}
	}
	ok = true
	return &SQLite{db: db, dir: dir}, nil
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
	var e *sqlite.Error
	if errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
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
