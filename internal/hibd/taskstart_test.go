package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks"
)

func taskRequestInput(t *testing.T, req hibapi.TaskStart, data io.Reader) io.Reader {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return io.MultiReader(bytes.NewReader(append(b, '\n')), data)
}

func submitTaskJSON(t *testing.T, a *App, req hibapi.TaskStart, data io.Reader) (int, hibapi.Response) {
	t.Helper()
	cmd := *a
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stdin = taskRequestInput(t, req, data)
	args := []string{"task", "start", "-json"}
	if req.Task != "" {
		args = []string{"task", "run", string(req.Task), "-json"}
	}
	code := cmd.Run(t.Context(), args)
	var r hibapi.Response
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("task reply exit %d: %s, %v", code, &out, err)
	}
	return code, r
}

func savedTasks(t *testing.T, a *App) *tasks.Tasks {
	t.Helper()
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	st := a.tasks(cfg)
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

func seedTask(t *testing.T, a *App, req hibapi.TaskStart, data io.Reader) task.Receipt {
	t.Helper()
	sub, err := submissionOf(req, task.Actor{Owner: "root", Name: "root", Source: "cli"}, a.now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := savedTasks(t, a).Submit(t.Context(), sub, data)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func requireTaskReply(t *testing.T, code int, r hibapi.Response, request string, n int) hibapi.Task {
	t.Helper()
	if code != 0 || r.Error != "" || r.RequestID != request || r.Task == nil {
		t.Fatalf("task submission exit %d, request %q, task %+v, error %q; want saved task", code, r.RequestID, r.Task, r.Error)
	}
	if _, err := task.ParseID(string(r.Task.ID)); err != nil {
		t.Fatal(err)
	}
	if len(r.Task.Runs) != 1 || r.Task.Runs[0].N != n || r.Task.Runs[0].Phase != task.PhaseQueued {
		t.Fatalf("receipt runs %+v; want only run %d queued", r.Task.Runs, n)
	}
	return *r.Task
}

func requireTaskError(t *testing.T, code int, r hibapi.Response, request string, want hibapi.ErrorCode, retry bool) {
	t.Helper()
	if code != 1 || r.Error == "" || r.RequestID != request || r.Code != want || r.Retryable != retry || r.Task != nil {
		t.Fatalf("task failure exit %d, request %q, code %q, retryable %v, error %q; want %s retryable %v", code, r.RequestID, r.Code, r.Retryable, r.Error, want, retry)
	}
}

func TestTaskStartSavesInputs(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	a.Now = func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }
	prompt, file := []byte("fix this\n\x00\xff"), []byte{0, 1, '\n', 255}
	req := hibapi.TaskStart{RequestID: "save-inputs", Repo: "solev/hibernaut", Command: []string{"true"},
		Prompt: &hibapi.TaskFile{Size: int64(len(prompt))},
		Files:  []hibapi.TaskFile{{Name: "data.bin", Size: int64(len(file))}}}
	code, r := submitTaskJSON(t, a, req, bytes.NewReader(append(prompt, file...)))
	reply := requireTaskReply(t, code, r, req.RequestID, 1)
	got, err := savedTasks(t, a).Task(t.Context(), reply.ID)
	if err != nil || len(got.Runs) != 1 || !got.Created.Equal(a.now()) || reply.Repo != req.Repo {
		t.Fatalf("saved task %+v, %v; reply %+v", got, err, reply)
	}
	for i, want := range [][]byte{prompt, file} {
		f := got.Runs[0].Spec.Inputs[i]
		b, err := os.ReadFile(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "tasks", "blobs", f.Hash.String()))
		if err != nil || !bytes.Equal(b, want) {
			t.Fatalf("input %s = %q, %v; want %q", f.Name, b, err, want)
		}
	}
}

func TestTaskStartNeverOpensEngine(t *testing.T) {
	a, _, _ := newApp(t, nil)
	a.NewEngine = func() (engine.Engine, error) {
		t.Error("submission opened E2B")
		return nil, errors.New("E2B unreachable")
	}
	req := hibapi.TaskStart{RequestID: "offline", Command: []string{"true"}}
	code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
	requireTaskReply(t, code, r, req.RequestID, 1)
}

func TestTaskStartReplaysReceipt(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	req := hibapi.TaskStart{RequestID: "replay", Command: []string{"true"},
		Files: []hibapi.TaskFile{{Name: "note", Size: 3}}}
	receipt := seedTask(t, a, req, strings.NewReader("one"))
	code, r := submitTaskJSON(t, a, req, strings.NewReader("one"))
	got := requireTaskReply(t, code, r, req.RequestID, 1)
	if got.ID != receipt.Run.Task {
		t.Fatalf("replayed task %s, want %s", got.ID, receipt.Run.Task)
	}
	all, err := savedTasks(t, a).List(t.Context())
	if err != nil || len(all) != 1 || len(all[0].Runs) != 1 {
		t.Fatalf("replayed records %+v, %v; want one task and one run", all, err)
	}
}

func TestTaskStartConflictsOnChangedBody(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	req := hibapi.TaskStart{RequestID: "conflict", Command: []string{"true"},
		Files: []hibapi.TaskFile{{Name: "note", Size: 3}}}
	seedTask(t, a, req, strings.NewReader("one"))
	code, r := submitTaskJSON(t, a, req, strings.NewReader("two"))
	requireTaskError(t, code, r, req.RequestID, hibapi.CodeConflict, false)
}

func TestTaskRunQueuesNextRun(t *testing.T) {
	a, sim := taskDaemonApp(t)
	req := hibapi.TaskStart{RequestID: "first", Command: []string{"true"}}
	first := seedTask(t, a, req, strings.NewReader(""))
	stop := runTaskDaemon(t, a)
	waitTaskEnded(t, a, first.Run.Task)
	stop()
	req.RequestID, req.Task = "follow-up", first.Run.Task
	code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
	reply := requireTaskReply(t, code, r, req.RequestID, 2)
	got, err := savedTasks(t, a).Task(t.Context(), reply.ID)
	if err != nil || len(got.Runs) != 2 || got.Runs[1].Kept != got.Runs[0].MachineID() || got.Runs[1].Kept == "" {
		t.Fatalf("follow-up %+v, %v; want same task and kept machine", got, err)
	}
	ms, err := sim.List(t.Context(), engine.ListOptions{})
	if err != nil || len(ms) != 1 || ms[0].State != engine.Paused {
		t.Fatalf("machines after follow-up %+v, %v; want one kept machine", ms, err)
	}
}

func TestTaskRunRejectsLiveRun(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	req := hibapi.TaskStart{RequestID: "live", Command: []string{"true"}}
	receipt := seedTask(t, a, req, strings.NewReader(""))
	req.RequestID, req.Task = "live-follow-up", receipt.Run.Task
	code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
	requireTaskError(t, code, r, req.RequestID, hibapi.CodeRunLive, false)
}

func TestTaskRunRejectsMissingTask(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	req := hibapi.TaskStart{RequestID: "missing", Task: "t-aaaaaaaa", Command: []string{"true"}}
	code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
	requireTaskError(t, code, r, req.RequestID, hibapi.CodeNoTask, false)
}

type taskZeroReader struct{}

func (taskZeroReader) Read(b []byte) (int, error) {
	clear(b)
	return len(b), nil
}

func taskBlobs(t *testing.T, a *App, subdir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "tasks", subdir))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}

