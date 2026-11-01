package hibapi

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

// FeatureTasks means hibd saves task submissions and its daemon runs them.
// Other task handlers may still be unavailable.
const FeatureTasks = "tasks"

// ErrorCode says what kind of failure a Response's Error is, so hib can decide without reading
// the text. Task commands set it, other commands leave it empty.
type ErrorCode string

const (
	// CodeConflict means the request ID was used for a different request. Never retry.
	CodeConflict ErrorCode = "CONFLICT"
	// CodeQueueFull means too many runs wait already. Retry later.
	CodeQueueFull ErrorCode = "QUEUE_FULL"
	// CodeInputFull means the task's inputs would exceed its limit. Never retry.
	CodeInputFull ErrorCode = "INPUT_FULL"
	// CodeNoTask means no task has the ID.
	CodeNoTask ErrorCode = "NO_TASK"
	// CodeRunLive means the task has a run that hasn't ended, so no follow-up may start.
	CodeRunLive ErrorCode = "RUN_LIVE"
	// CodeNotLive means the task has no live run to cancel or rescue.
	CodeNotLive ErrorCode = "NOT_LIVE"
	// CodeBadRequest means the request didn't parse. Never retry.
	CodeBadRequest ErrorCode = "BAD_REQUEST"
	// CodeBusy means tasks.db stayed locked past its busy timeout. Retry with the same ID.
	CodeBusy ErrorCode = "BUSY"
)

// Retryable reports whether the same request may be sent again with the same request ID.
func (c ErrorCode) Retryable() bool { return c == CodeQueueFull || c == CodeBusy }

// RefusedBeforeSave reports whether hibd refused without recording this request.
// BUSY, NO_TASK and uncoded errors can also come from reads after Submit commits.
func (c ErrorCode) RefusedBeforeSave() bool {
	switch c {
	case CodeBadRequest:
		// The handler validates the input before calling Submit or Request.
		return true
	case CodeConflict:
		// Submit and Request compare the ID's saved request before inserting another.
		return true
	case CodeQueueFull, CodeInputFull, CodeRunLive:
		// Submit checks queue space, input budget and live runs before its inserts.
		return true
	case CodeNotLive:
		// Request checks for a live run before inserting cancel or rescue.
		return true
	default:
		return false
	}
}

// TaskReply is what every task command adds to a Response: the request ID it answered, and the
// task or the tasks. The fields are on Response through embedding, so a task reply is still one
// JSON object.
type TaskReply struct {
	// RequestID is the ID the request carried, echoed so a retry can be matched.
	RequestID string `json:"requestId,omitempty"`
	// Code is set with Error on a task command's failure.
	Code      ErrorCode    `json:"errorCode,omitempty"`
	Retryable bool         `json:"retryable,omitempty"`
	Task      *Task        `json:"task,omitempty"`
	Tasks     []Task       `json:"tasks,omitzero"`
	Request   *TaskRequest `json:"request,omitempty"`
	// TaskLog is a range of a run's output, from task logs.
	TaskLog *TaskLog `json:"taskLog,omitempty"`
}

// TaskStart is the one JSON line hib task start and hib task run send on stdin. The bytes of
// each file follow it on stdin, in order, Size bytes each, the prompt first. hibd checks it into
// a task.Submission before anything is saved.
type TaskStart struct {
	RequestID string `json:"requestId"`
	// Task is the task a follow-up runs on, or empty for a new task.
	Task    task.ID    `json:"task,omitempty"`
	Repo    string     `json:"repo,omitempty"`
	Branch  string     `json:"branch,omitempty"`
	Recipe  string     `json:"recipe,omitempty"`
	Command []string   `json:"command,omitempty"`
	Prompt  *TaskFile  `json:"prompt,omitempty"`
	Files   []TaskFile `json:"files,omitzero"`
	Image   string     `json:"image,omitempty"`
	Size    string     `json:"size,omitempty"`
	// TimeoutSeconds is 0 for task.DefaultTimeout.
	TimeoutSeconds int64        `json:"timeoutSeconds,omitempty"`
	Publish        task.Publish `json:"publish,omitempty"`
}

// TaskFile names one input that follows on stdin.
type TaskFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Task is a task as task ls and task show report it. The task package defines every enum it
// carries, so a phase, outcome, step or stage that task doesn't know can't be sent.
type Task struct {
	ID      task.ID   `json:"id"`
	Repo    string    `json:"repo,omitempty"`
	Created time.Time `json:"created"`
	// Machine is running, paused, or gone. Empty without a recorded machine ID.
	Machine task.MachineState `json:"machine,omitempty"`
	// Runs are oldest first. task ls sends only the latest.
	Runs     []TaskRun     `json:"runs"`
	Requests []TaskRequest `json:"requests,omitempty"`
	Removal  *TaskRemoval  `json:"removal,omitempty"`
}

// TaskRequest is a recorded request. Run is 0 for removal with no live run.
type TaskRequest struct {
	ID    int64            `json:"id"`
	Kind  task.RequestKind `json:"kind"`
	Run   int              `json:"run"`
	At    time.Time        `json:"at"`
	Actor TaskActor        `json:"actor"`
}

// TaskActor identifies who made a request and where it came from.
type TaskActor struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// TaskRemoval is a task's removal. Done is absent while removal is open.
type TaskRemoval struct {
	Why  string     `json:"why"`
	At   time.Time  `json:"at"`
	Done *time.Time `json:"done,omitempty"`
}

