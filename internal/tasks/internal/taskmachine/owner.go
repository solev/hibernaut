// Package taskmachine owns the lifecycle of task machines in every hibd process. Its Owner
// decorates the engine App.engine() returns, so every pause and every wake hibd makes, by any
// path, passes through the same three guards:
//
//   - freeze before pause: the stage slice is frozen before the machine pauses, and a freeze
//     row says so, so a resumed machine's agent never acts before the daemon thaws it;
//   - the wake guard: a task machine wakes only when its task.Access allows task.OpWake, so no
//     SSH dial, link wake or repin wakes a stage that was paused unfrozen or is past its
//     deadline;
//   - the timeout floor: a task machine's E2B timeout is never set below its run's deadline
//     plus a margin, so E2B's own timeout doesn't pause a stage it shouldn't.
//
// SaveTemplate refuses every task machine, so nothing copies one. Check answers the same
// task.Access table for the hibd paths that pause, remove or set up a machine by hand, and for
// busy. Callers keep calling the engine by sandbox ID and never learn which machines are tasks.
package taskmachine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
)

// Store is what the Owner reads and writes in tasks.db.
type Store interface {
	Task(ctx context.Context, id task.ID) (task.Task, error)
	RecordFreeze(ctx context.Context, id task.ID, f task.Freeze) error
}

// Locks takes the per-machine lock. statefile.Dir's WaitLock is it in hibd.
type Locks interface {
	WaitLock(ctx context.Context, name, what string) (unlock func(), err error)
}

// LockName is the lock that orders every pause, wake, dial and thaw of one task machine. It
// comes after a machine's fence and machines.RoomLock and before the SQLite write lock in the
// lock order.
func LockName(id task.ID) string { return "task-machine-" + string(id) + ".lock" }

var (
	// ErrRefused wraps every op Check and the wake guard refuse. The message names the op and
	// the task, and the error it wraps says why and what to run.
	ErrRefused = errors.New("refused")
	// ErrStopping refuses a thaw of a run that must stop: the daemon kills the stage instead.
	ErrStopping = errors.New("the run is stopping")
)

func refused(op task.Op, tid task.ID, err error) error {
	return fmt.Errorf("%w to %s task machine %s: %w", ErrRefused, op, tid, err)
}

// inner is the wrapped engine. Embedding it under an unexported name promotes the methods
// Owner doesn't define and leaves no exported field to reach around the guards with.
type inner = engine.Engine

// Owner is the engine with the task machine guards. Methods it doesn't define pass through:
// TestEveryMethodIsClassified keeps that list honest as engine.Engine grows.
type Owner struct {
	inner
	store Store
	locks Locks
	now   func() time.Time
	// by names this process in freeze rows, for show.
	by string
}

// New wraps e. by names the process, like "hibd daemon" or "hibd pause".
func New(e engine.Engine, s Store, l Locks, now func() time.Time, by string) *Owner {
	return &Owner{inner: e, store: s, locks: l, now: now, by: by}
}

// taskOf returns the task a machine runs, from its label.
func taskOf(m engine.Machine) (task.ID, bool) {
	id, ok := m.Metadata[task.Label]
	return task.ID(id), ok && id != ""
}

// latest returns the task's latest run with machine m's state filled in. It fails closed: with
// no readable run, every op is refused.
func latest(ctx context.Context, s Store, id task.ID, m engine.Machine) (task.Run, error) {
	t, err := s.Task(ctx, id)
	if err != nil {
		return task.Run{}, fmt.Errorf("reading its task: %w", err)
	}
	if len(t.Runs) == 0 {
		return task.Run{}, errors.New("the task has no runs")
	}
	r := t.Runs[len(t.Runs)-1]
	r.Machine = task.MachineRunning
	if m.State == engine.Paused {
		r.Machine = task.MachinePaused
	}
	return r, nil
}

// Check returns nil if op may touch machine m at now, and otherwise an error wrapping
// ErrRefused. A machine without the task label is no task's, so every op may touch it.
func Check(ctx context.Context, s Store, now time.Time, m engine.Machine, op task.Op) error {
	tid, ok := taskOf(m)
	if !ok {
		return nil
	}
	r, err := latest(ctx, s, tid, m)
	if err != nil {
		return refused(op, tid, err)
	}
	if err := task.AccessOf(r, now).Allows(op); err != nil {
		return refused(op, tid, err)
	}
	return nil
}

