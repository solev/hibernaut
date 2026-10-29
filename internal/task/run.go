package task

import (
	"errors"
	"slices"
	"time"
)

// RequestKind is what a user asked a run or task to do. Requests are rows a CLI one-shot
// writes. The daemon carries them out, and only the log says whether it has.
type RequestKind string

const (
	// RequestCancel ends the run as cancelled.
	RequestCancel RequestKind = "cancel"
	// RequestRescue is consent to resume a machine paused without the freeze, kill its stage at
	// once and keep it. It also ends the run.
	RequestRescue RequestKind = "rescue"
	// RequestRemove ends the run as cancelled if one is live, then removes the machine and the
	// task.
	RequestRemove RequestKind = "remove"
)

// Valid reports whether k is a kind of request task defines.
func (k RequestKind) Valid() bool {
	switch k {
	case RequestCancel, RequestRescue, RequestRemove:
		return true
	}
	return false
}

// Request is one request row.
type Request struct {
	ID   int64
	Task ID
	// Run is the run that was live when the request was made. A request never reaches a later
	// run. It is 0 for a remove made with no run live.
	Run   int
	Kind  RequestKind
	At    time.Time
	Actor Actor
}

// Freeze is the latest freeze row of a task machine. taskmachine writes one, in whichever
// process pauses or thaws the machine, before it pauses and after it thaws. Rows are per actor:
// no two writers share a row, and the reader takes the latest.
type Freeze struct {
	At time.Time
	// Frozen is true when the stage slice was frozen. It is false after a thaw, and after a
	// freeze that failed, in which case Detail says why.
	Frozen bool
	Detail string
	// By names the process that wrote the row, for show.
	By string
}

// MachineState is what E2B last said about the run's machine. The reader fills it in, so
// derivations never call E2B themselves.
type MachineState string

const (
	MachineUnknown MachineState = ""
	MachineRunning MachineState = "running"
	MachinePaused  MachineState = "paused"
	MachineGone    MachineState = "gone"
)

// Run is everything known about one run, merged at read time from the submission, the run's
// log, the requests made against it, the machine's latest freeze row and what E2B says. It is a
// value: derive from it, never store what it derives.
type Run struct {
	ID        RunID
	Spec      Spec
	Actor     Actor
	Submitted time.Time
	Log       []Checkpoint
	Requests  []Request
	Freeze    Freeze
	Machine   MachineState
	// Kept is the sandbox ID of the machine an earlier run of the task made, or "" on a first
	// run.
	Kept string
}

// Last returns the run's latest checkpoint.
func (r Run) Last() (Checkpoint, bool) {
	if len(r.Log) == 0 {
		return Checkpoint{}, false
	}
	return r.Log[len(r.Log)-1], true
}

// Ended reports whether the keep step's result is recorded.
func (r Run) Ended() bool {
	c, ok := r.Last()
	return ok && c.Step() == StepKeep && c.Kind() == Result
}

// Stop returns the stop that ends the run early, if one is recorded.
func (r Run) Stop() (*StopIntent, bool) {
	for _, c := range r.Log {
		if s, ok := c.Body.(*StopIntent); ok {
			return s, true
		}
	}
	return nil, false
}

// MachineID returns the sandbox the run works in: its own machine result, or the kept machine.
func (r Run) MachineID() string {
	for i := len(r.Log) - 1; i >= 0; i-- {
		if m, ok := r.Log[i].Body.(*MachineResult); ok && m.Refused == "" {
			return m.ID
		}
	}
	return r.Kept
}

// machineIntent returns the latest machine intent and whether a refusal closed it.
func (r Run) machineIntent() (Checkpoint, bool) {
	for i := len(r.Log) - 1; i >= 0; i-- {
		if _, ok := r.Log[i].Body.(*MachineIntent); ok {
			refused := false
			if i+1 < len(r.Log) {
				m, ok := r.Log[i+1].Body.(*MachineResult)
				refused = ok && m.Refused != ""
			}
			return r.Log[i], !refused
		}
	}
	return Checkpoint{}, false
}

// Queued reports whether the run waits for room: no machine intent yet, or the last was refused.
func (r Run) Queued() bool {
	if _, ok := r.Stop(); ok {
		return false
	}
	_, ok := r.machineIntent()
	return !ok
}

// Deadline is when the run times out: its machine intent plus Spec.Timeout. The clock starts
// when the run holds the grant, so waiting in the queue doesn't count and every pause after
// does. It is false while the run is queued.
func (r Run) Deadline() (time.Time, bool) {
	c, ok := r.machineIntent()
	if !ok {
		return time.Time{}, false
	}
	return c.At.Add(r.Spec.Timeout), true
}

