package hibd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks"
	"github.com/solev/hibernaut/internal/termsafe"
)

type taskCommand = hibapi.TaskCommand

func taskHandlers() map[string]func(*App, context.Context, taskCommand, []string) int {
	return map[string]func(*App, context.Context, taskCommand, []string) int{
		"task start": (*App).taskStart, "task run": (*App).taskStart,
		"task ls": (*App).taskLs, "task show": (*App).taskShow,
		"task logs": (*App).taskLogs, "task wait": (*App).taskWaitNotYet,
		"task cancel": (*App).taskRequest, "task rescue": (*App).taskRequest,
		"task rm": (*App).taskRequest, "task cleanup": (*App).taskCleanup,
		"recipe put": (*App).recipe, "recipe ls": (*App).recipe,
		"recipe show": (*App).recipe, "recipe rm": (*App).recipe,
		"recipe default": (*App).recipe,
	}
}

var errTasksNotImplemented = errors.New("tasks are not implemented yet")

func (a *App) taskGroup(ctx context.Context, group string, args []string) int {
	if len(args) > 0 {
		for _, c := range hibapi.TaskCommands() {
			if c.Path == group+" "+args[0] {
				run := taskHandlers()[c.Path]
				if run == nil {
					return a.taskFail(true, c.Path, "", fmt.Errorf("no handler for %s", c.Path))
				}
				return run(a, ctx, c, args[1:])
			}
		}
	}
	fmt.Fprint(a.Stderr, taskUsage(group))
	return 2
}

func taskUsage(group string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: hibd %s <command> [-json]\n\ncommands:\n", group)
	for _, c := range hibapi.TaskCommands() {
		leaf, ok := strings.CutPrefix(c.Path, group+" ")
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "  %s\n      %s\n", strings.TrimSpace(leaf+" "+c.Args), c.Help)
	}
	return b.String()
}

func taskUsesE2B(cmd string) bool {
	for _, c := range hibapi.TaskCommands() {
		if c.Path == cmd {
			return c.E2B
		}
	}
	return false
}

func (a *App) taskWaitNotYet(_ context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	fl.Int("run", 0, "run number")
	if _, ok := a.parse(fl, args, 1, c.Path+" "+c.Args); !ok {
		return 2
	}
	return a.taskFail(*asJSON, c.Path, "", errTasksNotImplemented)
}

func (a *App) taskActor() task.Actor {
	user := cmp.Or(a.Getenv("SUDO_USER"), "root")
	return task.Actor{Owner: user, Name: user, Source: "cli"}
}

// taskStart is hibd task start and hibd task run TASK. stdin holds one hibapi.TaskStart line,
// then each file's bytes. It answers once the task and its inputs are saved. Nothing else runs:
// the daemon's Supervisor picks the run up on its next tick.
func (a *App) taskStart(ctx context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	n := strings.Count(c.Args, "TASK")
	rest, ok := a.parse(fl, args, n, c.Path+" "+c.Args)
	if !ok {
		return 2
	}
	in := newLineReader(a.Stdin)
	var req hibapi.TaskStart
	if err := json.Unmarshal(in.line(), &req); err != nil {
		return a.taskFail(*asJSON, c.Path, "", fmt.Errorf("%w: %w", errBadTaskRequest, err))
	}
	if n == 1 {
		req.Task = task.ID(rest[0])
	}
	sub, err := submissionOf(req, a.taskActor(), a.now())
	if err != nil {
		return a.taskFail(*asJSON, c.Path, req.RequestID, err)
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, req.RequestID, err)
	}
	st := a.tasks(cfg)
	defer st.Close()
	receipt, err := st.Submit(ctx, sub, in.rest())
	if err != nil {
		return a.taskFail(*asJSON, c.Path, req.RequestID, err)
	}
	t, err := st.Task(ctx, receipt.Run.Task)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, req.RequestID, err)
	}
	instance, err := st.Instance(ctx)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, req.RequestID, err)
	}
	run := t.Runs[receipt.Run.N-1]
	reply := hibapi.Task{ID: t.ID, Repo: run.Spec.Repo.String(), Created: t.Created,
		Runs: []hibapi.TaskRun{hibapi.RunOf(run, instance)}}
	if *asJSON {
		return a.reply(hibapi.Response{TaskReply: hibapi.TaskReply{
			RequestID: receipt.RequestID, Task: &reply}})
	}
	status := run.Status(instance)
	return a.writeReply([]byte(fmt.Sprintf("%s  %d  %s\n", t.ID, run.ID.N,
		cmp.Or(string(status.Phase), string(status.Outcome)))), nil, false)
}

