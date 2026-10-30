// Package tasks is the one door into the task subsystem. hibd's task commands, its daemon and
// every engine it builds go through it. The store, the executor and the Owner live under
// internal/, so the compiler refuses a checkpoint, freeze row or grant written from anywhere
// else: a change that looks right from one file can't skip a rule the others keep.
package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskmachine"
	"github.com/solev/hibernaut/internal/tasks/internal/taskrun"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

const (
	// DaemonLock is held by the one hibd daemon that runs tasks. A second daemon fails to start
	// tasks instead of running every task twice.
	DaemonLock = "tasks-daemon.lock"
	// Poll is how often the daemon ticks the task supervisor.
	Poll = taskrun.DefaultPoll
	// exemptWait bounds the tasks.db read busy makes for each task machine.
	exemptWait = 2 * time.Second
)

var (
	// ErrNotLive means the task has no live run to cancel or rescue.
	ErrNotLive = taskstore.ErrNotLive
	// ErrBusy means tasks.db stayed locked past the busy timeout. Retrying is safe.
	ErrBusy = taskstore.ErrBusy
	// ErrNoReceipt means no submission used that request ID.
	ErrNoReceipt = taskstore.ErrNoReceipt
	// ErrDaemonRunning means another hibd daemon holds DaemonLock.
	ErrDaemonRunning = errors.New("another hibd daemon is running tasks")
)

// Request is a cancel, rescue or rm as a CLI one-shot asks for it.
type Request = taskstore.NewRequest

// Machines is the task machine lifecycle hibd provides: Manager behind the links fence.
type Machines = taskrun.Machines

// Tasks is the task records in one state directory. It opens tasks.db on first use, so a
// command that never touches a task machine never opens it. It is safe for concurrent use.
type Tasks struct {
	dir statefile.Dir
	now func() time.Time

	mu sync.Mutex
	st taskstore.Store
}

// Open returns the tasks in dir. It reads nothing until a method needs tasks.db.
func Open(dir statefile.Dir, now func() time.Time) *Tasks {
	return &Tasks{dir: dir, now: now}
}

func (t *Tasks) store(ctx context.Context) (taskstore.Store, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st != nil {
		return t.st, nil
	}
	st, err := taskstore.Open(ctx, t.dir)
	if err != nil {
		return nil, err
	}
	t.st = st
	return st, nil
}

// Submit saves a new task, or a follow-up run of one, with its inputs. See hib task start.
func (t *Tasks) Submit(ctx context.Context, sub task.Submission, inputs io.Reader) (task.Receipt, error) {
	st, err := t.store(ctx)
	if err != nil {
		return task.Receipt{}, err
	}
	return st.Submit(ctx, sub, inputs)
}

// Receipt returns the answer a submission got, or ErrNoReceipt.
func (t *Tasks) Receipt(ctx context.Context, requestID string) (task.Receipt, error) {
	st, err := t.store(ctx)
	if err != nil {
		return task.Receipt{}, err
	}
	return st.Receipt(ctx, requestID)
}

// Request records a cancel, rescue or rm. The daemon acts on it at the run's next boundary.
func (t *Tasks) Request(ctx context.Context, q Request) (task.Request, error) {
	st, err := t.store(ctx)
	if err != nil {
		return task.Request{}, err
	}
	return st.Request(ctx, q)
}

// Task returns a task with all its runs, or task.ErrNoTask.
func (t *Tasks) Task(ctx context.Context, id task.ID) (task.Task, error) {
	st, err := t.store(ctx)
	if err != nil {
		return task.Task{}, err
	}
	return st.Task(ctx, id)
}

// Instance returns the daemon instance tasks.db recorded last, or "" if no daemon has started.
// hibapi.RunOf needs it: an intent another instance left open shows as recovering.
func (t *Tasks) Instance(ctx context.Context) (task.Instance, error) {
	st, err := t.store(ctx)
	if err != nil {
		return "", err
	}
	return st.Daemon(ctx)
}

// List returns every task not yet removed, newest first.
func (t *Tasks) List(ctx context.Context) ([]task.Task, error) {
	st, err := t.store(ctx)
	if err != nil {
		return nil, err
	}
	return st.Tasks(ctx)
}

// Close closes tasks.db if a method opened it.
func (t *Tasks) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st == nil {
		return nil
	}
	err := t.st.Close()
	t.st = nil
	return err
}