func TestTaskRunInputFullLeavesNoNewRecordsOrBlobs(t *testing.T) {
	a, _ := taskDaemonApp(t)
	req := hibapi.TaskStart{RequestID: "budget", Command: []string{"true"},
		Files: []hibapi.TaskFile{{Name: "large", Size: task.MaxInputBytes}}}
	first := seedTask(t, a, req, io.LimitReader(taskZeroReader{}, task.MaxInputBytes))
	st := savedTasks(t, a)
	if _, err := st.Request(t.Context(), tasks.Request{RequestID: "cancel-budget",
		Task: first.Run.Task, Kind: task.RequestCancel, At: a.now()}); err != nil {
		t.Fatal(err)
	}
	stop := runTaskDaemon(t, a)
	waitTaskEnded(t, a, first.Run.Task)
	stop()
	before, err := st.Task(t.Context(), first.Run.Task)
	if err != nil {
		t.Fatal(err)
	}
	blobs := taskBlobs(t, a, "blobs")
	req.RequestID, req.Task = "over-budget", first.Run.Task
	req.Files = []hibapi.TaskFile{{Name: "extra", Size: 1}}
	code, r := submitTaskJSON(t, a, req, strings.NewReader("x"))
	requireTaskError(t, code, r, req.RequestID, hibapi.CodeInputFull, false)
	after, err := st.Task(t.Context(), first.Run.Task)
	all, listErr := st.List(t.Context())
	if err != nil || listErr != nil || len(all) != 1 || !reflect.DeepEqual(before, after) ||
		!reflect.DeepEqual(blobs, taskBlobs(t, a, "blobs")) || len(taskBlobs(t, a, "staging")) != 0 {
		t.Fatalf("INPUT_FULL left records or blobs: task %+v, errors %v %v", after, err, listErr)
	}
	if _, err := st.Receipt(t.Context(), req.RequestID); !errors.Is(err, tasks.ErrNoReceipt) {
		t.Fatalf("INPUT_FULL left a receipt: %v", err)
	}
}

