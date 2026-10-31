package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks"
)

func taskReadReply(t *testing.T, a *App, out *bytes.Buffer, args ...string) (int, hibapi.Response) {
	t.Helper()
	out.Reset()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	code := a.Run(ctx, append(args, "-json"))
	var r hibapi.Response
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("hibd %v = exit %d, stdout %q, decode %v", args, code, out, err)
	}
	if err := r.TaskReply.Check(); err != nil {
		t.Fatalf("hibd %v reply fails Check: %v", args, err)
	}
	return code, r
}

func taskReadStore(t *testing.T, a *App) *tasks.Tasks {
	t.Helper()
	st := tasks.Open(statefile.Dir{Path: a.Getenv("HIBD_STATE_DIR"), EUID: os.Geteuid}, time.Now)
	t.Cleanup(func() { st.Close() })
	return st
}

func submitReadTask(t *testing.T, st *tasks.Tasks, key string, at time.Time) task.Receipt {
	t.Helper()
	rec, err := st.Submit(t.Context(), task.Submission{RequestID: key, At: at,
		Spec: task.Spec{Repo: task.Repo{Owner: "acme", Name: key}, Command: []string{"echo", key},
			Image: "dev", Size: "small", Timeout: time.Hour, Publish: task.PublishNone},
		Actor: task.Actor{Owner: "martin", Name: "martin", Source: "cli"}}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestTaskLsEmpty(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	code, r := taskReadReply(t, a, out, "task", "ls")
	if code != 0 || r.Error != "" || r.Tasks == nil || len(r.Tasks) != 0 {
		t.Fatalf("empty task ls = exit %d, %+v; want tasks []", code, r)
	}
}

type taskReadEngine struct {
	*fake.Engine
	listErr error
	lists   int
	onList  func()
}

func (e *taskReadEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	e.lists++
	if e.onList != nil {
		e.onList()
	}
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.Engine.List(ctx, opts)
}

func TestTaskLsStoredSnapshot(t *testing.T) {
	e := &taskReadEngine{Engine: fake.New()}
	a, out, _ := newApp(t, e)
	st := taskReadStore(t, a)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	old := submitReadTask(t, st, "old", at)
	newer := submitReadTask(t, st, "new", at.Add(time.Hour))
	e.onList = func() {
		e.onList = nil
		submitReadTask(t, st, "during-list", at.Add(2*time.Hour))
	}
	code, r := taskReadReply(t, a, out, "task", "ls")
	if code != 0 || len(r.Tasks) != 2 || r.Tasks[0].ID != newer.Run.Task || r.Tasks[1].ID != old.Run.Task {
		t.Fatalf("task ls = exit %d, %+v; want stored tasks newest first", code, r)
	}
	if e.lists != 1 {
		t.Errorf("task ls made %d machine lists, want 1", e.lists)
	}
	for i, want := range []struct {
		repo string
		at   time.Time
	}{{"acme/new", at.Add(time.Hour)}, {"acme/old", at}} {
		got := r.Tasks[i]
		if got.Repo != want.repo || !got.Created.Equal(want.at) || got.Machine != task.MachineUnknown ||
			len(got.Runs) != 1 || got.Runs[0].N != 1 || got.Runs[0].Phase != task.PhaseQueued ||
			!reflect.DeepEqual(got.Runs[0].Command, []string{"echo", strings.TrimPrefix(want.repo, "acme/")}) ||
			len(got.Requests) != 0 || got.Removal != nil {
			t.Errorf("listed task = %+v, want repo, created time and only queued run", got)
		}
	}
}

func TestTaskReadOfMachineStates(t *testing.T) {
	id := task.RunID{Task: "t-aaaaaaaa", N: 2}
	run := task.Run{ID: id, Spec: task.Spec{Command: []string{"true"}, Repo: task.Repo{Owner: "acme", Name: "api"}},
		Log: []task.Checkpoint{{By: "daemon", Body: &task.MachineIntent{}},
			{By: "daemon", Body: &task.MachineResult{ID: "recorded"}},
			{By: "daemon", Body: &task.StageIntent{Stage: task.StageCommand}},
			{By: "daemon", Body: &task.StageLaunched{Stage: task.StageCommand}}}}
	replaced := run
	replaced.Kept = "kept"
	for _, tc := range []struct {
		name string
		run  task.Run
		ms   []hibapi.Machine
		want task.MachineState
	}{
		{"running", run, []hibapi.Machine{{ID: "recorded", State: "running"}}, task.MachineRunning},
		{"paused", run, []hibapi.Machine{{ID: "recorded", State: "paused"}}, task.MachinePaused},
		{"gone", run, []hibapi.Machine{{ID: "other", State: "running"}}, task.MachineGone},
		{"kept before resume", task.Run{ID: id, Kept: "kept"}, []hibapi.Machine{{ID: "kept", State: "paused"}}, task.MachinePaused},
		{"recorded replaces kept", replaced, []hibapi.Machine{
			{ID: "kept", State: "paused"}, {ID: "recorded", State: "running"},
		}, task.MachineRunning},
		{"none recorded", task.Run{ID: id}, []hibapi.Machine{{ID: "unrecorded", Name: string(id.Task), State: "running"}}, task.MachineUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := task.Task{ID: id.Task, Runs: []task.Run{tc.run}}
			out := taskReadOf(record, "daemon", tc.ms, true)
			if err := out.Check(); err != nil {
				t.Fatal(err)
			}
			if out.Machine != tc.want {
				t.Fatalf("machine = %q, want %q", out.Machine, tc.want)
			}
			if tc.name == "paused" && (out.Runs[0].Phase != task.PhasePaused || out.Runs[0].Held != task.ErrHeld.Error()) {
				t.Errorf("paused run = %+v, want phase paused and held reason", out.Runs[0])
			}
			if tc.run.Machine != task.MachineUnknown {
				t.Fatal("read conversion mutated the stored run")
			}
		})
	}
}

func TestTaskReadOfHistoryAndRemoval(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	id := task.ID("t-aaaaaaaa")
	first := task.Run{ID: task.RunID{Task: id, N: 1}, Submitted: at, Spec: task.Spec{Publish: task.PublishNone},
		Log: []task.Checkpoint{{Body: &task.KeepResult{}}}}
	second := task.Run{ID: task.RunID{Task: id, N: 2}, Submitted: at.Add(time.Hour),
		Log: []task.Checkpoint{{By: "old-daemon", Body: &task.MachineIntent{}}}}
	record := task.Task{ID: id, Created: at, Runs: []task.Run{first, second}, Requests: []task.Request{
		{ID: 1, Kind: task.RequestCancel, Run: 1, At: at, Actor: task.Actor{Name: "martin", Source: "cli"}},
		{ID: 2, Kind: task.RequestRemove, Run: 0, At: at.Add(time.Hour), Actor: task.Actor{Name: "root", Source: "cli"}}}}
	for _, state := range []string{"absent", "open", "done"} {
		t.Run(state, func(t *testing.T) {
			record.Removal = nil
			if state != "absent" {
				record.Removal = &task.Removal{Why: "rm", At: at}
				if state == "done" {
					record.Removal.Done = at.Add(2 * time.Hour)
				}
			}
			out := taskReadOf(record, "current-daemon", nil, true)
			if err := out.Check(); err != nil {
				t.Fatal(err)
			}
			if len(out.Runs) != 2 || out.Runs[0].N != 1 || out.Runs[0].Outcome != task.Succeeded ||
				out.Runs[1].N != 2 || out.Runs[1].Phase != task.PhaseRecovering {
				t.Fatalf("show history = %+v, want ended first run then recovering follow-up", out.Runs)
			}
			if len(out.Requests) != 2 || out.Requests[0].ID != 1 || out.Requests[1].ID != 2 ||
				out.Requests[0].Run != 1 || out.Requests[1].Run != 0 ||
				out.Requests[0].Actor.Name != "martin" || out.Requests[0].Actor.Source != "cli" ||
				!out.Requests[0].At.Equal(at) {
				t.Fatalf("show requests = %+v, want oldest first with actor and time", out.Requests)
			}
			if (out.Removal == nil) != (state == "absent") {
				t.Fatalf("show removal = %+v for %s", out.Removal, state)
			}
			if out.Removal != nil && (out.Removal.Why != "rm" || !out.Removal.At.Equal(at) ||
				(out.Removal.Done == nil) != (state != "done")) {
				t.Fatalf("show removal = %+v for %s", out.Removal, state)
			}
			if state == "done" && !out.Removal.Done.Equal(record.Removal.Done) {
				t.Fatalf("removal done = %v, want %v", out.Removal.Done, record.Removal.Done)
			}
			listed := taskReadOf(record, "current-daemon", nil, false)
			if len(listed.Runs) != 1 || listed.Runs[0].N != 2 || len(listed.Requests) != 0 || listed.Removal != nil {
				t.Fatalf("ls history = %+v, want only latest run", listed)
			}
			var outText bytes.Buffer
			a := &App{Stdout: &outText, Now: func() time.Time { return at.Add(time.Minute) }}
			if err := a.printTaskList([]hibapi.Task{taskReadOf(task.Task{ID: id, Created: at, Runs: []task.Run{first}}, "", nil, false)}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(outText.String(), "succeeded") {
				t.Fatalf("ended task table = %q, want outcome in PHASE", &outText)
			}
		})
	}
}

func TestTaskReadsE2BFailure(t *testing.T) {
	e := &taskReadEngine{Engine: fake.New(), listErr: errors.New("E2B unreachable \x1b[31m")}
	a, out, errOut := newApp(t, e)
	rec := submitReadTask(t, taskReadStore(t, a), "read", time.Now())
	_, base := taskReadReply(t, a, out, "ls")
	for _, cmd := range [][]string{{"task", "ls"}, {"task", "show", string(rec.Run.Task)}} {
		code, r := taskReadReply(t, a, out, cmd...)
		if code != 1 || r.Error != base.Error || !strings.Contains(r.Error, "Check E2B on the host") || strings.ContainsRune(r.Error, '\x1b') {
			t.Errorf("%v = exit %d, error %q, want ls error %q and its safe hint", cmd, code, r.Error, base.Error)
		}
		out.Reset()
		errOut.Reset()
		if code := a.Run(t.Context(), cmd); code != 1 || !strings.Contains(errOut.String(), base.Error) {
			t.Errorf("plain %v = exit %d, stderr %q, want same E2B error and hint", cmd, code, errOut)
		}
	}
}

func TestTaskShowAndReceipt(t *testing.T) {
	e := &taskReadEngine{Engine: fake.New()}
	a, out, _ := newApp(t, e)
	st := taskReadStore(t, a)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	rec := submitReadTask(t, st, "submission", at)
	if _, err := st.Request(t.Context(), tasks.Request{RequestID: "cancel", Task: rec.Run.Task,
		Kind: task.RequestCancel, At: at, Actor: task.Actor{Name: "martin", Source: "cli"}}); err != nil {
		t.Fatal(err)
	}
	for _, selectBy := range [][]string{{string(rec.Run.Task)}, {"-request", "submission"}} {
		t.Run(selectBy[0], func(t *testing.T) {
			before := e.lists
			code, r := taskReadReply(t, a, out, append([]string{"task", "show"}, selectBy...)...)
			if code != 0 || r.Task == nil || r.Task.ID != rec.Run.Task || len(r.Task.Runs) != 1 ||
				len(r.Task.Requests) != 1 || r.Task.Requests[0].Kind != task.RequestCancel || r.Task.Runs[0].Phase != task.PhaseQueued {
				t.Fatalf("show %v = exit %d, %+v, want submitted task and request", selectBy, code, r)
			}
			if e.lists != before+1 {
				t.Errorf("show lists = %d, want one", e.lists-before)
			}
		})
	}
	for _, tc := range []struct {
		args []string
		code hibapi.ErrorCode
	}{
		{[]string{"t-aaaaaaaa"}, hibapi.CodeNoTask},
		{[]string{"invalid"}, hibapi.CodeBadRequest},
		{[]string{"-request", "missing"}, hibapi.CodeNoTask},
	} {
		code, r := taskReadReply(t, a, out, append([]string{"task", "show"}, tc.args...)...)
		if code != 1 || r.Code != tc.code {
			t.Errorf("show %v = exit %d, code %s, want %s", tc.args, code, r.Code, tc.code)
		}
	}
}

func TestTaskReadsDoNotLockOrRemoveDuplicates(t *testing.T) {
	e := fake.New()
	a, out, _ := newApp(t, e)
	rec := submitReadTask(t, taskReadStore(t, a), "read", time.Now())
	for _, id := range []string{"one", "two"} {
		e.Add(engine.Machine{ID: id, State: engine.Running,
			Metadata: map[string]string{"hib.name": string(rec.Run.Task), task.Label: string(rec.Run.Task)}})
	}
	unlock, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), "hibd.lock", "other writer")
	if err != nil || !ok {
		t.Fatalf("hold hibd.lock = %v, %v", ok, err)
	}
	defer unlock()
	for _, args := range [][]string{{"task", "ls"}, {"task", "show", string(rec.Run.Task)}} {
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		out.Reset()
		code := a.Run(ctx, append(args, "-json"))
		expired := ctx.Err()
		cancel()
		var reply hibapi.Response
		if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if err := reply.TaskReply.Check(); err != nil {
			t.Fatal(err)
		}
		if code != 0 || expired != nil {
			t.Fatalf("%v = exit %d, %v, %q, want read without waiting on lock", args, code, expired, out)
		}
		if reply.Task != nil && reply.Task.Machine != task.MachineUnknown ||
			len(reply.Tasks) > 0 && reply.Tasks[0].Machine != task.MachineUnknown {
			t.Fatalf("unrecorded machines gained state: %+v", reply)
		}
	}
	ms, err := e.List(t.Context(), engine.ListOptions{})
	if err != nil || len(ms) != 2 {
		t.Fatalf("machines after reads = %v, %v, want both duplicates untouched", ms, err)
	}
}

