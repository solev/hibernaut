package taskstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

func readTask(ctx context.Context, tx *sql.Tx, id task.ID) (task.Task, error) {
	rows := taskRows{record: taskRecord{id: id}}
	t := &rows.record
	var created int64
	var why sql.NullString
	var request, at, done sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT owner, actor, source, created_at, removal_why,
		removal_request, removal_at, removed_at FROM tasks WHERE id = ?`, id).
		Scan(&t.actor.Owner, &t.actor.Name, &t.actor.Source, &created, &why, &request, &at, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, task.ErrNoTask
	}
	if err != nil {
		return task.Task{}, busy(err)
	}
	t.created = time.UnixMilli(created).UTC()
	if why.Valid {
		t.removal = &task.Removal{Why: why.String, Request: request.Int64,
			At: time.UnixMilli(at.Int64).UTC()}
		if done.Valid {
			t.removal.Done = time.UnixMilli(done.Int64).UTC()
		}
	}
	rs, err := tx.QueryContext(ctx, `SELECT n, spec, actor, source, submitted_at
		FROM runs WHERE task = ? ORDER BY n`, id)
	if err != nil {
		return task.Task{}, busy(err)
	}
	defer rs.Close()
	for rs.Next() {
		r := runRow{id: task.RunID{Task: id}}
		var raw string
		var submitted int64
		if err := rs.Scan(&r.id.N, &raw, &r.actor.Name, &r.actor.Source, &submitted); err != nil {
			return task.Task{}, err
		}
		if err := json.Unmarshal([]byte(raw), &r.spec); err != nil {
			return task.Task{}, err
		}
		r.submitted = time.UnixMilli(submitted).UTC()
		rows.runs = append(rows.runs, r)
	}
	if err := rs.Err(); err != nil {
		return task.Task{}, busy(err)
	}
	if err := rs.Close(); err != nil {
		return task.Task{}, err
	}
	for i := range rows.runs {
		r := &rows.runs[i]
		if r.log, err = readLog(ctx, tx, r.id); err != nil {
			return task.Task{}, err
		}
	}
	qs, err := tx.QueryContext(ctx, `SELECT id, n, kind, at, actor, source
		FROM requests WHERE task = ? ORDER BY id`, id)
	if err != nil {
		return task.Task{}, busy(err)
	}
	defer qs.Close()
	for qs.Next() {
		q := task.Request{Task: id}
		var at int64
		if err := qs.Scan(&q.ID, &q.Run, &q.Kind, &at, &q.Actor.Name, &q.Actor.Source); err != nil {
			return task.Task{}, err
		}
		q.At = time.UnixMilli(at).UTC()
		rows.requests = append(rows.requests, q)
	}
	if err := qs.Err(); err != nil {
		return task.Task{}, busy(err)
	}
	if err := qs.Close(); err != nil {
		return task.Task{}, err
	}
	var frozenAt int64
	f := &rows.freeze
	err = tx.QueryRowContext(ctx, `SELECT at, frozen, detail, by FROM freezes
		WHERE task = ? ORDER BY seq DESC LIMIT 1`, id).
		Scan(&frozenAt, &f.Frozen, &f.Detail, &f.By)
	if err == nil {
		f.At = time.UnixMilli(frozenAt).UTC()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, busy(err)
	}
	return taskView(rows), nil
}

func readTasks(ctx context.Context, tx *sql.Tx) ([]task.Task, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id FROM tasks
		WHERE removed_at IS NULL ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, busy(err)
	}
	defer rs.Close()
	var ids []task.ID
	for rs.Next() {
		var id task.ID
		if err := rs.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rs.Err(); err != nil {
		return nil, busy(err)
	}
	if err := rs.Close(); err != nil {
		return nil, err
	}
	var out []task.Task
	for _, id := range ids {
		t, err := readTask(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func readUnfinished(ctx context.Context, tx *sql.Tx) ([]task.Run, error) {
	rs, err := tx.QueryContext(ctx, `SELECT DISTINCT r.task FROM runs r
		WHERE NOT EXISTS (SELECT 1 FROM checkpoints c WHERE c.task = r.task AND c.n = r.n
		AND c.step = ? AND c.kind = ?)`, task.StepKeep, task.Result)
	if err != nil {
		return nil, busy(err)
	}
	defer rs.Close()
	var ids []task.ID
	for rs.Next() {
		var id task.ID
		if err := rs.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rs.Err(); err != nil {
		return nil, busy(err)
	}
	if err := rs.Close(); err != nil {
		return nil, err
	}
	var out []task.Run
	for _, id := range ids {
		t, err := readTask(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if live, ok := t.Live(); ok {
			out = append(out, live)
		}
	}
	slices.SortFunc(out, func(a, b task.Run) int { return a.Submitted.Compare(b.Submitted) })
	return out, nil
}

func readReceipt(ctx context.Context, tx *sql.Tx, requestID string) (task.Receipt, task.Hash, error) {
	rec := task.Receipt{RequestID: requestID}
	var digest []byte
	var at int64
	err := tx.QueryRowContext(ctx, `SELECT digest, task, n, at FROM receipts
		WHERE request_id = ?`, requestID).Scan(&digest, &rec.Run.Task, &rec.Run.N, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Receipt{}, task.Hash{}, ErrNoReceipt
	}
	if err != nil {
		return task.Receipt{}, task.Hash{}, busy(err)
	}
	if len(digest) != len(task.Hash{}) {
		return task.Receipt{}, task.Hash{}, fmt.Errorf("receipt %s: invalid digest", requestID)
	}
	rec.At = time.UnixMilli(at).UTC()
	return rec, task.Hash(digest), nil
}
