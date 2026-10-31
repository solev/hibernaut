package task

import (
	"encoding/json"
	"fmt"
	"time"
)

// Step is one durable checkpoint of a run. A run enters steps in the order of the step table
// and never goes back, so the table is the state machine and taskrun's straight-line code, which
// calls them in that order, is its transition graph. Phase, stage and the diagram all derive
// from the table.
type Step string

// The steps in order. The main line runs StepMachine to StepFinish. The wrap-up, StepStop to
// StepKeep, runs after the main line ends, early or not, and no request cuts it short.
const (
	// StepMachine waits for room, then creates the task's machine or resumes the one an earlier
	// run kept. Admission and create are one step because the room check must sit under
	// machines.RoomLock with the create.
	StepMachine Step = "machine"
	// StepUpload copies the inputs and the runner into the machine and checks their hashes.
	StepUpload Step = "upload"
	// StepSetup clones the repository and makes the task branch.
	StepSetup Step = "setup"
	// StepCommand runs the command with the prompt on stdin.
	StepCommand Step = "command"
	// StepFinish commits, pushes and opens the draft pull request.
	StepFinish Step = "finish"
	// StepStop ends the run early: it kills the live stage and proves it gone.
	StepStop Step = "stop"
	// StepCollect copies the last log bytes and the publication record to the host.
	StepCollect Step = "collect"
	// StepKeep pauses the machine and keeps it for follow-ups, or removes it on rm.
	StepKeep Step = "keep"
)

// Kind says where in its step a checkpoint sits.
type Kind string

const (
	// Intent opens a step, before its outside action. An open intent means the action may or
	// may not have happened, so whoever resumes the step looks before it acts.
	Intent Kind = "intent"
	// Result closes the outside action. For a stage it means the unit is known to have started.
	Result Kind = "result"
	// Exit closes a stage step: the unit ended. Waiting for it is not an outside action, so it
	// has no intent of its own.
	Exit Kind = "exit"
)

// Phase is what a live run is doing, as users see it. It is derived from the log and never
// stored.
type Phase string

const (
	PhaseQueued     Phase = "queued"
	PhasePreparing  Phase = "preparing"
	PhaseRunning    Phase = "running"
	PhasePaused     Phase = "paused"
	PhaseStopping   Phase = "stopping"
	PhaseCollecting Phase = "collecting"
	PhaseRecovering Phase = "recovering"
)

// Phases returns every phase in the order users see them.
func Phases() []Phase {
	return []Phase{PhaseQueued, PhasePreparing, PhaseRunning, PhasePaused, PhaseStopping,
		PhaseCollecting, PhaseRecovering}
}

// Outcome is how a run ended. It is derived from the log and never stored.
type Outcome string

const (
	Succeeded Outcome = "succeeded"
	NoChanges Outcome = "no_changes"
	Failed    Outcome = "failed"
	Cancelled Outcome = "cancelled"
	TimedOut  Outcome = "timed_out"
	Expired   Outcome = "expired"
	Lost      Outcome = "lost"
)

// Outcomes returns every outcome.
func Outcomes() []Outcome {
	return []Outcome{Succeeded, NoChanges, Failed, Cancelled, TimedOut, Expired, Lost}
}

// EndsEarly reports whether o can be a StopIntent's outcome: a reason to end a run before its
// main line does.
func (o Outcome) EndsEarly() bool {
	switch o {
	case Failed, Cancelled, TimedOut, Expired, Lost:
		return true
	}
	return false
}

// Stage is a step that runs a systemd unit in the machine.
type Stage string

const (
	StageSetup   Stage = "setup"
	StageCommand Stage = "command"
	StageFinish  Stage = "finish"
)

// Stages returns the stages in the order the step table runs them.
func Stages() []Stage {
	var out []Stage
	for _, s := range steps {
		if s.stage != "" {
			out = append(out, s.stage)
		}
	}
	return out
}