func TestTaskLogsCommandEdges(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	rec := submitReadTask(t, taskReadStore(t, a), "read", time.Now())
	for _, tc := range []struct {
		args []string
		want hibapi.ErrorCode
	}{
		{[]string{"t-aaaaaaaa"}, hibapi.CodeNoTask},
		{[]string{string(rec.Run.Task), "-from", "1"}, hibapi.CodeBadRequest},
		{[]string{string(rec.Run.Task), "-run", "2"}, hibapi.CodeBadRequest},
		{[]string{string(rec.Run.Task), "-from", "-1"}, hibapi.CodeBadRequest},
		{[]string{"invalid"}, hibapi.CodeBadRequest},
	} {
		code, r := taskReadReply(t, a, out, append([]string{"task", "logs"}, tc.args...)...)
		if code != 1 || r.Code != tc.want {
			t.Errorf("logs %v = exit %d, code %s, want %s", tc.args, code, r.Code, tc.want)
		}
	}
	code, r := taskReadReply(t, a, out, "task", "logs", string(rec.Run.Task))
	if code != 0 || r.TaskLog == nil || r.TaskLog.Run != 1 || r.TaskLog.From != 0 ||
		r.TaskLog.Next != 0 || r.TaskLog.Done || len(r.TaskLog.Data) != 0 {
		t.Fatalf("queued logs = exit %d, %+v, want empty next 0 not done", code, r)
	}
}

