package taskstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

// Memory is the Store and Logs in memory, for tests. It keeps the same contract as SQLite, which
// storetest checks for both, so tests of the daemon run against it unchanged.
type Memory struct {
	mu        sync.Mutex
	tasks     map[task.ID]*memTask
	receipts  map[string]memReceipt
	requests  []task.Request
	reqIDs    map[string]int64
	freezes   map[task.ID][]task.Freeze
	blobs     map[task.Hash][]byte
	logs      map[string][]byte
	daemon    task.Instance
	appendErr func(task.Checkpoint) error
}

type memTask struct {
	record taskRecord
	runs   []runRow
}

type memReceipt struct {
	digest  task.Hash
	receipt task.Receipt
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{tasks: map[task.ID]*memTask{}, receipts: map[string]memReceipt{},
		reqIDs: map[string]int64{}, freezes: map[task.ID][]task.Freeze{},
		blobs: map[task.Hash][]byte{}, logs: map[string][]byte{}}
}

// FailAppends makes Append call f first and fail with its error, for crash tests.
func (m *Memory) FailAppends(f func(task.Checkpoint) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendErr = f
}

func (m *Memory) Submit(ctx context.Context, sub task.Submission, inputs io.Reader) (task.Receipt, error) {
	sub.At = storedTime(sub.At)
	staged := map[task.Hash][]byte{}
	sub.Spec = cloneSpec(sub.Spec)
	for i, f := range sub.Spec.Inputs {
		if err := task.CheckInputName(f.Name); err != nil {
			return task.Receipt{}, err
		}
		b := make([]byte, f.Size)
		if _, err := io.ReadFull(inputs, b); err != nil {
			return task.Receipt{}, fmt.Errorf("reading %s: %w", f.Name, err)
		}
		h := task.Hash(sha256.Sum256(b))
		sub.Spec.Inputs[i].Hash = h
		staged[h] = b
	}
	digest := sub.Digest()

	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.receipts[sub.RequestID]; ok {
		if r.digest != digest {
			return task.Receipt{}, task.ErrConflict
		}
		return r.receipt, nil
	}
	queued := 0
	for _, r := range m.unfinished() {
		if r.Queued() {
			queued++
		}
	}
	if queued >= task.MaxQueuedRuns {
		return task.Receipt{}, task.ErrQueueFull
	}
	var t *memTask
	if sub.Task != "" {
		t = m.tasks[sub.Task]
		if t == nil || t.record.removal != nil {
			return task.Receipt{}, task.ErrNoTask
		}
		if _, live := m.view(t).Live(); live {
			return task.Receipt{}, task.ErrRunLive
		}
	} else {
		id, err := m.newID()
		if err != nil {
			return task.Receipt{}, err
		}
		t = &memTask{record: taskRecord{id: id, actor: sub.Actor, created: sub.At}}
		m.tasks[id] = t
	}
	actor := sub.Actor
	actor.Owner = ""
	run := runRow{id: task.RunID{Task: t.record.id, N: len(t.runs) + 1}, spec: sub.Spec,
		actor: actor, submitted: sub.At}
	t.runs = append(t.runs, run)
	for h, b := range staged {
		m.blobs[h] = b
	}
	rec := task.Receipt{RequestID: sub.RequestID, Run: run.id, At: sub.At}
	m.receipts[sub.RequestID] = memReceipt{digest: digest, receipt: rec}
	return rec, nil
}

func (m *Memory) newID() (task.ID, error) {
	for {
		id, err := task.NewID(rand.Reader)
		if err != nil {
			return "", err
		}
		if m.tasks[id] == nil {
			return id, nil
		}
	}
}

func (m *Memory) Receipt(ctx context.Context, requestID string) (task.Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.receipts[requestID]
	if !ok {
		return task.Receipt{}, ErrNoReceipt
	}
	return r.receipt, nil
}