// Step returns the step that runs s.
func (s Stage) Step() Step { return Step(s) }

type stepInfo struct {
	step  Step
	phase Phase
	stage Stage
	// wrapUp steps run after the main line, and no request cuts them short.
	wrapUp bool
	// early is true for the step entered only when the main line ends early.
	early bool
	// skip says when the main line passes over the step, and why, for the diagram.
	skip    func(RunID, Spec) bool
	skipWhy string
	intent  func() Body
	result  func() Body
	exit    func() Body
	doc     string
}

// steps is the state machine. Its order is the order a run enters steps, and nothing else in
// the module lists them.
var steps = []stepInfo{
	{step: StepMachine, phase: PhasePreparing,
		intent: func() Body { return &MachineIntent{} }, result: func() Body { return &MachineResult{} },
		doc: "wait for room, then create or resume"},
	{step: StepUpload, phase: PhasePreparing,
		intent: func() Body { return &UploadIntent{} }, result: func() Body { return &UploadResult{} },
		doc: "upload inputs and runner, check hashes"},
	{step: StepSetup, phase: PhaseRunning, stage: StageSetup,
		skip:    func(r RunID, s Spec) bool { return r.FollowUp() || s.Repo == (Repo{}) },
		skipWhy: "follow-up or no repo",
		intent:  func() Body { return &StageIntent{Stage: StageSetup} },
		result:  func() Body { return &StageLaunched{Stage: StageSetup} },
		exit:    func() Body { return &StageExit{Stage: StageSetup} },
		doc:     "clone and branch"},
	{step: StepCommand, phase: PhaseRunning, stage: StageCommand,
		intent: func() Body { return &StageIntent{Stage: StageCommand} },
		result: func() Body { return &StageLaunched{Stage: StageCommand} },
		exit:   func() Body { return &StageExit{Stage: StageCommand} },
		doc:    "run the command"},
	{step: StepFinish, phase: PhaseRunning, stage: StageFinish,
		skip:    func(_ RunID, s Spec) bool { return s.Repo == (Repo{}) },
		skipWhy: "no repo",
		intent:  func() Body { return &StageIntent{Stage: StageFinish} },
		result:  func() Body { return &StageLaunched{Stage: StageFinish} },
		exit:    func() Body { return &StageExit{Stage: StageFinish} },
		doc:     "commit, push, draft PR"},
	{step: StepStop, phase: PhaseStopping, wrapUp: true, early: true,
		intent: func() Body { return &StopIntent{} }, result: func() Body { return &StopResult{} },
		doc: "kill the live stage, prove it gone"},
	{step: StepCollect, phase: PhaseCollecting, wrapUp: true,
		intent: func() Body { return &CollectIntent{} }, result: func() Body { return &CollectResult{} },
		doc: "copy last logs and the publication"},
	{step: StepKeep, phase: PhaseCollecting, wrapUp: true,
		intent: func() Body { return &KeepIntent{} }, result: func() Body { return &KeepResult{} },
		doc: "pause and keep, or remove on rm"},
}

// Steps returns every step in the order a run enters them.
func Steps() []Step {
	out := make([]Step, len(steps))
	for i, s := range steps {
		out[i] = s.step
	}
	return out
}

func (s Step) info() stepInfo {
	for _, i := range steps {
		if i.step == s {
			return i
		}
	}
	panic(fmt.Sprintf("task: unknown step %q", string(s)))
}

// Valid reports whether s is in the step table.
func (s Step) Valid() bool {
	for _, i := range steps {
		if i.step == s {
			return true
		}
	}
	return false
}

// Index is the step's place in the table. A run's log never goes to a lower index.
func (s Step) Index() int {
	for n, i := range steps {
		if i.step == s {
			return n
		}
	}
	return -1
}

// Phase is what users see while the run is in s, before overlays such as paused.
func (s Step) Phase() Phase { return s.info().phase }