func TestTaskRequestsRecordAndReplay(t *testing.T) {
	for _, cmd := range []string{"cancel", "rescue", "rm"} {
		t.Run(cmd, func(t *testing.T) {
			e := fake.New()
			a, out, _ := newApp(t, e)
			baseEnv := a.Getenv
			a.Getenv = func(k string) string {
				if k == "SUDO_USER" {
					return "martin"
				}
				return baseEnv(k)
			}
			st := taskReadStore(t, a)
			rec := submitReadTask(t, st, "read", time.Now())
			e.Add(engine.Machine{ID: "untouched", State: engine.Paused,
				Metadata: map[string]string{"hib.name": string(rec.Run.Task), task.Label: string(rec.Run.Task)}})
			kind := map[string]task.RequestKind{"cancel": task.RequestCancel, "rescue": task.RequestRescue, "rm": task.RequestRemove}[cmd]
			var first *hibapi.TaskRequest
			for range 2 {
				code, r := taskReadReply(t, a, out, "task", cmd, string(rec.Run.Task), "-request-id", "retry")
				if code != 0 || r.RequestID != "retry" || r.Request == nil || r.Request.Kind != kind ||
					r.Request.Run != 1 || r.Request.Actor.Name != "martin" || r.Request.Actor.Source != "cli" || r.Request.At.IsZero() {
					t.Fatalf("%s = exit %d, %+v, want recorded %s run 1 and request ID", cmd, code, r, kind)
				}
				if first != nil && !reflect.DeepEqual(first, r.Request) {
					t.Fatalf("replayed request = %+v, want %+v", r.Request, first)
				}
				first = r.Request
			}
			var keys []string
			for range 2 {
				code, r := taskReadReply(t, a, out, "task", cmd, string(rec.Run.Task))
				if code != 0 || r.RequestID == "" || r.Request == nil {
					t.Fatalf("generated request = exit %d, %+v, want echoed fresh ID", code, r)
				}
				keys = append(keys, r.RequestID)
			}
			if keys[0] == keys[1] {
				t.Fatal("requests without IDs reused the same ID")
			}
			code, show := taskReadReply(t, a, out, "task", "show", string(rec.Run.Task))
			if code != 0 || show.Task == nil || len(show.Task.Requests) != 3 || !reflect.DeepEqual(show.Task.Requests[0], *first) {
				t.Fatalf("show after requests = exit %d, %+v, want exactly three requests", code, show)
			}
			for _, requestID := range []string{"", strings.Repeat("x", 129)} {
				code, r := taskReadReply(t, a, out, "task", cmd, string(rec.Run.Task), "-request-id", requestID)
				if code != 1 || r.Code != hibapi.CodeBadRequest {
					t.Errorf("bad request ID = exit %d, code %s, want BAD_REQUEST", code, r.Code)
				}
			}
			code, r := taskReadReply(t, a, out, "task", cmd, "malformed")
			if code != 1 || r.Code != hibapi.CodeBadRequest {
				t.Errorf("malformed task = exit %d, code %s, want BAD_REQUEST", code, r.Code)
			}
			record, err := st.Task(t.Context(), rec.Run.Task)
			if err != nil || len(record.Requests) != 3 || record.Removal != nil || record.Runs[0].Ended() {
				t.Errorf("request command acted instead of recording: %+v, %v", record, err)
			}
			if m, err := e.Get(t.Context(), "untouched"); err != nil || m.State != engine.Paused {
				t.Errorf("request command touched machine: %+v, %v, want paused", m, err)
			}
		})
	}
}