func (m *Memory) Request(ctx context.Context, q NewRequest) (task.Request, error) {
	if !q.Kind.Valid() {
		return task.Request{}, fmt.Errorf("unknown request kind %q", q.Kind)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.reqIDs[q.RequestID]; ok && q.RequestID != "" {
		r := m.requests[id-1]
		r.Actor.Owner = m.tasks[r.Task].record.actor.Owner
		return r, nil
	}
	t := m.tasks[q.Task]
	if t == nil {
		return task.Request{}, task.ErrNoTask
	}
	n := 0
	if live, ok := m.view(t).Live(); ok {
		n = live.ID.N
	} else if q.Kind != task.RequestRemove {
		return task.Request{}, ErrNotLive
	}
	r := task.Request{ID: int64(len(m.requests) + 1), Task: q.Task, Run: n, Kind: q.Kind,
		At: storedTime(q.At), Actor: task.Actor{Name: q.Actor.Name, Source: q.Actor.Source}}
	m.requests = append(m.requests, r)
	if q.RequestID != "" {
		m.reqIDs[q.RequestID] = r.ID
	}
	r.Actor.Owner = t.record.actor.Owner
	return r, nil
}

func (m *Memory) Task(ctx context.Context, id task.ID) (task.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[id]
	if t == nil {
		return task.Task{}, task.ErrNoTask
	}
	return m.view(t), nil
}

func (m *Memory) Tasks(ctx context.Context) ([]task.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []task.Task
	for _, t := range m.tasks {
		if t.record.removal == nil || t.record.removal.Done.IsZero() {
			out = append(out, m.view(t))
		}
	}
	slices.SortFunc(out, func(a, b task.Task) int { return b.Created.Compare(a.Created) })
	return out, nil
}

func (m *Memory) Run(ctx context.Context, id task.RunID) (task.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[id.Task]
	if t == nil || id.N < 1 || id.N > len(t.runs) {
		return task.Run{}, ErrNoRun
	}
	return m.view(t).Runs[id.N-1], nil
}

func (m *Memory) Unfinished(ctx context.Context) ([]task.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unfinished(), nil
}

func (m *Memory) unfinished() []task.Run {
	var out []task.Run
	for _, t := range m.tasks {
		if r, ok := m.view(t).Live(); ok {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b task.Run) int { return a.Submitted.Compare(b.Submitted) })
	return out
}

func (m *Memory) Append(ctx context.Context, c task.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.appendErr != nil {
		if err := m.appendErr(c); err != nil {
			return err
		}
	}
	t := m.tasks[c.Run.Task]
	if t == nil || c.Run.N < 1 || c.Run.N > len(t.runs) {
		return ErrNoRun
	}
	r := &t.runs[c.Run.N-1]
	if c.Seq <= len(r.log) && c.Seq >= 1 {
		if sameCheckpoint(r.log[c.Seq-1], c) {
			return nil
		}
		return ErrStale
	}
	if c.Seq != len(r.log)+1 {
		return ErrStale
	}
	if err := task.CheckNext(r.id, r.spec, r.log, c.Body); err != nil {
		return err
	}
	c.At = storedTime(c.At)
	r.log = append(r.log, c)
	return nil
}

func sameCheckpoint(a, b task.Checkpoint) bool {
	ja, _ := json.Marshal(a.Body)
	jb, _ := json.Marshal(b.Body)
	return a.Step() == b.Step() && a.Kind() == b.Kind() && bytes.Equal(ja, jb)
}

func (m *Memory) RecordFreeze(ctx context.Context, id task.ID, f task.Freeze) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f.At = storedTime(f.At)
	m.freezes[id] = append(m.freezes[id], f)
	return nil
}

func (m *Memory) Retire(ctx context.Context, id task.ID, r task.Removal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[id]
	if t == nil {
		return task.ErrNoTask
	}
	if t.record.removal == nil || !r.Done.IsZero() {
		r.At, r.Done = storedTime(r.At), storedTime(r.Done)
		t.record.removal = &r
	}
	return nil
}

func (m *Memory) StartDaemon(ctx context.Context, inst task.Instance, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.daemon = inst
	return nil
}

func (m *Memory) Daemon(ctx context.Context) (task.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.daemon, nil
}

func (m *Memory) Blob(h task.Hash) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[h]
	if !ok {
		return nil, fmt.Errorf("no blob %s", h)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *Memory) Close() error { return nil }

func (m *Memory) view(t *memTask) task.Task {
	rows := taskRows{record: t.record, runs: t.runs}
	if f := m.freezes[t.record.id]; len(f) > 0 {
		rows.freeze = f[len(f)-1]
	}
	for _, q := range m.requests {
		if q.Task == t.record.id {
			rows.requests = append(rows.requests, q)
		}
	}
	return taskView(rows)
}

func logKey(r task.RunID, s task.Stage) string { return r.String() + "/" + string(s) }

// MemoryLogs is the Logs of a Memory.
type MemoryLogs struct{ M *Memory }

func (l MemoryLogs) Size(r task.RunID, s task.Stage) (int64, error) {
	l.M.mu.Lock()
	defer l.M.mu.Unlock()
	return int64(len(l.M.logs[logKey(r, s)])), nil
}

func (l MemoryLogs) Append(r task.RunID, s task.Stage, offset int64, data []byte) error {
	l.M.mu.Lock()
	defer l.M.mu.Unlock()
	k := logKey(r, s)
	if int64(len(l.M.logs[k])) != offset {
		return ErrStale
	}
	var total int64
	for _, stage := range task.Stages() {
		total += int64(len(l.M.logs[logKey(r, stage)]))
	}
	if int64(len(data)) > task.MaxLogBytes-total {
		return ErrLogFull
	}
	l.M.logs[k] = append(l.M.logs[k], data...)
	return nil
}

func (l MemoryLogs) Open(r task.RunID, s task.Stage, offset int64) (io.ReadCloser, error) {
	l.M.mu.Lock()
	defer l.M.mu.Unlock()
	b := l.M.logs[logKey(r, s)]
	if offset < 0 || offset > int64(len(b)) {
		return nil, errors.New("offset past the end of the log")
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(b[offset:]))), nil
}

var (
	_ Store = (*Memory)(nil)
	_ Logs  = MemoryLogs{}
)
