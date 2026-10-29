// Package taskrun carries out runs in the daemon. Each run is one straight-line workflow, the
// executor's Run, which reads top to bottom as the steps in task.Steps: machine, upload, setup,
// command, finish, then stop if one was recorded, collect and keep. Every step records its
// intent in tasks.db before its outside action and its result after, so a run re-enters its
// workflow from the log after any crash, restart or nudge, and a step whose intent is recorded
// looks before it acts.
//
// One goroutine runs each unfinished run, so a run has at most one outside action in flight.
// The Supervisor starts them, nudges them when a request arrives and grants machines one at a
// time.
package taskrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

// DefaultPoll is how often a waiting step looks again.
const DefaultPoll = 5 * time.Second

// Config is everything the workflows touch.
type Config struct {
	Store taskstore.Store
	Logs  taskstore.Logs
	// Engine is taskmachine's Owner in hibd, so the executor's own Exec, Upload and thaw obey
	// the same lock as every other process's pauses and wakes.
	Engine Engine
	// Machines creates, resumes, pauses and removes task machines. In hibd it is Manager behind
	// the links fence.
	Machines Machines
	Clock    Clock
	// Instance is this daemon's instance, written into every checkpoint it appends.
	Instance task.Instance
	// Poll is how often a waiting step looks again. Zero means DefaultPoll.
	Poll time.Duration
	Logf func(format string, args ...any)
}

// Engine is what the executor calls in a running machine. It can't pause or resume: those go
// through Machines, which admits and fences.
type Engine interface {
	Get(ctx context.Context, id string) (engine.Machine, error)
	Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error)
	Upload(ctx context.Context, id, user string, files []engine.File) error
	// Thaw lets a running task machine's stage run again, recording it first.
	Thaw(ctx context.Context, tid task.ID, id string) error
}

// Machines is the machine lifecycle the executor asks for. Create and Resume admit under
// machines.RoomLock and wrap room.ErrFull when there is no room. They refuse while the host is going to
// sleep, so no workflow wakes a machine the shutdown pause-all just paused. Pause and Remove
// fence the machine and close its links first, and the engine underneath freezes before it
// pauses.
type Machines interface {
	// Fits returns the admission check on one reading of the host, for task.Pick.
	Fits(ctx context.Context) (func(task.Run) error, error)
	// Find returns the task's machine by name and label, or found false.
	Find(ctx context.Context, id task.ID) (m engine.Machine, found bool, err error)
	Create(ctx context.Context, r task.Run, timeout time.Duration) (engine.Machine, error)
	Resume(ctx context.Context, r task.Run, sandbox string, timeout time.Duration) (engine.Machine, error)
	Pause(ctx context.Context, id task.ID) error
	Remove(ctx context.Context, id task.ID) error
}