// Stage is the stage s runs, or "".
func (s Step) Stage() Stage { return s.info().stage }

// WrapUp reports whether s runs after the main line, whatever ended it.
func (s Step) WrapUp() bool { return s.info().wrapUp }

// Skipped reports whether the main line passes over s for run r with spec sp. Skipping is a pure
// function of the run, so a skipped step leaves no checkpoint.
func (s Step) Skipped(r RunID, sp Spec) bool {
	i := s.info()
	return i.skip != nil && i.skip(r, sp)
}

// Doc is one line on what s does, for help and the diagram.
func (s Step) Doc() string { return s.info().doc }

// Body is what a checkpoint says. Each step has one intent body and one result body, and the
// unexported method seals the set, so a body can't be filed under the wrong step or kind.
type Body interface {
	checkpoint() (Step, Kind)
}

// NewBody returns an empty body for step and kind, for a store to decode into.
func NewBody(step Step, kind Kind) (Body, error) {
	if !step.Valid() {
		return nil, fmt.Errorf("unknown step %q", step)
	}
	i := step.info()
	switch kind {
	case Intent:
		return i.intent(), nil
	case Result:
		return i.result(), nil
	case Exit:
		if i.exit != nil {
			return i.exit(), nil
		}
	}
	return nil, fmt.Errorf("the %s step has no %q checkpoint", step, kind)
}

// DecodeBody decodes data, as a store saved it, into the body for step and kind.
func DecodeBody(step Step, kind Kind, data []byte) (Body, error) {
	b, err := NewBody(step, kind)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, b); err != nil {
		return nil, fmt.Errorf("decoding the %s %s: %w", step, kind, err)
	}
	return b, nil
}

// Instance names one start of the daemon. A checkpoint records the instance that wrote it, so an
// intent another instance left open shows as recovering.
type Instance string

// Checkpoint is one row of a run's log. Seq counts from 1 with no gaps, and a store accepts a
// checkpoint only as the next one, which is how two writers of one run are caught.
type Checkpoint struct {
	Run  RunID
	Seq  int
	At   time.Time
	By   Instance
	Body Body
}

// Step is the step the checkpoint belongs to.
func (c Checkpoint) Step() Step { s, _ := c.Body.checkpoint(); return s }

// Kind says whether it opens or closes its step.
func (c Checkpoint) Kind() Kind { _, k := c.Body.checkpoint(); return k }

// MachineIntent opens StepMachine once the run holds the grant. The planned machine name is the
// task ID and its labels are Labels(run), both pure functions of the run, so this row alone
// tells a restarted daemon what to look up before it creates anything.
type MachineIntent struct{}

// MachineResult closes StepMachine.
type MachineResult struct {
	// ID is the sandbox E2B gave the machine. E2B stays the catalog: this is a reference.
	ID string `json:",omitempty"`
	// Adopted is true when the daemon found the machine by name after a crash rather than
	// creating it.
	Adopted bool `json:",omitempty"`
	// Refused says why there was no room. The run is queued again and a later grant opens a new
	// MachineIntent.
	Refused string `json:",omitempty"`
}

// UploadIntent opens StepUpload. What to upload is the run's Spec.Inputs and the runner.
type UploadIntent struct{}

// UploadResult closes StepUpload once every file's hash matched inside the machine.
type UploadResult struct{}

// StageIntent opens a stage step. The unit name is UnitName(run, stage), so launching twice is
// refused by systemd and the stage's start marker refuses a second start inside the unit.
type StageIntent struct {
	Stage Stage
}

// StageLaunched closes a stage's launch: its unit or its start marker exists, so the stage is
// never launched again.
type StageLaunched struct {
	Stage Stage
	// Adopted is true when the look before launching found the unit already started: the last
	// pass launched it and crashed before recording this.
	Adopted bool `json:",omitempty"`
}

// StageExit closes a stage step with the unit's exit record.
type StageExit struct {
	Stage Stage
	Exit  ExitRecord
}