func TestTaskStartQueueFullIsRetryable(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	for i := range task.MaxQueuedRuns {
		seedTask(t, a, hibapi.TaskStart{RequestID: fmt.Sprintf("queued-%d", i),
			Command: []string{"true"}}, strings.NewReader(""))
	}
	req := hibapi.TaskStart{RequestID: "queue-full", Command: []string{"true"}}
	code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
	requireTaskError(t, code, r, req.RequestID, hibapi.CodeQueueFull, true)
}

func TestTaskStartShortInputCanRetry(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	req := hibapi.TaskStart{RequestID: "short-input", Command: []string{"true"},
		Files: []hibapi.TaskFile{{Name: "note", Size: 4}}}
	code, r := submitTaskJSON(t, a, req, strings.NewReader("abc"))
	if code != 1 || !strings.Contains(r.Error, "EOF") {
		t.Fatalf("short input exit %d, error %q; want EOF failure", code, r.Error)
	}
	st := savedTasks(t, a)
	all, err := st.List(t.Context())
	if err != nil || len(all) != 0 || len(taskBlobs(t, a, "blobs")) != 0 || len(taskBlobs(t, a, "staging")) != 0 {
		t.Fatalf("short input left data: %+v, %v", all, err)
	}
	code, r = submitTaskJSON(t, a, req, strings.NewReader("abcd"))
	requireTaskReply(t, code, r, req.RequestID, 1)
}

func TestTaskStartTextReceiptAndFailure(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	req := hibapi.TaskStart{RequestID: "text", Command: []string{"true"}}
	a.Stdin = taskRequestInput(t, req, strings.NewReader(""))
	if code := a.Run(context.Background(), []string{"task", "start"}); code != 0 {
		t.Fatalf("text submission exit %d, stderr %q; want saved task", code, errOut)
	}
	fields := strings.Fields(out.String())
	if len(fields) != 3 || fields[1] != "1" || fields[2] != "queued" || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("text receipt %q; want TASK 1 queued on one line", out)
	}
	if _, err := task.ParseID(fields[0]); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	req.Command = []string{"false"}
	a.Stdin = taskRequestInput(t, req, strings.NewReader(""))
	if code := a.Run(t.Context(), []string{"task", "start"}); code != 1 || out.Len() != 0 ||
		!strings.Contains(errOut.String(), "hibd task start: "+task.ErrConflict.Error()) {
		t.Fatalf("text failure exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestTaskVersionAdvertisesTasks(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	code, r, err := runJSON(t, a, out, "version", "-json")
	if code != 0 || err != nil || !strings.Contains(out.String(), `"tasks"`) {
		t.Fatalf("version exit %d, features %v, error %v; want tasks", code, r.Features, err)
	}
}