// Guard wraps e so every pause, wake, dial, timeout and save of a task machine passes the task
// machine guards, whichever hibd command makes it. Plain machines pass straight through. by
// names the process in freeze rows, like "hibd daemon" or "hibd pause".
func (t *Tasks) Guard(e engine.Engine, by string) engine.Engine {
	return taskmachine.New(e, ownerStore{t}, t.dir, t.now, by)
}

// Guarded reports whether e came from Guard.
func Guarded(e engine.Engine) bool {
	_, ok := e.(*taskmachine.Owner)
	return ok
}

// Check returns nil if op may touch machine m now, and otherwise why not: task.Access's table,
// read from tasks.db, failing closed. Plain machines pass. hibd asks it before hib pause, hib
// rm, leakscan's pause and a setup push.
func (t *Tasks) Check(ctx context.Context, m engine.Machine, op task.Op) error {
	return taskmachine.Check(ctx, ownerStore{t}, t.now(), m, op)
}

// Exempt reports whether busy must leave m alone: its run holds it, or tasks.db can't say. busy
// then never renews or shortens its timeout, which the run's deadline sets, and its agent's
// work keeps no linked machine awake.
func (t *Tasks) Exempt(m engine.Machine) bool {
	ctx, cancel := context.WithTimeout(context.Background(), exemptWait)
	defer cancel()
	return t.Check(ctx, m, task.OpRenew) != nil
}

// ownerStore is the part of tasks.db the Owner reads and writes, opened on first use. A failed
// open fails the call, and the Owner fails closed on it.
type ownerStore struct{ t *Tasks }

func (o ownerStore) Task(ctx context.Context, id task.ID) (task.Task, error) {
	return o.t.Task(ctx, id)
}

func (o ownerStore) RecordFreeze(ctx context.Context, id task.ID, f task.Freeze) error {
	st, err := o.t.store(ctx)
	if err != nil {
		return err
	}
	return st.RecordFreeze(ctx, id, f)
}

// DaemonConfig is what the task daemon runs on.
type DaemonConfig struct {
	// Engine must come from Guard, so the executor's own exec, upload and thaw take the same
	// per-machine lock as every other process's pauses and wakes.
	Engine   engine.Engine
	Machines Machines
	Logf     func(format string, args ...any)
	// Clock and Poll replace the workflow clock and wait interval. Zero values use wall time
	// and the package's Poll interval.
	Clock taskrun.Clock
	Poll  time.Duration
}

// Daemon runs every task's workflow. Only the hibd daemon holding DaemonLock has one.
type Daemon struct {
	sup    *taskrun.Supervisor
	unlock func()
}

// Daemon takes DaemonLock, records a new daemon instance and starts the supervisor, whose
// workflows live as long as life. An intent an earlier instance left open is then a crash, and
// its workflow looks before it acts again. Daemon fails with ErrDaemonRunning while another
// daemon holds the lock.
func (t *Tasks) Daemon(life context.Context, cfg DaemonConfig) (*Daemon, error) {
	owner, ok := cfg.Engine.(*taskmachine.Owner)
	if !ok {
		return nil, errors.New("tasks: the daemon's engine must come from Guard")
	}
	unlock, ok, err := t.dir.TryLock(DaemonLock, "running tasks")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDaemonRunning
	}
	d, err := t.startDaemon(life, cfg, owner)
	if err != nil {
		unlock()
		return nil, err
	}
	d.unlock = unlock
	return d, nil
}

func (t *Tasks) startDaemon(life context.Context, cfg DaemonConfig, owner *taskmachine.Owner) (*Daemon, error) {
	st, err := t.store(life)
	if err != nil {
		return nil, err
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("tasks: making a daemon instance: %w", err)
	}
	inst := task.Instance(hex.EncodeToString(b[:]))
	if err := st.StartDaemon(life, inst, t.now()); err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = taskrun.RealClock{}
	}
	sup := taskrun.NewSupervisor(life, taskrun.Config{Store: st, Logs: taskstore.FileLogs{Dir: t.dir},
		Engine: owner, Machines: cfg.Machines, Clock: clock, Instance: inst,
		Poll: cfg.Poll, Logf: cfg.Logf})
	return &Daemon{sup: sup}, nil
}

// Tick starts, nudges and grants workflows. The daemon calls it every Poll, and never while the
// host is going to sleep.
func (d *Daemon) Tick(ctx context.Context) error { return d.sup.Tick(ctx) }

// Close waits for every workflow to return, which they do once life ends, then lets another
// daemon run tasks.
func (d *Daemon) Close() {
	d.sup.Wait()
	d.unlock()
}