var errBadTaskRequest = errors.New("bad task request")

// submissionOf checks a start request at the boundary and resolves its defaults. It is pure.
// Recipe names are resolved by the caller, which reads recipes.json, before it calls this.
func submissionOf(req hibapi.TaskStart, actor task.Actor, at time.Time) (task.Submission, error) {
	bad := func(format string, args ...any) (task.Submission, error) {
		return task.Submission{}, fmt.Errorf("%w: %s", errBadTaskRequest, fmt.Sprintf(format, args...))
	}
	if req.RequestID == "" || len(req.RequestID) > 128 {
		return bad("a request ID of 1 to 128 bytes is required")
	}
	if req.Task != "" {
		if _, err := task.ParseID(string(req.Task)); err != nil {
			return bad("%v", err)
		}
	}
	spec := task.Spec{Base: req.Branch, Command: req.Command, Recipe: req.Recipe,
		Image: req.Image, Size: req.Size, Publish: req.Publish,
		Timeout: time.Duration(req.TimeoutSeconds) * time.Second}
	if req.Repo != "" {
		owner, name, ok := strings.Cut(req.Repo, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return bad("repo %q isn't OWNER/NAME", req.Repo)
		}
		spec.Repo = task.Repo{Owner: owner, Name: name}
	}
	if len(spec.Command) == 0 {
		return bad("no command: give -- CMD ARG... or --recipe NAME")
	}
	spec.Image = cmp.Or(spec.Image, images.Default)
	spec.Size = cmp.Or(spec.Size, images.DefaultSize().Name())
	if _, err := images.SizeNamed(spec.Size); err != nil {
		return bad("%v", err)
	}
	if spec.Timeout <= 0 {
		spec.Timeout = task.DefaultTimeout
	}
	switch spec.Publish {
	case "":
		spec.Publish = task.PublishDraftPR
		if spec.Repo == (task.Repo{}) {
			spec.Publish = task.PublishNone
		}
	case task.PublishDraftPR, task.PublishNone:
	default:
		return bad("publish %q isn't draft-pr or none", spec.Publish)
	}
	if spec.Publish == task.PublishDraftPR && spec.Repo == (task.Repo{}) {
		return bad("--publish draft-pr needs --repo")
	}
	var total int64
	add := func(f hibapi.TaskFile) error {
		if f.Size < 0 {
			return fmt.Errorf("file %q has a negative size", f.Name)
		}
		total += f.Size
		spec.Inputs = append(spec.Inputs, task.File{Name: f.Name, Size: f.Size})
		return nil
	}
	if req.Prompt != nil {
		if err := add(hibapi.TaskFile{Name: task.PromptName, Size: req.Prompt.Size}); err != nil {
			return bad("%v", err)
		}
	}
	for _, f := range req.Files {
		if err := task.CheckInputName(f.Name); err != nil {
			return bad("%v", err)
		}
		if f.Name == task.PromptName {
			return bad("file %q has the prompt's name", f.Name)
		}
		if err := add(f); err != nil {
			return bad("%v", err)
		}
	}
	if total > task.MaxInputBytes {
		return bad("the prompt and files are %d bytes, more than %d", total, task.MaxInputBytes)
	}
	return task.Submission{RequestID: req.RequestID, Task: req.Task, Spec: spec, Actor: actor,
		At: at}, nil
}