func TestTaskRequestRejectsInvalidUTF8(t *testing.T) {
	for _, cmd := range []string{"cancel", "rescue", "rm"} {
		t.Run(cmd, func(t *testing.T) {
			a, out, _ := newApp(t, fake.New())
			st := taskReadStore(t, a)
			rec := submitReadTask(t, st, "invalid-request", time.Now())
			code, reply := taskReadReply(t, a, out, "task", cmd, string(rec.Run.Task),
				"-request-id", "bad\xff\xfeutf")
			if code != 1 || reply.Code != hibapi.CodeBadRequest || reply.Request != nil {
				t.Errorf("non-UTF-8 request = exit %d, %+v, want BAD_REQUEST", code, reply)
			}
			record, err := st.Task(t.Context(), rec.Run.Task)
			if err != nil || len(record.Requests) != 0 {
				t.Errorf("invalid request saved %d requests, %v, want none", len(record.Requests), err)
			}
		})
	}
}

func TestTaskStartDecodesInvalidUTF8(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	a.Stdin = strings.NewReader("{\"requestId\":\"bad\xff\xfeutf\",\"command\":[\"true\"]}\n")
	code, reply := taskReadReply(t, a, out, "task", "start")
	const decoded = "bad\ufffd\ufffdutf"
	if code != 0 || reply.Task == nil || reply.RequestID != decoded {
		t.Fatalf("JSON request = exit %d, %+v, want decoded UTF-8 request ID", code, reply)
	}
	st := taskReadStore(t, a)
	rec, err := st.Receipt(t.Context(), decoded)
	if err != nil || rec.Run.Task != reply.Task.ID || rec.RequestID != decoded {
		t.Fatalf("decoded receipt = %+v, %v, want JSON-decoded request ID", rec, err)
	}
}

