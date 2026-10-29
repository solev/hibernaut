package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/hibd"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func logFixture(t *testing.T) (*tasks.Tasks, taskstore.Store, taskstore.FileLogs, task.RunID) {
	t.Helper()
	dir := statefile.Dir{Path: t.TempDir(), EUID: os.Geteuid}
	api := tasks.Open(dir, time.Now)
	t.Cleanup(func() { api.Close() })
	rec, err := api.Submit(t.Context(), task.Submission{RequestID: "start",
		Spec:  task.Spec{Command: []string{"true"}, Publish: task.PublishNone},
		Actor: task.Actor{Owner: "martin", Name: "martin", Source: "cli"}, At: time.Now()}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	st, err := taskstore.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return api, st, taskstore.FileLogs{Dir: dir}, rec.Run
}

func endLogRun(t *testing.T, st taskstore.Store, id task.RunID) {
	t.Helper()
	for i, body := range []task.Body{&task.StopIntent{Outcome: task.Cancelled},
		&task.StopResult{How: "not-started"}, &task.CollectIntent{}, &task.CollectResult{},
		&task.KeepIntent{}, &task.KeepResult{Gone: true}} {
		if err := st.Append(t.Context(), task.Checkpoint{Run: id, Seq: i + 1,
			At: time.Now(), By: "daemon", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
}

func checkedLog(t *testing.T, api *tasks.Tasks, id task.ID, n int, from int64) hibapi.TaskLog {
	t.Helper()
	log, err := api.Logs(t.Context(), id, n, from)
	if err != nil {
		t.Fatal(err)
	}
	if err := (hibapi.TaskReply{TaskLog: &log}).Check(); err != nil {
		t.Fatal(err)
	}
	return log
}

func TestLogsRunStream(t *testing.T) {
	api, st, files, id := logFixture(t)
	setup := []byte("setup\x00\xff\n")
	command := bytes.Repeat([]byte("command\n"), (1<<20)/4)
	finish := []byte("finish\n")
	for i, data := range [][]byte{setup, command, finish} {
		if err := files.Append(id, task.Stages()[i], 0, data); err != nil {
			t.Fatal(err)
		}
	}
	endLogRun(t, st, id)
	var got []byte
	var from int64
	var stages []task.Stage
	for range 10 {
		log := checkedLog(t, api, id.Task, 0, from)
		if log.From != from || log.Run != 1 || log.Next != from+int64(len(log.Data)) || len(log.Data) > 1<<20 {
			t.Fatalf("stream range = %+v", log)
		}
		stages = append(stages, log.Stage)
		got = append(got, log.Data...)
		from = log.Next
		if log.Done {
			break
		}
	}
	want := bytes.Join([][]byte{setup, command, finish}, nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream has %d bytes, want %d setup/command/finish bytes", len(got), len(want))
	}
	if !reflect.DeepEqual(stages, []task.Stage{task.StageSetup, task.StageCommand, task.StageCommand, task.StageFinish}) {
		t.Fatalf("reply stages = %v, want setup command command finish", stages)
	}
	last := checkedLog(t, api, id.Task, 1, from)
	if !last.Done || len(last.Data) != 0 || last.Next != from {
		t.Fatalf("ended stream tail = %+v, want empty and done", last)
	}
}

func TestLogsDoneAndEdges(t *testing.T) {
	api, st, files, id := logFixture(t)
	queued := checkedLog(t, api, id.Task, 0, 0)
	if queued.Done || queued.Next != 0 || len(queued.Data) != 0 {
		t.Fatalf("queued stream = %+v, want empty, next 0, not done", queued)
	}
	for _, args := range []struct {
		n    int
		from int64
	}{{1, 1}, {2, 0}, {-1, 0}, {1, -1}} {
		if _, err := api.Logs(t.Context(), id.Task, args.n, args.from); !errors.Is(err, tasks.ErrLogRange) {
			t.Errorf("Logs(%d, %d) = %v, want ErrLogRange", args.n, args.from, err)
		}
	}
	if _, err := api.Logs(t.Context(), "t-aaaaaaaa", 0, 0); !errors.Is(err, task.ErrNoTask) {
		t.Errorf("unknown task = %v, want ErrNoTask", err)
	}
	if err := files.Append(id, task.StageCommand, 0, []byte("live")); err != nil {
		t.Fatal(err)
	}
	first := checkedLog(t, api, id.Task, 1, 0)
	tail := checkedLog(t, api, id.Task, 1, first.Next)
	if string(first.Data) != "live" || first.Done || tail.Done || tail.From != 4 || tail.Next != 4 || len(tail.Data) != 0 {
		t.Fatalf("live read = %+v, tail = %+v, want live bytes then empty, neither done", first, tail)
	}
	if err := files.Append(id, task.StageFinish, 0, []byte("later")); err != nil {
		t.Fatal(err)
	}
	next := checkedLog(t, api, id.Task, 1, tail.Next)
	if string(next.Data) != "later" || next.Stage != task.StageFinish || next.Next != 9 || next.Done {
		t.Fatalf("growing stream = %+v, want finish bytes at unchanged cursor", next)
	}
	endLogRun(t, st, id)
	if final := checkedLog(t, api, id.Task, 1, 9); !final.Done {
		t.Fatalf("ended stream = %+v, want done", final)
	}
	api2, st2, _, emptyID := logFixture(t)
	endLogRun(t, st2, emptyID)
	if log := checkedLog(t, api2, emptyID.Task, 0, 0); !log.Done || log.Next != 0 || len(log.Data) != 0 {
		t.Fatalf("empty ended stream = %+v, want done from 0", log)
	}
	follow, err := api.Submit(t.Context(), task.Submission{RequestID: "follow", Task: id.Task,
		Spec: task.Spec{Command: []string{"true"}}, At: time.Now()}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if log := checkedLog(t, api, id.Task, 0, 0); log.Run != follow.Run.N || log.Next != 0 || log.Done {
		t.Fatalf("latest stream = %+v, want queued follow-up", log)
	}
	if log := checkedLog(t, api, id.Task, 1, 0); string(log.Data) != "live" {
		t.Fatalf("selected old stream = %+v, want live", log)
	}
}

func TestRequestWithoutLiveRun(t *testing.T) {
	api, st, _, id := logFixture(t)
	endLogRun(t, st, id)
	for _, kind := range []task.RequestKind{task.RequestCancel, task.RequestRescue} {
		if _, err := api.Request(t.Context(), tasks.Request{RequestID: string(kind), Task: id.Task,
			Kind: kind, At: time.Now()}); !errors.Is(err, tasks.ErrNotLive) {
			t.Errorf("%s without live run = %v, want ErrNotLive", kind, err)
		}
	}
	record, err := api.Task(t.Context(), id.Task)
	if err != nil || len(record.Requests) != 0 {
		t.Fatalf("rejected requests left rows = %v, %v", record.Requests, err)
	}
	q, err := api.Request(t.Context(), tasks.Request{RequestID: "rm", Task: id.Task,
		Kind: task.RequestRemove, At: time.Now()})
	if err != nil || q.Run != 0 || q.Kind != task.RequestRemove {
		t.Fatalf("remove without live run = %+v, %v, want remove run 0", q, err)
	}
}

func logApp(dir string, out, errOut *bytes.Buffer) *hibd.App {
	env := map[string]string{"HIBD_STATE_DIR": dir, "HIBD_CONFIG": filepath.Join(dir, "none"),
		"HIBD_LINK_PORT": "0"}
	return &hibd.App{Stdout: out, Stderr: errOut, Stdin: strings.NewReader(""),
		Getenv: func(k string) string { return env[k] }, EUID: os.Geteuid}
}

func TestTaskLogsPlainBytes(t *testing.T) {
	_, st, files, id := logFixture(t)
	chunks := [][]byte{[]byte("setup\x00\xff\n"), bytes.Repeat([]byte("command\n"), (1<<20)/4), []byte("finish\n")}
	for i, data := range chunks {
		if err := files.Append(id, task.Stages()[i], 0, data); err != nil {
			t.Fatal(err)
		}
	}
	endLogRun(t, st, id)
	var out, errOut bytes.Buffer
	a := logApp(files.Dir.Path, &out, &errOut)
	want := bytes.Join(chunks, nil)
	for _, from := range []int{0, 3, len(chunks[0]), len(chunks[0]) + (1 << 20), len(want)} {
		out.Reset()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		code := a.Run(ctx, []string{"task", "logs", string(id.Task), "-from", fmt.Sprint(from)})
		cancel()
		if code != 0 || !bytes.Equal(out.Bytes(), want[from:]) || errOut.Len() != 0 {
			t.Fatalf("plain logs from %d = exit %d, %d bytes, stderr %q; want exactly %d bytes",
				from, code, out.Len(), &errOut, len(want)-from)
		}
	}
}

func TestTaskRmEndedRequest(t *testing.T) {
	api, st, files, id := logFixture(t)
	endLogRun(t, st, id)
	var out, errOut bytes.Buffer
	a := logApp(files.Dir.Path, &out, &errOut)
	for _, cmd := range []string{"cancel", "rescue", "rm"} {
		out.Reset()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		code := a.Run(ctx, []string{"task", cmd, string(id.Task), "-json"})
		cancel()
		var reply hibapi.Response
		if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if err := reply.TaskReply.Check(); err != nil {
			t.Fatal(err)
		}
		if cmd == "rm" {
			if code != 0 || reply.RequestID == "" || reply.Request == nil ||
				reply.Request.Kind != task.RequestRemove || reply.Request.Run != 0 || reply.Request.Actor.Name != "root" {
				t.Fatalf("rm ended task = exit %d, %+v, want recorded remove run 0", code, reply)
			}
		} else if code != 1 || reply.Code != hibapi.CodeNotLive || reply.Request != nil {
			t.Errorf("%s ended task = exit %d, %+v, want NOT_LIVE", cmd, code, reply)
		}
	}
	record, err := api.Task(t.Context(), id.Task)
	if err != nil || len(record.Requests) != 1 || record.Removal != nil {
		t.Fatalf("rm command = %+v, %v, want only one request and no removal", record, err)
	}
}
