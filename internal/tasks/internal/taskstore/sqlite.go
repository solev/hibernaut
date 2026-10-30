package taskstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

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
	// SQLite can remove a stale WAL beside an empty database before trying to open it.
	for _, name := range []string{DB + "-wal", DB + "-shm"} {
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := privateFile(fi, owner); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{Files, Files + "/staging", Files + "/blobs", Files + "/logs"} {
		if err := dir.EnsureDirectory(root, owner, name); err != nil {
			return nil, err
		}
		if err := syncDirectory(root, filepath.Dir(name)); err != nil {
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
		c.At.UnixMilli(), c.By, string(body)); err != nil {
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
// checks the receipt, the queue and the task's input budget, inserts the task, run, inputs and
// receipt, and renames the staged inputs into Files/blobs before it commits. A crash after the
// renames and before the commit leaves blobs no row references, which cleanup removes.
func (s *SQLite) Submit(ctx context.Context, sub task.Submission, inputs io.Reader) (task.Receipt, error) {
	sub.Spec = cloneSpec(sub.Spec)
	sub.At = storedTime(sub.At)
	root, owner, err := s.dir.Root()
	if err != nil {
		return task.Receipt{}, err
	}
	defer root.Close()
	staged, err := stageInputs(s.dir, root, owner, &sub.Spec, inputs)
	if err != nil {
		return task.Receipt{}, err
	}
	defer func() {
		for _, f := range staged {
			root.Remove(f.name)
		}
	}()
	digest := sub.Digest()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Receipt{}, busy(err)
	}
	defer tx.Rollback()
	if rec, old, err := readReceipt(ctx, tx, sub.RequestID); err == nil {
		if old != digest {
			return task.Receipt{}, task.ErrConflict
		}
		return rec, nil
	} else if !errors.Is(err, ErrNoReceipt) {
		return task.Receipt{}, err
	}
	var t task.Task
	if sub.Task != "" {
		t, err = readTask(ctx, tx, sub.Task)
		if err != nil {
			return task.Receipt{}, err
		}
		if t.Removal != nil || t.Removing() {
			return task.Receipt{}, task.ErrNoTask
		}
	}
	runs, err := readUnfinished(ctx, tx)
	if err != nil {
		return task.Receipt{}, err
	}
	queued := 0
	for _, r := range runs {
		if r.Queued() {
			queued++
		}
	}
	if queued >= task.MaxQueuedRuns {
		return task.Receipt{}, task.ErrQueueFull
	}
	budget := inputBudget(task.MaxInputBytes)
	if err := budget.take(sub.Spec); err != nil {
		return task.Receipt{}, err
	}
	id := task.RunID{Task: sub.Task, N: 1}
	if sub.Task != "" {
		if _, live := t.Live(); live {
			return task.Receipt{}, task.ErrRunLive
		}
		for _, r := range t.Runs {
			if err := budget.take(r.Spec); err != nil {
				return task.Receipt{}, err
			}
		}
		id.N = len(t.Runs) + 1
	} else {
		for {
			id.Task, err = task.NewID(rand.Reader)
			if err != nil {
				return task.Receipt{}, err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO tasks
				(id, owner, actor, source, created_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (id) DO NOTHING`, id.Task, sub.Actor.Owner, sub.Actor.Name,
				sub.Actor.Source, sub.At.UnixMilli())
			if err != nil {
				return task.Receipt{}, busy(err)
			}
			n, err := result.RowsAffected()
			if err != nil {
				return task.Receipt{}, err
			}
			if n == 1 {
				break
			}
		}
	}
	raw, err := json.Marshal(sub.Spec)
	if err != nil {
		return task.Receipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs
		(task, n, spec, actor, source, submitted_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id.Task, id.N, string(raw), sub.Actor.Name, sub.Actor.Source, sub.At.UnixMilli()); err != nil {
		return task.Receipt{}, busy(err)
	}
	for i, f := range sub.Spec.Inputs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO inputs
			(task, n, pos, name, size, hash) VALUES (?, ?, ?, ?, ?, ?)`,
			id.Task, id.N, i, f.Name, f.Size, f.Hash[:]); err != nil {
			return task.Receipt{}, busy(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO receipts (request_id, digest, task, n, at)
		VALUES (?, ?, ?, ?, ?)`, sub.RequestID, digest[:], id.Task, id.N,
		sub.At.UnixMilli()); err != nil {
		return task.Receipt{}, busy(err)
	}
	if err := publishInputs(root, owner, staged); err != nil {
		return task.Receipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Receipt{}, busy(err)
	}
	return task.Receipt{RequestID: sub.RequestID, Run: id, At: sub.At}, nil
}

// Unfinished reads every run with no keep result, oldest first, as merged views.
func (s *SQLite) Unfinished(ctx context.Context) ([]task.Run, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, busy(err)
	}
	defer tx.Rollback()
	return readUnfinished(ctx, tx)
}

func (s *SQLite) Receipt(ctx context.Context, requestID string) (task.Receipt, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return task.Receipt{}, busy(err)
	}
	defer tx.Rollback()
	rec, _, err := readReceipt(ctx, tx, requestID)
	return rec, err
}

func (s *SQLite) Request(ctx context.Context, q NewRequest) (task.Request, error) {
	if !q.Kind.Valid() {
		return task.Request{}, fmt.Errorf("unknown request kind %q", q.Kind)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Request{}, busy(err)
	}
	defer tx.Rollback()
	if q.RequestID != "" {
		var r task.Request
		var at int64
		err := tx.QueryRowContext(ctx, `SELECT r.id, r.task, r.n, r.kind, r.at,
			r.actor, r.source, t.owner FROM requests r JOIN tasks t ON t.id = r.task
			WHERE r.request_id = ?`, q.RequestID).
			Scan(&r.ID, &r.Task, &r.Run, &r.Kind, &at, &r.Actor.Name, &r.Actor.Source, &r.Actor.Owner)
		if err == nil {
			if r.Task != q.Task || r.Kind != q.Kind {
				return task.Request{}, task.ErrConflict
			}
			r.At = time.UnixMilli(at).UTC()
			return r, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return task.Request{}, busy(err)
		}
	}
	t, err := readTask(ctx, tx, q.Task)
	if err != nil {
		return task.Request{}, err
	}
	if t.Removal != nil {
		return task.Request{}, task.ErrNoTask
	}
	n := 0
	if live, ok := t.Live(); ok {
		n = live.ID.N
	} else if q.Kind != task.RequestRemove {
		return task.Request{}, ErrNotLive
	}
	var requestID any
	if q.RequestID != "" {
		requestID = q.RequestID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO requests
		(request_id, task, n, kind, at, actor, source) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		requestID, q.Task, n, q.Kind, q.At.UnixMilli(), q.Actor.Name, q.Actor.Source)
	if err != nil {
		return task.Request{}, busy(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return task.Request{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Request{}, busy(err)
	}
	actor := q.Actor
	actor.Owner = t.Actor.Owner
	return task.Request{ID: id, Task: q.Task, Run: n, Kind: q.Kind,
		At: storedTime(q.At), Actor: actor}, nil
}

func (s *SQLite) Task(ctx context.Context, id task.ID) (task.Task, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return task.Task{}, busy(err)
	}
	defer tx.Rollback()
	return readTask(ctx, tx, id)
}

func (s *SQLite) Tasks(ctx context.Context) ([]task.Task, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, busy(err)
	}
	defer tx.Rollback()
	return readTasks(ctx, tx)
}

func (s *SQLite) Removing(ctx context.Context) ([]task.Task, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, busy(err)
	}
	defer tx.Rollback()
	return readRemoving(ctx, tx)
}

func (s *SQLite) Run(ctx context.Context, id task.RunID) (task.Run, error) {
	t, err := s.Task(ctx, id.Task)
	if errors.Is(err, task.ErrNoTask) {
		return task.Run{}, ErrNoRun
	}
	if err != nil {
		return task.Run{}, err
	}
	if id.N < 1 || id.N > len(t.Runs) {
		return task.Run{}, ErrNoRun
	}
	return t.Runs[id.N-1], nil
}

func (s *SQLite) RecordFreeze(ctx context.Context, id task.ID, f task.Freeze) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO freezes (task, seq, at, frozen, detail, by)
		SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ? FROM freezes WHERE task = ?`,
		id, f.At.UnixMilli(), f.Frozen, f.Detail, f.By, id)
	return busy(err)
}

func (s *SQLite) Retire(ctx context.Context, id task.ID, r task.Removal) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return busy(err)
	}
	defer tx.Rollback()
	t, err := readTask(ctx, tx, id)
	if err != nil {
		return err
	}
	if t.Removal != nil && r.Done.IsZero() {
		return nil
	}
	if r.Done.IsZero() {
		if _, live := t.Live(); live {
			return task.ErrRunLive
		}
	}
	var done any
	if !r.Done.IsZero() {
		done = r.Done.UnixMilli()
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET removal_why = ?, removal_request = ?,
		removal_at = ?, removed_at = ? WHERE id = ?`, r.Why, r.Request, r.At.UnixMilli(), done, id)
	if err != nil {
		return busy(err)
	}
	return busy(tx.Commit())
}

func (s *SQLite) StartDaemon(ctx context.Context, inst task.Instance, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO daemon (one, instance, started_at) VALUES (1, ?, ?)
		ON CONFLICT (one) DO UPDATE SET instance = excluded.instance,
		started_at = excluded.started_at`, inst, at.UnixMilli())
	return busy(err)
}

func (s *SQLite) Daemon(ctx context.Context) (task.Instance, error) {
	var inst task.Instance
	err := s.db.QueryRowContext(ctx, `SELECT instance FROM daemon WHERE one = 1`).Scan(&inst)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return inst, busy(err)
}

func (s *SQLite) Blob(h task.Hash) (io.ReadCloser, error) {
	root, owner, err := s.dir.Root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openPrivate(root, owner, Files+"/blobs/"+h.String(), os.O_RDONLY)
}

func (s *SQLite) Close() error { return s.db.Close() }

var _ Store = (*SQLite)(nil)