// RequestOf is the wire form of recorded request r.
func RequestOf(r task.Request) TaskRequest {
	return TaskRequest{ID: r.ID, Kind: r.Kind, Run: r.Run, At: r.At,
		Actor: TaskActor{Name: r.Actor.Name, Source: r.Actor.Source}}
}

// TaskRun is one run. Exactly one of Phase and Outcome is set.
type TaskRun struct {
	N       int          `json:"n"`
	Phase   task.Phase   `json:"phase,omitempty"`
	Outcome task.Outcome `json:"outcome,omitempty"`
	// Stage is the stage the run is in, or the one that failed.
	Stage task.Stage `json:"stage,omitempty"`
	// Step is the step of the run's last checkpoint, for show.
	Step task.Step `json:"step,omitempty"`
	// Held says why a paused run waits for rescue or rm.
	Held        string            `json:"held,omitempty"`
	Detail      string            `json:"detail,omitempty"`
	Command     []string          `json:"command"`
	Submitted   time.Time         `json:"submitted"`
	Deadline    *time.Time        `json:"deadline,omitempty"`
	Publication *task.Publication `json:"publication,omitempty"`
}

// TaskLog is a range of a run's setup, command and finish logs in task.Stages order.
// From and Next count bytes in that stream. A range never spans stages. Data is base64 in JSON.
// Done means the run ended before the read and Next reaches the stream's end.
type TaskLog struct {
	Run   int        `json:"run"`
	Stage task.Stage `json:"stage,omitempty"`
	From  int64      `json:"from"`
	Data  []byte     `json:"data"`
	Next  int64      `json:"next"`
	Done  bool       `json:"done,omitempty"`
}

// Check rejects unknown task values and invalid log ranges in the reply.
func (r TaskReply) Check() error {
	switch r.Code {
	case "", CodeConflict, CodeQueueFull, CodeInputFull, CodeNoTask, CodeRunLive,
		CodeNotLive, CodeBadRequest, CodeBusy:
	default:
		return fmt.Errorf("unknown error code %q", r.Code)
	}
	if r.Task != nil {
		if err := r.Task.Check(); err != nil {
			return err
		}
	}
	for _, t := range r.Tasks {
		if err := t.Check(); err != nil {
			return err
		}
	}
	if r.Request != nil {
		if err := r.Request.Check(); err != nil {
			return err
		}
	}
	if r.TaskLog != nil {
		return r.TaskLog.Check()
	}
	return nil
}

// Check rejects unknown machine states, run values and request kinds.
func (t Task) Check() error {
	if !t.Machine.Valid() {
		return fmt.Errorf("unknown machine state %q", t.Machine)
	}
	for _, r := range t.Runs {
		if err := r.Check(); err != nil {
			return err
		}
	}
	for _, r := range t.Requests {
		if err := r.Check(); err != nil {
			return err
		}
	}
	return nil
}

// Check rejects request kinds that task does not define.
func (r TaskRequest) Check() error {
	if !r.Kind.Valid() {
		return fmt.Errorf("unknown request kind %q", r.Kind)
	}
	return nil
}

// Check rejects unknown stages and invalid byte ranges.
func (l TaskLog) Check() error {
	if l.Stage != "" && !slices.Contains(task.Stages(), l.Stage) {
		return fmt.Errorf("unknown stage %q", l.Stage)
	}
	if l.Run < 1 || l.From < 0 || l.Next != l.From+int64(len(l.Data)) ||
		len(l.Data) > 1<<20 || len(l.Data) > 0 && l.Stage == "" {
		return errors.New("invalid task log range")
	}
	return nil
}

// RunOf is the wire form of run r as daemon sees it.
func RunOf(r task.Run, daemon task.Instance) TaskRun {
	st := r.Status(daemon)
	out := TaskRun{N: r.ID.N, Phase: st.Phase, Outcome: st.Outcome, Stage: st.Stage,
		Held: st.Held, Detail: st.Detail, Command: r.Spec.Command, Submitted: r.Submitted,
		Publication: r.Publication()}
	if c, ok := r.Last(); ok {
		out.Step = c.Step()
	}
	if d, ok := r.Deadline(); ok {
		out.Deadline = &d
	}
	return out
}

// Check parses what hib received into known values: every enum must be one task defines.
func (r TaskRun) Check() error {
	switch {
	case (r.Phase == "") == (r.Outcome == ""):
		return errors.New("a run needs exactly one of phase and outcome")
	case r.Phase != "" && !slices.Contains(task.Phases(), r.Phase):
		return fmt.Errorf("unknown phase %q", r.Phase)
	case r.Outcome != "" && !slices.Contains(task.Outcomes(), r.Outcome):
		return fmt.Errorf("unknown outcome %q", r.Outcome)
	case r.Stage != "" && !slices.Contains(task.Stages(), r.Stage):
		return fmt.Errorf("unknown stage %q", r.Stage)
	case r.Step != "" && !r.Step.Valid():
		return fmt.Errorf("unknown step %q", r.Step)
	}
	return nil
}

// CodeOf maps a task error to its code. Errors it doesn't know get no code.
func CodeOf(err error) ErrorCode {
	for _, c := range []struct {
		err  error
		code ErrorCode
	}{
		{task.ErrConflict, CodeConflict},
		{task.ErrQueueFull, CodeQueueFull},
		{task.ErrInputFull, CodeInputFull},
		{task.ErrNoTask, CodeNoTask},
		{task.ErrRunLive, CodeRunLive},
	} {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	return ""
}