// ExitRecord is a stage's exit record, as the runner's ExecStopPost wrote it from systemd's
// SERVICE_RESULT, EXIT_CODE and EXIT_STATUS, or Vanished when the marker exists but the record
// and the unit are both gone.
type ExitRecord struct {
	Result   string `json:",omitempty"`
	Code     string `json:",omitempty"`
	Status   int    `json:",omitempty"`
	Vanished bool   `json:",omitempty"`
}

// OK reports whether the stage succeeded.
func (e ExitRecord) OK() bool { return !e.Vanished && e.Result == "success" }

// StopIntent opens StepStop: the main line ends early with Outcome. It is the only place an
// early outcome is recorded.
type StopIntent struct {
	Outcome Outcome
	Detail  string `json:",omitempty"`
	// Request is the request row this stop carries out, or 0. A request is consumed exactly
	// when a StopIntent cites it.
	Request int64 `json:",omitempty"`
}

// Error lets a step return the stop as an error, which ends the main line.
func (s *StopIntent) Error() string {
	if s.Detail == "" {
		return string(s.Outcome)
	}
	return string(s.Outcome) + ": " + s.Detail
}

// StopResult closes StepStop.
type StopResult struct {
	// How says how the stage ended: "term" or "kill" when the stop ended it, "vanished" when its
	// unit was gone without an exit record, "not-started" when no stage was live, "gone" with
	// the machine, "removed-paused" when an rm removes it paused, or "unproven" when the
	// proof budget expired since the later of the intent and this daemon's start.
	// An unproven stage may still run when the machine wakes.
	How string
}

// CollectIntent opens StepCollect.
type CollectIntent struct{}

// CollectResult closes StepCollect.
type CollectResult struct {
	// LogBytes counts kept output. Dropped counts reported guest bytes not kept, including
	// unread bytes below MaxLogBytes. It can understate when a guest size is unknown or false.
	LogBytes int64 `json:",omitempty"`
	Dropped  int64 `json:",omitempty"`
	// Unread names tails of launched stages without a StageExit and publication the host could
	// not read. Their size may be unknown, and output written after collection is not counted.
	Unread []string `json:",omitempty"`
	// Publication is what finish published, if it ran.
	Publication *Publication `json:",omitempty"`
}

// Publication is what the finish stage reports.
type Publication struct {
	Changes bool
	Commit  string `json:",omitempty"`
	Pushed  bool   `json:",omitempty"`
	PR      string `json:",omitempty"`
	// Author is the commit and PR author, recorded apart from the actor.
	Author string `json:",omitempty"`
}

// KeepIntent opens StepKeep.
type KeepIntent struct{}

// KeepResult closes StepKeep, and the run with it.
type KeepResult struct {
	// Removed is true when an rm request removed the machine instead of keeping it.
	Removed bool `json:",omitempty"`
	// Gone is true when there was no machine to keep.
	Gone bool `json:",omitempty"`
}

func (*MachineIntent) checkpoint() (Step, Kind)   { return StepMachine, Intent }
func (*MachineResult) checkpoint() (Step, Kind)   { return StepMachine, Result }
func (*UploadIntent) checkpoint() (Step, Kind)    { return StepUpload, Intent }
func (*UploadResult) checkpoint() (Step, Kind)    { return StepUpload, Result }
func (b *StageIntent) checkpoint() (Step, Kind)   { return b.Stage.Step(), Intent }
func (b *StageLaunched) checkpoint() (Step, Kind) { return b.Stage.Step(), Result }
func (b *StageExit) checkpoint() (Step, Kind)     { return b.Stage.Step(), Exit }
func (*StopIntent) checkpoint() (Step, Kind)      { return StepStop, Intent }
func (*StopResult) checkpoint() (Step, Kind)      { return StepStop, Result }
func (*CollectIntent) checkpoint() (Step, Kind)   { return StepCollect, Intent }
func (*CollectResult) checkpoint() (Step, Kind)   { return StepCollect, Result }
func (*KeepIntent) checkpoint() (Step, Kind)      { return StepKeep, Intent }
func (*KeepResult) checkpoint() (Step, Kind)      { return StepKeep, Result }