// taskFail is fail with the error code and retryable flag hibapi derives from err.
func (a *App) taskFail(asJSON bool, cmd, requestID string, err error) int {
	if !asJSON {
		return a.fail(false, cmd, err)
	}
	code := hibapi.CodeOf(err)
	switch {
	case errors.Is(err, errBadTaskRequest), errors.Is(err, tasks.ErrLogRange):
		code = hibapi.CodeBadRequest
	case errors.Is(err, tasks.ErrNoReceipt):
		code = hibapi.CodeNoTask
	case errors.Is(err, tasks.ErrNotLive):
		code = hibapi.CodeNotLive
	case errors.Is(err, tasks.ErrBusy):
		code = hibapi.CodeBusy
	}
	return a.reply(hibapi.Response{Error: termsafe.String(withNextStep(cmd, err).Error()), TaskReply: hibapi.TaskReply{
		RequestID: requestID, Code: code, Retryable: code.Retryable()}})
}

// lineReader reads one line, then hands over the rest of the stream unread.
type lineReader struct {
	r   io.Reader
	buf []byte
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: r} }

// line returns the first line, at most 1 MiB, without its newline. It reads a byte at a time
// so nothing past the newline is consumed.
func (l *lineReader) line() []byte {
	var b [1]byte
	for len(l.buf) < 1<<20 {
		if n, err := l.r.Read(b[:]); n == 0 || err != nil || b[0] == '\n' {
			break
		}
		l.buf = append(l.buf, b[0])
	}
	return l.buf
}

// taskLs is hibd task ls.
func (a *App) taskLs(ctx context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, c.Path); !ok {
		return 2
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	st := a.tasks(cfg)
	defer st.Close()
	records, err := st.List(ctx)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	instance, ms, err := a.taskReadState(ctx, st)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	out := make([]hibapi.Task, len(records))
	for i, record := range records {
		out[i] = taskReadOf(record, instance, ms, false)
	}
	if *asJSON {
		return a.reply(hibapi.Response{TaskReply: hibapi.TaskReply{Tasks: out}})
	}
	if err := a.printTaskList(out); err != nil {
		return a.taskFail(false, c.Path, "", err)
	}
	return 0
}

