package task

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// StopFor returns the stop run r must take at now, if any. The daemon calls it at every step
// boundary and on every poll while a stage runs, so a cancel, a deadline or a lost machine ends
// the main line within one poll. When several apply, the earliest wins, so a cancel made after
// the deadline passed still ends the run as timed_out.
func StopFor(r Run, now time.Time) (StopIntent, bool) {
	if r.Ended() {
		return StopIntent{}, false
	}
	if _, ok := r.Stop(); ok {
		return StopIntent{}, false
	}
	if r.MachineID() != "" && r.Machine == MachineGone && !r.Queued() {
		return StopIntent{Outcome: Lost, Detail: "E2B no longer has the machine"}, true
	}
	type cause struct {
		at   time.Time
		stop StopIntent
	}
	var causes []cause
	deadline, granted := r.Deadline()
	if granted && !now.Before(deadline) {
		causes = append(causes, cause{deadline, StopIntent{Outcome: TimedOut,
			Detail: fmt.Sprintf("the %s deadline passed", r.Spec.Timeout)}})
	}
	if r.Queued() && !now.Before(r.QueueLimit()) {
		causes = append(causes, cause{r.QueueLimit(), StopIntent{Outcome: Expired,
			Detail: fmt.Sprintf("no room for %s", MaxQueued)}})
	}
	for _, q := range r.Requests {
		if q.At.After(now) {
			continue
		}
		causes = append(causes, cause{q.At, StopIntent{Outcome: Cancelled,
			Detail: fmt.Sprintf("hib task %s by %s", q.Kind, q.Actor.Name), Request: q.ID}})
	}
	if len(causes) == 0 {
		return StopIntent{}, false
	}
	first := slices.MinFunc(causes, func(a, b cause) int { return a.at.Compare(b.at) })
	return first.stop, true
}

// PausedAction is what the daemon does when it finds a run's machine paused.
type PausedAction string

const (
	// ResumeAndThaw resumes a frozen machine and thaws its stage. Only the host pauses a live
	// run's machine, so the daemon resumes it at its next look: after boot, or after a shutdown
	// that never came.
	ResumeAndThaw PausedAction = "resume-and-thaw"
	// ResumeToStop resumes the machine only to kill its stage at once: the run is stopping and
	// the stage is frozen, or the user rescued it.
	ResumeToStop PausedAction = "resume-to-stop"
	// StopPaused ends the stage without resuming: an rm removes the machine and its stage with
	// it.
	StopPaused PausedAction = "stop-paused"
	// Hold leaves a machine paused without the freeze until rescue or rm. hibd never chooses.
	Hold PausedAction = "hold"
)

// DecidePaused says what to do with run r whose machine is paused while a stage is live.
func DecidePaused(r Run) PausedAction {
	_, stopping := r.Stop()
	switch {
	case r.Removing():
		return StopPaused
	case !r.Frozen() && stopping && r.Rescued():
		return ResumeToStop
	case !r.Frozen():
		return Hold
	case stopping:
		return ResumeToStop
	}
	return ResumeAndThaw
}

// Pick returns the queued run to grant next: the oldest whose machine fits now. fits checks
// room and is called oldest first. Runs past their queue limit are left to StopFor.
func Pick(runs []Run, now time.Time, fits func(Run) error) (RunID, bool) {
	queued := slices.Clone(runs)
	slices.SortStableFunc(queued, func(a, b Run) int {
		return cmp.Or(a.Submitted.Compare(b.Submitted), cmp.Compare(a.ID.N, b.ID.N))
	})
	for _, r := range queued {
		if !r.Queued() || r.Ended() || !now.Before(r.QueueLimit()) {
			continue
		}
		if _, stopping := StopFor(r, now); stopping {
			continue
		}
		if fits(r) == nil {
			return r.ID, true
		}
	}
	return RunID{}, false
}

// MachineTimeout is the E2B timeout to set on run r's machine at now: the time to its deadline
// plus TimeoutMargin, so hibd's stop always comes before E2B's pause.
func MachineTimeout(r Run, now time.Time) time.Duration {
	d, ok := r.Deadline()
	if !ok {
		return TimeoutMargin
	}
	return max(d.Sub(now), 0) + TimeoutMargin
}

// RuntimeMax is a stage unit's RuntimeMaxSec when launched at now: the time to the deadline.
func RuntimeMax(r Run, now time.Time) time.Duration {
	d, ok := r.Deadline()
	if !ok {
		return time.Second
	}
	return max(d.Sub(now).Truncate(time.Second), time.Second)
}

// Task is a task with its runs, oldest first, every request made against it and its removal if
// one began.
type Task struct {
	ID       ID
	Actor    Actor
	Created  time.Time
	Runs     []Run
	Requests []Request
	Removal  *Removal
}

// Live returns the task's run that hasn't ended, if any. A task has at most one: a follow-up is
// refused while one is live.
func (t Task) Live() (Run, bool) {
	if n := len(t.Runs); n > 0 && !t.Runs[n-1].Ended() {
		return t.Runs[n-1], true
	}
	return Run{}, false
}

// Removing reports whether an rm request has been made against the task.
func (t Task) Removing() bool {
	return slices.ContainsFunc(t.Requests, func(q Request) bool { return q.Kind == RequestRemove })
}

// Removal is the task's own two-checkpoint workflow: recorded before the machine is removed,
// and Done after. Deleting a task is its lifecycle, not a run's outcome.
type Removal struct {
	// Why is "rm" or "retention".
	Why     string
	Request int64
	At      time.Time
	// Done is zero while the removal is open, and an open removal is carried out again on
	// restart: removing a machine that is already gone succeeds.
	Done time.Time
}

// Retire returns the removal task t needs at now, if any: an rm once no run is live, or
// retention KeepFor after its last run ended.
func Retire(t Task, now time.Time) (Removal, bool) {
	if t.Removal != nil {
		return Removal{}, false
	}
	if _, live := t.Live(); live || len(t.Runs) == 0 {
		return Removal{}, false
	}
	last := t.Runs[len(t.Runs)-1]
	for _, q := range t.Requests {
		if q.Kind == RequestRemove {
			return Removal{Why: "rm", Request: q.ID, At: now}, true
		}
	}
	if end, ok := last.Last(); ok && !now.Before(end.At.Add(KeepFor)) {
		return Removal{Why: "retention", At: now}, true
	}
	return Removal{}, false
}