// CheckNext returns nil if next may follow log, the run's checkpoints so far. It is the whole
// transition rule, and stores call it inside the append:
//
//   - a run starts with the machine intent, or with a stop when it ends while queued;
//   - an open intent is followed by its result, or superseded by a stop;
//   - a launched stage is followed by its exit, or by a stop;
//   - a closed step is followed by the intent of the step the table enters next for this run,
//     passing over only the steps it skips, and a failed stage's exit by the wrap-up;
//   - a refused machine result is followed by a new machine intent, or a stop;
//   - a stop carries an early outcome;
//   - after the keep result nothing follows.
//
// A stop that supersedes an open intent doesn't say whether the action happened, so the wrap-up
// steps look before they act.
func CheckNext(id RunID, sp Spec, log []Checkpoint, next Body) error {
	step, kind := next.checkpoint()
	stop := step == StepStop && kind == Intent
	if stop {
		if err := checkStop(next); err != nil {
			return err
		}
	}
	if len(log) == 0 {
		if (step == StepMachine && kind == Intent) || stop {
			return nil
		}
		return fmt.Errorf("a run starts with the machine intent or a stop, not the %s %s", step, kind)
	}
	last := log[len(log)-1]
	lastStep, lastKind := last.Body.checkpoint()
	switch {
	case lastStep == StepKeep && lastKind == Result:
		return fmt.Errorf("the run has ended")
	case lastKind == Intent:
		if step == lastStep && kind == Result {
			return nil
		}
		if stop && lastStep.Index() < StepStop.Index() {
			return nil
		}
		return fmt.Errorf("the %s intent is open, so its result or a stop follows, not the %s %s",
			lastStep, step, kind)
	case lastKind == Result && lastStep.Stage() != "":
		if (step == lastStep && kind == Exit) || stop {
			return nil
		}
		return fmt.Errorf("the %s stage is live, so its exit or a stop follows, not the %s %s",
			lastStep, step, kind)
	}
	if kind != Intent {
		return fmt.Errorf("the %s %s needs its intent first", step, kind)
	}
	if step.Index() < lastStep.Index() {
		return fmt.Errorf("steps never go back: %s after %s", step, lastStep)
	}
	refused := false
	if r, ok := last.Body.(*MachineResult); ok && r.Refused != "" {
		refused = true
	}
	if step == lastStep {
		if !refused {
			return fmt.Errorf("the %s step is closed", step)
		}
		return nil
	}
	if stop {
		return nil
	}
	if refused {
		return fmt.Errorf("the machine was refused, so a new machine intent or a stop follows, "+
			"not the %s %s", step, kind)
	}
	if want, ok := follows(id, sp, last); !ok || step != want {
		return fmt.Errorf("the table enters %q after the %s %s, not %s", want, lastStep, lastKind,
			step)
	}
	return nil
}

// follows returns the step run id with spec sp enters once c closes its step: the next step in
// the table it doesn't skip. A failed stage ends the main line, so the wrap-up follows it, and
// the stop step is entered only through a stop.
func follows(id RunID, sp Spec, c Checkpoint) (Step, bool) {
	from := c.Step().Index() + 1
	if e, ok := c.Body.(*StageExit); ok && !e.Exit.OK() {
		from = StepStop.Index()
	}
	for _, i := range steps[from:] {
		if i.early || (i.skip != nil && i.skip(id, sp)) {
			continue
		}
		return i.step, true
	}
	return "", false
}

func checkStop(b Body) error {
	if s, ok := b.(*StopIntent); !ok || !s.Outcome.EndsEarly() {
		return fmt.Errorf("a stop needs an early outcome")
	}
	return nil
}