// taskShow is hibd task show TASK and hibd task show -request ID.
func (a *App) taskShow(ctx context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	request := fl.String("request", "", "show the task a submission made")
	rest, ok := a.parseAny(fl, args)
	if !ok {
		return 2
	}
	var id task.ID
	switch {
	case len(rest) == 1 && *request == "":
		var err error
		id, err = task.ParseID(rest[0])
		if err != nil {
			return a.taskFail(*asJSON, c.Path, "", fmt.Errorf("%w: %w", errBadTaskRequest, err))
		}
	case len(rest) == 0 && *request != "":
	default:
		return a.taskFail(*asJSON, c.Path, "", fmt.Errorf("%w: give TASK or -request ID", errBadTaskRequest))
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	st := a.tasks(cfg)
	defer st.Close()
	if *request != "" {
		rec, err := st.Receipt(ctx, *request)
		if err != nil {
			return a.taskFail(*asJSON, c.Path, "", err)
		}
		id = rec.Run.Task
	}
	record, err := st.Task(ctx, id)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	instance, ms, err := a.taskReadState(ctx, st)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	out := taskReadOf(record, instance, ms, true)
	if *asJSON {
		return a.reply(hibapi.Response{TaskReply: hibapi.TaskReply{Task: &out}})
	}
	if err := a.printTask(out); err != nil {
		return a.taskFail(false, c.Path, "", err)
	}
	return 0
}

// taskLogs is hibd task logs. The cursor is a byte offset in the host's copy, so -f on the
// laptop is a loop of calls that each pass the last reply's Next.
func (a *App) taskLogs(ctx context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	n := fl.Int("run", 0, "run number, latest by default")
	from := fl.Int64("from", 0, "number of stream bytes already read")
	rest, ok := a.parse(fl, args, 1, c.Path+" "+c.Args)
	if !ok {
		return 2
	}
	id, err := task.ParseID(rest[0])
	if err != nil || *n < 0 || *from < 0 {
		return a.taskFail(*asJSON, c.Path, "", fmt.Errorf("%w: want a task ID and nonnegative run and cursor", errBadTaskRequest))
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	st := a.tasks(cfg)
	defer st.Close()
	for {
		out, err := st.Logs(ctx, id, *n, *from)
		if err != nil {
			return a.taskFail(*asJSON, c.Path, "", err)
		}
		if *asJSON {
			return a.reply(hibapi.Response{TaskReply: hibapi.TaskReply{TaskLog: &out}})
		}
		if _, err := a.Stdout.Write(out.Data); err != nil {
			return a.taskFail(false, c.Path, "", err)
		}
		if len(out.Data) == 0 || out.Done {
			return 0
		}
		*n, *from = out.Run, out.Next
	}
}

// taskRequest is hibd task cancel, rescue and rm. It records a request row and answers. The
// daemon carries it out at the run's next step boundary or poll, and show says when it has.
func (a *App) taskRequest(ctx context.Context, c taskCommand, args []string) int {
	return a.recordTaskRequest(ctx, c, args)
}

// taskCleanup is hibd task cleanup.
func (a *App) taskCleanup(ctx context.Context, c taskCommand, args []string) int {
	return a.taskNotYet(c, args)
	// TODO: under cleanup.lock, list blobs and log files, then delete those no task row names,
	// rechecking references in one short transaction per batch.
}

// recipe is every hibd recipe leaf. M15 adds recipes.json.
func (a *App) recipe(ctx context.Context, c taskCommand, args []string) int {
	return a.taskNotYet(c, args)
}

func (a *App) taskNotYet(c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parseAny(fl, args); !ok {
		return 2
	}
	return a.taskFail(*asJSON, c.Path, "", errTasksNotImplemented)
}

// tasks returns the task records in cfg's state directory. tasks.db opens only when a method
// needs it, so commands on people's machines never open it.
func (a *App) tasks(cfg config) *tasks.Tasks {
	return tasks.Open(statefile.Dir{Path: cfg.stateDir, EUID: a.EUID}, a.now)
}

// taskMachines is Manager as tasks.Machines. Fits, Find, Create and Resume are Manager's task
// methods. Pause fences the kept machine; Remove fences and kills every task machine by ID.
// Both close links the way hibd pause and hibd rm do, and e, the Owner, freezes before the pause.
type taskMachines struct {
	a   *App
	cfg config
	e   engine.Engine
	m   *machines.Manager
}

func (t taskMachines) Fits(ctx context.Context) (func(task.Run) error, error) {
	return t.m.TaskFits(ctx)
}

func (t taskMachines) Find(ctx context.Context, r task.Run) (engine.Machine, bool, error) {
	return t.m.FindTask(ctx, r)
}

func (t taskMachines) Create(ctx context.Context, r task.Run, timeout time.Duration) (engine.Machine, error) {
	if err := t.a.awake(); err != nil {
		return engine.Machine{}, err
	}
	return t.m.CreateTask(ctx, r, timeout)
}

func (t taskMachines) Resume(ctx context.Context, r task.Run, id string, timeout time.Duration) (engine.Machine, error) {
	if err := t.a.awake(); err != nil {
		return engine.Machine{}, err
	}
	return t.m.ResumeTask(ctx, r, id, timeout)
}

func (t taskMachines) Pause(ctx context.Context, r task.Run) error {
	em, found, err := t.m.FindTask(ctx, r)
	if err != nil || !found {
		return err
	}
	name := string(r.ID.Task)
	return t.a.pauseMachine(ctx, t.cfg, t.e, em, "keep "+name, "pause "+name)
}

func (t taskMachines) Remove(ctx context.Context, r task.Run) error {
	em, found, err := t.m.FindTask(ctx, r)
	if err != nil {
		return err
	}
	name := string(r.ID.Task)
	if !found {
		em.ID = r.MachineID()
	}
	if em.ID != "" {
		if err := t.a.pauseFenced(ctx, t.cfg, em.ID, name, "rm", "rm "+name, func(ctx context.Context) error {
			return t.m.RemoveID(ctx, em.ID)
		}); err != nil {
			return fmt.Errorf("removing task machine %s (%s): %w", name, em.ID, err)
		}
		t.a.dropRemovedLink(ctx, t.cfg, em.ID, name)
	}
	_, found, err = t.m.FindTask(ctx, task.Run{ID: r.ID, Kept: em.ID})
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("a machine for %s appeared during removal: try again", name)
	}
	return nil
}

var _ tasks.Machines = taskMachines{}