func TestTaskShowPlainRemovalDone(t *testing.T) {
	for _, finished := range []bool{false, true} {
		t.Run(fmt.Sprint("finished=", finished), func(t *testing.T) {
			a, out, _ := newApp(t, fake.New())
			at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			record := task.Task{ID: "t-aaaaaaaa", Created: at,
				Removal: &task.Removal{Why: "rm", At: at}}
			if finished {
				record.Removal.Done = at.Add(time.Hour)
			}
			if err := a.printTask(taskReadOf(record, "", nil, true)); err != nil {
				t.Fatal(err)
			}
			want := "removal rm at 2026-10-28T12:00:00Z"
			if finished {
				want += " done at 2026-10-28T13:00:00Z"
			}
			if !strings.HasSuffix(out.String(), want+"\n") {
				t.Fatalf("plain task removal = %q, want final line %q", out, want)
			}
		})
	}
}

func TestTaskFailNotLive(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	if code := a.taskFail(true, "task cancel", "request", fmt.Errorf("cancel: %w", tasks.ErrNotLive)); code != 1 {
		t.Fatalf("not-live failure exit %d, want 1", code)
	}
	var r hibapi.Response
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if err := r.TaskReply.Check(); err != nil {
		t.Fatal(err)
	}
	if r.Code != hibapi.CodeNotLive || r.RequestID != "request" || r.Request != nil {
		t.Fatalf("not-live reply = %+v, want NOT_LIVE and no request", r)
	}
}