// Clock is the daemon's time. Tests share one fake clock with Manager and the fake engine.
type Clock interface {
	Now() time.Time
	// Sleep returns nil after d, or ctx's cause once ctx is done.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the wall clock.
type RealClock struct{}

// Now returns time.Now.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep sleeps for d or until ctx is done.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// errNudged is the cause of a workflow cancelled because a request arrived. The workflow
// re-enters from the log at once and finds the stop.
var errNudged = errors.New("a request arrived")

// executor runs one run's workflow. It writes the run's log, and so does the Supervisor's grant.
// The store's sequence check makes every append a compare and swap: the loser gets
// taskstore.ErrStale, rereads and re-enters, and nothing else happens.
type executor struct {
	cfg Config
	id  task.RunID
	// r is the run as of the last refresh, with the machine's state from E2B.
	r task.Run
}

// Run is the run's whole workflow, and the order of its calls is the order of task.Steps.
// It returns nil once the keep step's result is recorded. Any other return means "quit and
// re-enter later": every step is safe to run again.
func (x *executor) Run(ctx context.Context) error {
	if err := x.refresh(ctx); err != nil {
		return err
	}
	if x.r.Ended() {
		return nil
	}
	var stop *task.StopIntent
	if err := x.main(ctx); err != nil && !errors.As(err, &stop) {
		return err
	}
	if err := x.stop(ctx); err != nil {
		return err
	}
	if err := x.collect(ctx); err != nil {
		return err
	}
	return x.keep(ctx)
}

// main is the run's main line. It returns nil when the stages are done or one failed, and a
// *task.StopIntent when a stop ends it early.
func (x *executor) main(ctx context.Context) error {
	if s, ok := x.r.Stop(); ok {
		return s
	}
	if err := x.machine(ctx); err != nil {
		return err
	}
	if err := x.upload(ctx); err != nil {
		return err
	}
	for _, s := range task.Stages() {
		ok, err := x.stage(ctx, s)
		if err != nil || !ok {
			return err
		}
	}
	return nil
}

// machine leaves the task's machine running: a first run creates it, a follow-up resumes the
// one the last run kept. A queued run waits until the Supervisor records its machine intent,
// the grant. The intent comes before E2B is asked, and the machine's name is the task ID, so
// after a crash between E2B's create and the result the machine is found by name and adopted,
// never created twice. E2B stays the catalog: the result records only its ID. A refusal sends
// the run back to the queue.
func (x *executor) machine(ctx context.Context) error {
	for {
		last, ok := x.last(task.StepMachine)
		switch {
		case ok && last.Kind() == task.Intent:
			res, err := x.makeMachine(ctx)
			if err != nil {
				return err
			}
			if err := x.append(ctx, res); err != nil {
				return err
			}
		case ok && last.Body.(*task.MachineResult).Refused == "":
			return nil
		default:
			if err := x.boundary(ctx); err != nil {
				return err
			}
			if x.r.Queued() {
				if err := x.wait(ctx); err != nil {
					return err
				}
			}
		}
	}
}

func (x *executor) makeMachine(ctx context.Context) (task.Body, error) {
	timeout := task.MachineTimeout(x.r, x.now())
	m, found, err := x.cfg.Machines.Find(ctx, x.id.Task)
	if err != nil {
		return nil, err
	}
	// TODO: when the open intent is another instance's and younger than the engine's create
	// timeout, and Find found nothing, wait a poll instead of creating: a create the crashed
	// daemon sent may still land at E2B. FindTask settles the rare duplicate that slips past.
	adopted := found && m.ID != x.r.Kept
	switch {
	case found && m.State == engine.Running:
	case found:
		m, err = x.cfg.Machines.Resume(ctx, x.r, m.ID, timeout)
	case x.r.Kept != "":
		return x.stopNow(ctx, task.StopIntent{Outcome: task.Lost,
			Detail: "E2B no longer has the machine the last run kept"})
	default:
		m, err = x.cfg.Machines.Create(ctx, x.r, timeout)
	}
	if errors.Is(err, room.ErrFull) {
		return &task.MachineResult{Refused: err.Error()}, nil
	}
	if err != nil {
		return nil, err
	}
	return &task.MachineResult{ID: m.ID, Adopted: adopted}, nil
}

// upload writes the runner, the inputs and their manifest into the machine, then checks every
// hash inside it. Upload overwrites, so after a lost reply or a crash it simply runs again.
func (x *executor) upload(ctx context.Context) error {
	if x.done(task.StepUpload) {
		return nil
	}
	if !x.open(task.StepUpload) {
		if err := x.boundary(ctx); err != nil {
			return err
		}
		if err := x.append(ctx, &task.UploadIntent{}); err != nil {
			return err
		}
	}
	id := x.r.MachineID()
	files := taskguest.Uploads(x.r, x.cfg.Store.Blob)
	if err := x.cfg.Engine.Upload(ctx, id, "root", files); err != nil {
		return err
	}
	if err := x.execOK(ctx, taskguest.Verify(x.id)); err != nil {
		return err
	}
	return x.append(ctx, &task.UploadResult{})
}

// stage runs stage s to its exit and reports whether it succeeded. A skipped stage succeeds.
func (x *executor) stage(ctx context.Context, s task.Stage) (bool, error) {
	if s.Step().Skipped(x.id, x.r.Spec) {
		return true, nil
	}
	if e, ok := x.exitOf(s); ok {
		return e.OK(), nil
	}
	if !x.launched(s) {
		if err := x.boundary(ctx); err != nil {
			return false, err
		}
		if !x.open(s.Step()) {
			if err := x.append(ctx, &task.StageIntent{Stage: s}); err != nil {
				return false, err
			}
		}
		if err := x.launch(ctx, s); err != nil {
			return false, err
		}
	}
	e, err := x.await(ctx, s)
	if err != nil {
		return false, err
	}
	if err := x.append(ctx, &task.StageExit{Stage: s, Exit: e}); err != nil {
		return false, err
	}
	return e.OK(), nil
}

// launch starts stage s's unit unless a look shows it started, then records the launch. The
// unit's name and its start marker make a second launch harmless, and a lost reply is judged
// by the look after it, never by the reply.
func (x *executor) launch(ctx context.Context, s task.Stage) error {
	seen, err := x.look(ctx, s)
	if err != nil {
		return err
	}
	adopted := seen.Verdict() != taskguest.NotStarted
	if !adopted {
		argv := taskguest.Launch(x.r, s, task.RuntimeMax(x.r, x.now()))
		if _, err := x.exec(ctx, argv, taskguest.ExecTimeout); err != nil &&
			!errors.Is(err, engine.ErrConnectionLost) {
			return err
		}
		if seen, err = x.look(ctx, s); err != nil {
			return err
		}
		if seen.Verdict() == taskguest.NotStarted {
			return fmt.Errorf("stage %s of %s didn't start", s, x.id)
			// TODO: systemd-run --no-block returns before the unit starts. Count looks that
			// find it not started after a launch that exited 0, and after a few record a
			// StageExit with Result "launch-failed" rather than launching until the deadline.
		}
	}
	return x.append(ctx, &task.StageLaunched{Stage: s, Adopted: adopted})
}

// await waits for stage s's exit, copying its log as it grows. A paused machine is handled by
// task.DecidePaused and a frozen stage on a running machine is thawed. It returns a
// *task.StopIntent when a stop comes first: the stop step then ends the stage.
func (x *executor) await(ctx context.Context, s task.Stage) (task.ExitRecord, error) {
	for {
		if err := x.boundary(ctx); err != nil {
			return task.ExitRecord{}, err
		}
		if x.r.Machine == task.MachinePaused {
			if err := x.paused(ctx); err != nil {
				return task.ExitRecord{}, err
			}
		} else if e, done, err := x.lookAndCopy(ctx, s); done || err != nil {
			return e, err
		}
		if err := x.wait(ctx); err != nil {
			return task.ExitRecord{}, err
		}
	}
}

// lookAndCopy looks at a running stage once: it thaws a frozen one, copies new log bytes and
// reports the exit if there is one. A paused machine or a lost reply means look again later.
func (x *executor) lookAndCopy(ctx context.Context, s task.Stage) (task.ExitRecord, bool, error) {
	seen, err := x.look(ctx, s)
	if errors.Is(err, engine.ErrNotRunning) || errors.Is(err, engine.ErrConnectionLost) {
		return task.ExitRecord{}, false, nil
	}
	if err != nil {
		return task.ExitRecord{}, false, err
	}
	if seen.Frozen() {
		if err := x.cfg.Engine.Thaw(ctx, x.id.Task, x.r.MachineID()); err != nil &&
			!errors.Is(err, engine.ErrNotRunning) {
			return task.ExitRecord{}, false, err
		}
	}
	if err := x.copyLog(ctx, s, seen.LogSize); err != nil {
		return task.ExitRecord{}, false, err
	}
	switch seen.Verdict() {
	case taskguest.Exited:
		return *seen.Exit, true, nil
	case taskguest.Vanished, taskguest.NotStarted:
		return task.ExitRecord{Vanished: true}, true, nil
	}
	return task.ExitRecord{}, false, nil
}

// paused acts on a paused machine while its stage is live and no stop is due. Only the host
// pauses a live run's machine, freezing its stage first, so a frozen one is resumed at once. A
// machine paused without the freeze is held.
func (x *executor) paused(ctx context.Context) error {
	if task.DecidePaused(x.r) != task.ResumeAndThaw {
		return nil
	}
	_, err := x.cfg.Machines.Resume(ctx, x.r, x.r.MachineID(), task.MachineTimeout(x.r, x.now()))
	if errors.Is(err, room.ErrFull) {
		return nil
	}
	return err
}

// stop ends the live stage after a recorded stop. A frozen stage is killed, since it can't act
// on TERM until thawed, and so is a rescued one. Otherwise the unit gets TERM, then KILL after
// task.StopGrace. A stage held paused without the freeze waits for rescue or rm: hibd never
// wakes it on its own.
func (x *executor) stop(ctx context.Context) error {
	if _, ok := x.r.Stop(); !ok || x.done(task.StepStop) {
		return nil
	}
	for {
		if err := x.refresh(ctx); err != nil {
			return err
		}
		how, err := x.end(ctx)
		if err != nil {
			return err
		}
		if how != "" {
			return x.append(ctx, &task.StopResult{How: how})
		}
		if err := x.wait(ctx); err != nil {
			return err
		}
	}
}

// end tries once to end the live stage. It returns how the stage ended, or "" to try again
// after a poll.
func (x *executor) end(ctx context.Context) (string, error) {
	s, live := x.r.LiveStage()
	switch {
	case !live:
		return "not-started", nil
	case x.r.Machine == task.MachineGone:
		return "gone", nil
	case x.r.Machine == task.MachinePaused:
		switch task.DecidePaused(x.r) {
		case task.StopPaused:
			return "removed-paused", nil
		case task.ResumeToStop:
			_, err := x.cfg.Machines.Resume(ctx, x.r, x.r.MachineID(),
				task.MachineTimeout(x.r, x.now()))
			if errors.Is(err, room.ErrFull) {
				return "", nil
			}
			return "", err
		}
		return "", nil
	}
	seen, err := x.look(ctx, s)
	if err != nil {
		return "", lookAgain(err)
	}
	how, argv := "term", taskguest.Stop(x.id, s)
	if seen.Frozen() || x.r.Rescued() {
		how, argv = "kill", taskguest.Kill(x.id, s)
	}
	// Stopping a unit that never started cancels a start still queued, so this is sent
	// whatever the look said.
	if _, err := x.exec(ctx, argv, task.StopGrace+taskguest.ExecTimeout); err != nil {
		return "", lookAgain(err)
	}
	if seen, err = x.look(ctx, s); err != nil {
		return "", lookAgain(err)
	}
	switch seen.Verdict() {
	case taskguest.Exited:
		return how, x.copyLog(ctx, s, seen.LogSize)
	case taskguest.NotStarted:
		return "not-started", nil
	case taskguest.Vanished:
		return "vanished", nil
	}
	return "", nil
}

// lookAgain turns the errors a poll recovers from into nil.
func lookAgain(err error) error {
	if errors.Is(err, engine.ErrNotRunning) || errors.Is(err, engine.ErrConnectionLost) {
		return nil
	}
	return err
}

// collect copies what the host doesn't have of every stage's log, within task.MaxLogBytes, and
// reads what finish published. A machine that is paused or gone gives what the host already
// copied: collect never wakes one.
func (x *executor) collect(ctx context.Context) error {
	if x.done(task.StepCollect) {
		return nil
	}
	if !x.open(task.StepCollect) {
		if err := x.append(ctx, &task.CollectIntent{}); err != nil {
			return err
		}
	}
	if err := x.refresh(ctx); err != nil {
		return err
	}
	res := &task.CollectResult{}
	for _, s := range task.Stages() {
		if !x.launched(s) {
			continue
		}
		if x.r.Machine == task.MachineRunning {
			seen, err := x.look(ctx, s)
			if err != nil {
				return err
			}
			if err := x.copyLog(ctx, s, seen.LogSize); err != nil {
				return err
			}
			res.Dropped += max(seen.LogSize-task.MaxLogBytes, 0)
		}
		n, err := x.cfg.Logs.Size(x.id, s)
		if err != nil {
			return err
		}
		res.LogBytes += n
	}
	if e, ok := x.exitOf(task.StageFinish); ok && e.OK() && x.r.Machine == task.MachineRunning {
		p, err := x.publication(ctx)
		if err != nil {
			return err
		}
		res.Publication = p
	}
	return x.append(ctx, res)
}

// publication reads what finish published. A finish that published nothing has no file, which
// reads as no publication and so no_changes.
func (x *executor) publication(ctx context.Context) (*task.Publication, error) {
	res, err := x.exec(ctx, taskguest.PublicationOf(x.id), taskguest.ExecTimeout)
	if err != nil || res.ExitCode != 0 {
		return nil, err
	}
	p, err := taskguest.ParsePublication(res.Stdout)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// keep pauses the machine for follow-ups and hib ssh, or removes it after an rm. The pause goes
// through Machines and the Owner, so the stage slice is frozen first: even a stage that outlived
// its stop stays still.
func (x *executor) keep(ctx context.Context) error {
	if !x.open(task.StepKeep) {
		if err := x.append(ctx, &task.KeepIntent{}); err != nil {
			return err
		}
	}
	if err := x.refresh(ctx); err != nil {
		return err
	}
	res := &task.KeepResult{}
	switch {
	case x.r.MachineID() == "" || x.r.Machine == task.MachineGone:
		res.Gone = true
	case x.r.Removing():
		if err := x.cfg.Machines.Remove(ctx, x.id.Task); err != nil {
			return err
		}
		res.Removed = true
	case x.r.Machine == task.MachineRunning:
		if err := x.cfg.Machines.Pause(ctx, x.id.Task); err != nil &&
			!errors.Is(err, engine.ErrNotRunning) {
			return err
		}
	}
	return x.append(ctx, res)
}

// boundary is the check between steps and on every poll: refresh, then record and return the
// stop task.StopFor says is due, if any.
func (x *executor) boundary(ctx context.Context) error {
	if err := x.refresh(ctx); err != nil {
		return err
	}
	if s, ok := x.r.Stop(); ok {
		return s
	}
	if s, ok := task.StopFor(x.r, x.now()); ok {
		_, err := x.stopNow(ctx, s)
		return err
	}
	return nil
}

// stopNow records s and returns it as the error that ends the main line.
func (x *executor) stopNow(ctx context.Context, s task.StopIntent) (task.Body, error) {
	if err := x.append(ctx, &s); err != nil {
		return nil, err
	}
	return nil, &s
}

// refresh rereads the run and asks E2B about its machine.
func (x *executor) refresh(ctx context.Context) error {
	r, err := x.cfg.Store.Run(ctx, x.id)
	if err != nil {
		return err
	}
	if id := r.MachineID(); id != "" {
		m, err := x.cfg.Engine.Get(ctx, id)
		switch {
		case errors.Is(err, engine.ErrNotFound):
			r.Machine = task.MachineGone
		case err != nil:
			return err
		case m.State == engine.Paused:
			r.Machine = task.MachinePaused
		default:
			r.Machine = task.MachineRunning
		}
	}
	x.r = r
	return nil
}

// append records b as the run's next checkpoint.
func (x *executor) append(ctx context.Context, b task.Body) error {
	c := task.Checkpoint{Run: x.id, Seq: len(x.r.Log) + 1, At: x.now(), By: x.cfg.Instance, Body: b}
	if err := x.cfg.Store.Append(ctx, c); err != nil {
		return err
	}
	x.r.Log = append(x.r.Log, c)
	return nil
}

// last returns the latest checkpoint of step.
func (x *executor) last(step task.Step) (task.Checkpoint, bool) {
	for i := len(x.r.Log) - 1; i >= 0; i-- {
		if x.r.Log[i].Step() == step {
			return x.r.Log[i], true
		}
	}
	return task.Checkpoint{}, false
}

// open reports whether step's intent is recorded.
func (x *executor) open(step task.Step) bool {
	_, ok := x.last(step)
	return ok
}

// done reports whether step's result is recorded. For a stage that is its exit.
func (x *executor) done(step task.Step) bool {
	c, ok := x.last(step)
	if step.Stage() != "" {
		return ok && c.Kind() == task.Exit
	}
	return ok && c.Kind() == task.Result
}

// launched reports whether stage s's launch is recorded.
func (x *executor) launched(s task.Stage) bool {
	c, ok := x.last(s.Step())
	return ok && c.Kind() != task.Intent
}

func (x *executor) exitOf(s task.Stage) (task.ExitRecord, bool) {
	c, ok := x.last(s.Step())
	if !ok || c.Kind() != task.Exit {
		return task.ExitRecord{}, false
	}
	return c.Body.(*task.StageExit).Exit, true
}

func (x *executor) look(ctx context.Context, s task.Stage) (taskguest.Seen, error) {
	out, err := x.execOut(ctx, taskguest.Look(x.id, s))
	if err != nil {
		return taskguest.Seen{}, err
	}
	return taskguest.ParseLook(out)
}

// copyLog appends stage s's log to the host's copy, from the host copy's length up to size
// and task.MaxLogBytes, in chunks of taskguest.MaxLogChunk. The host file's length is the only
// cursor, so a chunk whose reply was lost is read again and never appended twice.
func (x *executor) copyLog(ctx context.Context, s task.Stage, size int64) error {
	for {
		have, err := x.cfg.Logs.Size(x.id, s)
		if err != nil || have >= min(size, task.MaxLogBytes) {
			return err
		}
		out, err := x.execOut(ctx, taskguest.ReadLog(x.id, s, have, task.MaxLogBytes-have))
		if err != nil {
			return lookAgain(err)
		}
		_, data, err := taskguest.ParseLog(out)
		if err != nil || len(data) == 0 {
			return err
		}
		if err := x.cfg.Logs.Append(x.id, s, have, data); err != nil {
			return err
		}
	}
}

func (x *executor) exec(ctx context.Context, argv []string, timeout time.Duration) (engine.ExecResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return x.cfg.Engine.Exec(ctx, x.r.MachineID(), engine.ExecRequest{Cmd: argv[0],
		Args: argv[1:], User: "root"})
}

// execOut runs argv and wants exit 0 and its stdout.
func (x *executor) execOut(ctx context.Context, argv []string) ([]byte, error) {
	res, err := x.exec(ctx, argv, taskguest.ExecTimeout)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%s exited %d: %s", argv[0], res.ExitCode, res.Stderr)
	}
	return res.Stdout, nil
}

func (x *executor) execOK(ctx context.Context, argv []string) error {
	_, err := x.execOut(ctx, argv)
	return err
}

func (x *executor) wait(ctx context.Context) error {
	return x.cfg.Clock.Sleep(ctx, x.poll())
}

func (x *executor) now() time.Time { return x.cfg.Clock.Now() }

func (x *executor) poll() time.Duration {
	if x.cfg.Poll > 0 {
		return x.cfg.Poll
	}
	return DefaultPoll
}

// Supervisor runs every unfinished run's workflow in the daemon.
type Supervisor struct {
	cfg  Config
	life context.Context

	mu   sync.Mutex
	runs map[task.RunID]*handle
	wg   sync.WaitGroup
}

type handle struct {
	x *executor
	// cancel cancels the workflow's current pass with a cause.
	cancel context.CancelCauseFunc
	// requests is how many requests the workflow has been nudged for.
	requests int
}

// NewSupervisor returns a Supervisor whose workflows live as long as life.
func NewSupervisor(life context.Context, cfg Config) *Supervisor {
	return &Supervisor{cfg: cfg, life: life, runs: map[task.RunID]*handle{}}
}

// Tick starts a workflow for each unfinished run that has none, nudges each whose run has a new
// request and grants the next machine. The daemon calls it every Poll, and never while the host
// is going to sleep.
//
// At most one run holds the grant at a time, and the log says which: the run whose last
// checkpoint is a machine intent. Nothing about the queue lives only in memory, so a restarted
// daemon neither loses a grant nor gives two.
func (s *Supervisor) Tick(ctx context.Context) error {
	runs, err := s.cfg.Store.Unfinished(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, r := range runs {
		h := s.runs[r.ID]
		if h == nil {
			h = s.start(r)
		}
		if len(r.Requests) > h.requests {
			h.requests = len(r.Requests)
			h.cancel(errNudged)
		}
	}
	s.mu.Unlock()
	if granted(runs) {
		return nil
	}
	fits, err := s.cfg.Machines.Fits(ctx)
	if err != nil {
		return err
	}
	id, ok := task.Pick(runs, s.cfg.Clock.Now(), fits)
	if !ok {
		return nil
	}
	return s.grant(ctx, runs, id)
	// TODO: retention and rm of tasks with no live run: for each task.Retire(t, now), record
	// the Removal, Machines.Remove, then record it done. An open removal is carried out again.
}

// granted reports whether a run holds the grant: its machine intent is its last checkpoint.
func granted(runs []task.Run) bool {
	for _, r := range runs {
		if c, ok := r.Last(); ok && c.Step() == task.StepMachine && c.Kind() == task.Intent {
			return true
		}
	}
	return false
}

// grant records run id's machine intent, then nudges its workflow to act on it. If the workflow
// appended first, say a stop, the grant loses with ErrStale and the next Tick picks again.
func (s *Supervisor) grant(ctx context.Context, runs []task.Run, id task.RunID) error {
	i := slices.IndexFunc(runs, func(r task.Run) bool { return r.ID == id })
	c := task.Checkpoint{Run: id, Seq: len(runs[i].Log) + 1, At: s.cfg.Clock.Now(),
		By: s.cfg.Instance, Body: &task.MachineIntent{}}
	if err := s.cfg.Store.Append(ctx, c); err != nil {
		if errors.Is(err, taskstore.ErrStale) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.runs[id]; h != nil {
		h.cancel(errNudged)
	}
	return nil
}

// start runs r's workflow until it ends or the daemon stops. A pass that fails re-enters
// after a poll and a nudged pass at once, both from the log. Called with s.mu held.
func (s *Supervisor) start(r task.Run) *handle {
	h := &handle{
		x: &executor{cfg: s.cfg, id: r.ID},
		// Requests made before the workflow started need no nudge: its first pass reads them.
		requests: len(r.Requests),
		cancel:   func(error) {},
	}
	s.runs[r.ID] = h
	s.wg.Go(func() {
		defer func() {
			s.mu.Lock()
			delete(s.runs, r.ID)
			s.mu.Unlock()
		}()
		for s.life.Err() == nil {
			ctx, cancel := context.WithCancelCause(s.life)
			s.mu.Lock()
			h.cancel = cancel
			s.mu.Unlock()
			err := h.x.Run(ctx)
			nudged := errors.Is(context.Cause(ctx), errNudged)
			cancel(nil)
			switch {
			case err == nil:
				return
			case nudged, errors.Is(err, taskstore.ErrStale):
				continue
			}
			if s.cfg.Logf != nil && s.life.Err() == nil {
				s.cfg.Logf("task %s: %v", r.ID, err)
			}
			_ = s.cfg.Clock.Sleep(s.life, h.x.poll())
		}
	})
	return h
}

// Wait waits for every workflow to return, after the daemon's life ends.
func (s *Supervisor) Wait() { s.wg.Wait() }