// Pause freezes the stage slice of a running task machine, records the freeze, then pauses it.
// It pauses even when the freeze fails: the row then says unfrozen, so the run is held and
// nothing wakes it until rescue or rm. Every pause hibd makes reaches this method, through
// Manager.Pause, pauseall and Wake's pause again, and none is refused here: hib pause and
// leakscan ask Check first, and the daemon's own pauses must always land.
func (o *Owner) Pause(ctx context.Context, id string) error {
	m, err := o.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	tid, ok := taskOf(m)
	if !ok || m.State != engine.Running {
		return o.inner.Pause(ctx, id)
	}
	unlock, err := o.locks.WaitLock(ctx, LockName(tid), "pausing task machine "+string(tid))
	if err != nil {
		return err
	}
	defer unlock()
	// Another process may have paused it while this one waited for the lock.
	if m, err = o.inner.Get(ctx, id); err != nil {
		return err
	}
	if m.State == engine.Running {
		o.freeze(ctx, tid, id)
	}
	return o.inner.Pause(ctx, id)
}

// freeze freezes and records the result. The row says frozen only after the freeze is
// confirmed, so a failure anywhere leaves the run held rather than wakeable.
func (o *Owner) freeze(ctx context.Context, tid task.ID, id string) {
	f := task.Freeze{At: o.now(), By: o.by}
	fctx, cancel := context.WithTimeout(ctx, taskguest.ExecTimeout)
	defer cancel()
	res, err := o.inner.Exec(fctx, id, root(taskguest.Freeze()))
	switch {
	case err != nil:
		f.Detail = "freeze failed: " + err.Error()
	case res.ExitCode != 0:
		f.Detail = fmt.Sprintf("freeze exited %d: %s", res.ExitCode, res.Stderr)
	default:
		f.Frozen = true
	}
	// A lost row reads as the row before it. Thaw writes unfrozen before it thaws, so that row
	// never says frozen while the stage runs.
	_ = o.store.RecordFreeze(context.WithoutCancel(ctx), tid, f)
}

// Thaw lets a running task machine's stage run again. Only the daemon's executor calls it, and
// only once its run is past every check DecidePaused makes. It records unfrozen first.
func (o *Owner) Thaw(ctx context.Context, tid task.ID, id string) error {
	unlock, err := o.locks.WaitLock(ctx, LockName(tid), "thawing task machine "+string(tid))
	if err != nil {
		return err
	}
	defer unlock()
	m, err := o.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.State != engine.Running {
		return fmt.Errorf("thaw %s: %w", tid, engine.ErrNotRunning)
	}
	// The run may have passed its deadline or been cancelled since the executor looked.
	r, err := latest(ctx, o.store, tid, m)
	if err != nil {
		return fmt.Errorf("thaw %s: %w", tid, err)
	}
	if _, stopping := r.Stop(); stopping {
		return fmt.Errorf("thaw %s: %w", tid, ErrStopping)
	}
	if _, must := task.StopFor(r, o.now()); must {
		return fmt.Errorf("thaw %s: %w", tid, ErrStopping)
	}
	if err := o.store.RecordFreeze(ctx, tid, task.Freeze{At: o.now(), By: o.by}); err != nil {
		return err
	}
	return execOK(ctx, o.inner, id, taskguest.Thaw())
}

// Resume wakes a paused task machine only if its task.Access allows a wake, and with its
// timeout raised to the run's floor. Wake, repin and the daemon itself all resume through it,
// so none of them can wake a held or overdue stage.
func (o *Owner) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	m, err := o.inner.Get(ctx, id)
	if err != nil {
		return engine.Machine{}, err
	}
	tid, ok := taskOf(m)
	if !ok {
		return o.inner.Resume(ctx, id, timeout)
	}
	unlock, err := o.locks.WaitLock(ctx, LockName(tid), "waking task machine "+string(tid))
	if err != nil {
		return engine.Machine{}, err
	}
	defer unlock()
	// The state read before the lock is stale: a pause or a wake may have landed since.
	if m, err = o.inner.Get(ctx, id); err != nil {
		return engine.Machine{}, err
	}
	timeout, err = o.guard(ctx, tid, m, timeout)
	if err != nil {
		return engine.Machine{}, err
	}
	return o.inner.Resume(ctx, id, timeout)
}