func TestTaskFailTerminalSafe(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	failure := errors.New("E2B unreachable \x1b[31munsafe\x1b[0m")
	a.fail(false, "ls", failure)
	want := strings.TrimSuffix(strings.TrimPrefix(errOut.String(), "hibd ls: "), "\n")
	if code := a.taskFail(true, "task ls", "", failure); code != 1 {
		t.Fatalf("taskFail exit %d, want 1", code)
	}
	var reply hibapi.Response
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if err := reply.TaskReply.Check(); err != nil {
		t.Fatal(err)
	}
	if reply.Error != want || strings.ContainsRune(reply.Error, '\x1b') || !strings.Contains(reply.Error, "Check E2B on the host") {
		t.Fatalf("taskFail JSON error = %q, want safe ls error %q with next step", reply.Error, want)
	}
}

func TestTaskRequestBoundary(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	st := taskReadStore(t, a)
	rec := submitReadTask(t, st, "boundary", time.Now())
	for _, key := range []string{"x", strings.Repeat("x", 128)} {
		code, reply := taskReadReply(t, a, out, "task", "cancel", string(rec.Run.Task), "-request-id", key)
		if code != 0 || reply.RequestID != key || reply.Request == nil || reply.Request.Actor.Name != "root" {
			t.Fatalf("request key of %d bytes = exit %d, %+v, want recorded request by root", len(key), code, reply)
		}
	}
	for _, key := range []string{"", strings.Repeat("x", 129), strings.Repeat("\u00e9", 65)} {
		code, reply := taskReadReply(t, a, out, "task", "cancel", string(rec.Run.Task), "-request-id", key)
		if code != 1 || reply.Code != hibapi.CodeBadRequest {
			t.Errorf("request key of %d bytes = exit %d, code %s, want BAD_REQUEST", len(key), code, reply.Code)
		}
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"task", "cancel", string(rec.Run.Task), "-request-id", "unsafe\x1b[31m"}); code != 0 ||
		strings.ContainsRune(out.String(), '\x1b') || errOut.Len() != 0 {
		t.Errorf("plain request = exit %d, stdout %q, stderr %q, want terminal-safe success", code, out, errOut)
	}
	record, err := st.Task(t.Context(), rec.Run.Task)
	if err != nil || len(record.Requests) != 3 {
		t.Errorf("boundary requests = %d, %v, want exactly three accepted requests", len(record.Requests), err)
	}
}