// QueueLimit is when a queued run expires.
func (r Run) QueueLimit() time.Time { return r.Submitted.Add(MaxQueued) }

// LiveStage returns the stage whose unit may be running: launched, or launching, with no exit
// recorded. A stop doesn't end it: the stage is live until the stop step's result.
func (r Run) LiveStage() (Stage, bool) {
	var live Stage
	for _, c := range r.Log {
		switch b := c.Body.(type) {
		case *StageIntent:
			live = b.Stage
		case *StageExit:
			live = ""
		case *StopResult:
			live = ""
		}
	}
	return live, live != ""
}

// Frozen reports whether the machine's latest freeze row says its stage slice is frozen.
func (r Run) Frozen() bool { return r.Freeze.Frozen }

// Rescued reports whether a rescue request has been made against the run.
func (r Run) Rescued() bool {
	for _, q := range r.Requests {
		if q.Kind == RequestRescue {
			return true
		}
	}
	return false
}

// Removing reports whether an rm request has been made against the run.
func (r Run) Removing() bool {
	for _, q := range r.Requests {
		if q.Kind == RequestRemove {
			return true
		}
	}
	return false
}

// Status is what users see of a run. Exactly one of Phase and Outcome is set.
type Status struct {
	Phase   Phase
	Outcome Outcome
	// Stage is the stage the run is in, or the stage that failed.
	Stage Stage
	// Held says why a paused run waits for rescue or rm, or is "".
	Held string
	// Detail is the stop's detail, or why the last machine intent was refused.
	Detail string
}

// ErrHeld is why a run's machine stays paused until rescue or rm.
var ErrHeld = errors.New("paused without the freeze: hib task rescue TASK or hib task rm TASK")

// ErrPastDeadline means a frozen stage's deadline passed while its machine was paused. Nothing
// wakes the machine until the daemon records the stop.
var ErrPastDeadline = errors.New("the run's deadline passed while it was paused")

// Status derives what users see. daemon is the current daemon instance, as the store records it
// at daemon start. An intent another instance left open shows as recovering until this one
// closes it.
func (r Run) Status(daemon Instance) Status {
	if r.Ended() {
		o, stage := r.Outcome()
		st := Status{Outcome: o, Stage: stage}
		if s, ok := r.Stop(); ok {
			st.Detail = s.Detail
		}
		return st
	}
	last, ok := r.Last()
	if !ok {
		return Status{Phase: PhaseQueued}
	}
	stage, live := r.LiveStage()
	st := Status{Phase: last.Step().Phase(), Stage: stage}
	if m, ok := last.Body.(*MachineResult); ok && m.Refused != "" {
		st.Phase, st.Detail = PhaseQueued, m.Refused
	}
	if s, ok := r.Stop(); ok {
		st.Detail = s.Detail
	} else if len(r.Requests) > 0 {
		st.Phase = PhaseStopping
	}
	if live && r.Machine == MachinePaused {
		st.Phase = PhasePaused
		if !r.Frozen() {
			st.Held = ErrHeld.Error()
		}
	}
	if last.Kind() == Intent && last.By != daemon {
		st.Phase = PhaseRecovering
	}
	return st
}

// Outcome derives how the run ended: the stop's outcome if one is recorded, otherwise failed at
// the first stage that didn't succeed, otherwise succeeded or no_changes from the publication.
// It is meaningful once the run has ended.
func (r Run) Outcome() (Outcome, Stage) {
	if s, ok := r.Stop(); ok {
		return s.Outcome, r.lastStage()
	}
	for _, c := range r.Log {
		if b, ok := c.Body.(*StageExit); ok && !b.Exit.OK() {
			return Failed, b.Stage
		}
	}
	if pub := r.Publication(); StepFinish.Skipped(r.ID, r.Spec) || (pub != nil && pub.Changes) {
		return Succeeded, ""
	}
	return NoChanges, ""
}

// Publication is what the finish stage published, as collect read it, or nil.
func (r Run) Publication() *Publication {
	for _, c := range slices.Backward(r.Log) {
		if b, ok := c.Body.(*CollectResult); ok {
			return b.Publication
		}
	}
	return nil
}

func (r Run) lastStage() Stage {
	var st Stage
	for _, c := range r.Log {
		if b, ok := c.Body.(*StageIntent); ok {
			st = b.Stage
		}
	}
	return st
}