// DialWebSocket refuses to wake a paused task machine the guard holds, since E2B's dial wakes
// a paused machine itself. Admitted SSH wakes dial through it. Running SSH uses
// DialRunningWebSocket with the same task access guard.
func (o *Owner) DialWebSocket(ctx context.Context, id string, port int, timeout time.Duration) (net.Conn, error) {
	return o.dial(ctx, id, timeout, func(timeout time.Duration) (net.Conn, error) {
		return o.inner.DialWebSocket(ctx, id, port, timeout)
	})
}

// DialRunningWebSocket keeps SSH's task access guard without permitting a resume.
func (o *Owner) DialRunningWebSocket(ctx context.Context, id string, port int) (net.Conn, error) {
	return o.dial(ctx, id, 0, func(time.Duration) (net.Conn, error) {
		return o.inner.DialRunningWebSocket(ctx, id, port)
	})
}

func (o *Owner) dial(ctx context.Context, id string, timeout time.Duration, dial func(time.Duration) (net.Conn, error)) (net.Conn, error) {
	m, err := o.inner.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	tid, ok := taskOf(m)
	if !ok {
		return dial(timeout)
	}
	unlock, err := o.locks.WaitLock(ctx, LockName(tid), "dialing task machine "+string(tid))
	if err != nil {
		return nil, err
	}
	defer unlock()
	// The state read before the lock is stale: a pause or a wake may have landed since.
	if m, err = o.inner.Get(ctx, id); err != nil {
		return nil, err
	}
	if timeout, err = o.guard(ctx, tid, m, timeout); err != nil {
		return nil, err
	}
	return dial(timeout)
}

// SetTimeout never sets a task machine's timeout below its run's floor, whoever asks: an SSH
// session's keepalive ends, a link idles out, busy renews a kept machine.
func (o *Owner) SetTimeout(ctx context.Context, id string, timeout time.Duration) error {
	m, err := o.inner.Get(ctx, id)
	if err != nil {
		return err
	}
	if tid, ok := taskOf(m); ok {
		if r, err := latest(ctx, o.store, tid, m); err == nil && !r.Ended() {
			timeout = max(timeout, task.MachineTimeout(r, o.now()))
		}
	}
	return o.inner.SetTimeout(ctx, id, timeout)
}

// SaveTemplate refuses every task machine, so no save or fork copies one. machines refuses
// them by label before it pauses anything, and this holds for any path that reaches the engine.
func (o *Owner) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	m, err := o.inner.Get(ctx, id)
	if err != nil {
		return engine.TemplateInfo{}, err
	}
	if err := Check(ctx, o.store, o.now(), m, task.OpCopy); err != nil {
		return engine.TemplateInfo{}, err
	}
	return o.inner.SaveTemplate(ctx, id, alias)
}

// guard refuses a wake task.AccessOf doesn't allow, and returns the timeout to wake m with.
func (o *Owner) guard(ctx context.Context, tid task.ID, m engine.Machine, timeout time.Duration) (time.Duration, error) {
	r, err := latest(ctx, o.store, tid, m)
	if err != nil {
		return 0, refused(task.OpWake, tid, err)
	}
	now := o.now()
	if err := task.AccessOf(r, now).Allows(task.OpWake); err != nil {
		return 0, refused(task.OpWake, tid, err)
	}
	if !r.Ended() {
		timeout = max(timeout, task.MachineTimeout(r, now))
	}
	return timeout, nil
}

// root makes an ExecRequest that runs argv as root.
func root(argv []string) engine.ExecRequest {
	return engine.ExecRequest{Cmd: argv[0], Args: argv[1:], User: "root"}
}

// execOK runs argv as root, bounded by taskguest.ExecTimeout, and wants exit 0.
func execOK(ctx context.Context, e engine.Engine, id string, argv []string) error {
	ctx, cancel := context.WithTimeout(ctx, taskguest.ExecTimeout)
	defer cancel()
	res, err := e.Exec(ctx, id, root(argv))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s exited %d: %s", argv[0], res.ExitCode, res.Stderr)
	}
	return nil
}

var _ engine.Engine = (*Owner)(nil)