func TestTaskQueuedRequests(t *testing.T) {
	for _, cmd := range []string{"cancel", "rescue", "rm"} {
		t.Run(cmd, func(t *testing.T) {
			a, out, _ := newApp(t, fake.New())
			rec := submitReadTask(t, taskReadStore(t, a), "queued", time.Now())
			code, reply := taskReadReply(t, a, out, "task", cmd, string(rec.Run.Task))
			kind := map[string]task.RequestKind{"cancel": task.RequestCancel, "rescue": task.RequestRescue, "rm": task.RequestRemove}[cmd]
			if code != 0 || reply.RequestID == "" || reply.Request == nil ||
				reply.Request.Kind != kind || reply.Request.Run != 1 {
				t.Fatalf("queued %s = exit %d, request %+v, key %q, error %q; want recorded %s run 1",
					cmd, code, reply.Request, reply.RequestID, reply.Error, kind)
			}
		})
	}
}

func TestTaskReadsPlain(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	rec := submitReadTask(t, taskReadStore(t, a), "read", time.Now())
	for _, cmd := range []string{"ls", "show", "cancel", "rescue", "rm"} {
		args := []string{"task", cmd}
		if cmd != "ls" {
			args = append(args, string(rec.Run.Task))
		}
		out.Reset()
		if code := a.Run(t.Context(), args); code != 0 || out.Len() == 0 || errOut.Len() != 0 {
			t.Fatalf("plain %s = exit %d, stdout %q, stderr %q; want readable success", cmd, code, out, errOut)
		}
		if cmd == "ls" {
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "TASK RUN PHASE STAGE REPO AGE" ||
				!slices.Equal(strings.Fields(lines[1])[:5], []string{string(rec.Run.Task), "1", "queued", "-", "acme/read"}) {
				t.Fatalf("task table = %q, want TASK RUN PHASE STAGE REPO AGE and queued row", out)
			}
		}
	}
}

func TestTaskRemoveDropsLinkEntry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		on      bool
		dropErr error
	}{
		{name: "on", on: true}, {name: "off"}, {name: "drop fails", on: true, dropErr: errors.New("link state unwritable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fake.New()
			e.Add(engine.Machine{ID: "recorded", State: engine.Paused,
				Metadata: map[string]string{"hib.name": "t-aaaaaaaa", task.Label: "t-aaaaaaaa"}})
			f := &fakeLinks{doc: links.Doc{Machines: map[string]*links.Machine{"recorded": {}, "other": {}}}, updateErr: tc.dropErr}
			a, _, errOut := newApp(t, e)
			withLinks(a, f, tc.on)
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}

			m := machines.New(machines.Config{Engine: e, StateDir: cfg.stateDir, Fenced: a.snapshotFence(cfg)})
			tm := taskMachines{a: a, cfg: cfg, e: e, m: m}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := tm.Remove(ctx, task.Run{ID: task.RunID{Task: "t-aaaaaaaa", N: 1}, Kept: "recorded"}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Get(ctx, "recorded"); !errors.Is(err, engine.ErrNotFound) {
				t.Fatalf("removed machine still exists: %v", err)
			}
			_, present := f.entry("recorded")
			if tc.on && tc.dropErr == nil && present {
				t.Error("task removal left its link entry")
			}
			if _, present := f.entry("other"); !present {
				t.Error("task removal dropped another entry")
			}
			if !tc.on && len(f.log()) != 0 {
				t.Errorf("links-off removal opened link state: %v", f.log())
			}
			if tc.dropErr != nil && !strings.Contains(errOut.String(), "couldn't drop its entry") {
				t.Errorf("failed link drop warning = %q, want warning with removal success", errOut)
			}
		})
	}
}

func TestTaskRemovalDocs(t *testing.T) {
	data, err := os.ReadFile("../../SKETCH.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, want := range []string{
		"The removal after `hib task rm` calls `Manager.RemoveID`",
		"**Pause and removal.**",
		"removes the run's recorded ID through `pauseFenced` and `Manager.RemoveID`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("SKETCH lacks %q", want)
		}
	}
	if strings.Contains(doc, "Removing a task machine must also drop its link entry") {
		t.Error("SKETCH still lists the resolved link-entry TODO")
	}
}
