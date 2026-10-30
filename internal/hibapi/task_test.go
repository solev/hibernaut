package hibapi_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

func TestTaskRunWire(t *testing.T) {
	t0 := time.Date(2025, 10, 28, 9, 0, 0, 0, time.UTC)
	id := task.RunID{Task: "t-aaaaaaaa", N: 1}
	at := func(b task.Body) task.Checkpoint {
		return task.Checkpoint{Run: id, At: t0, By: "d1", Body: b}
	}
	runs := map[string]task.Run{
		"queued": {ID: id, Submitted: t0},
		"running": {ID: id, Submitted: t0, Machine: task.MachineRunning, Log: []task.Checkpoint{
			at(&task.MachineIntent{}), at(&task.MachineResult{ID: "sbx"}),
			at(&task.UploadIntent{}), at(&task.UploadResult{}),
			at(&task.StageIntent{Stage: task.StageCommand}),
			at(&task.StageLaunched{Stage: task.StageCommand})}},
		"recovering": {ID: id, Submitted: t0, Log: []task.Checkpoint{at(&task.MachineIntent{})}},
	}
	for name, r := range runs {
		w := hibapi.RunOf(r, "d2")
		if err := w.Check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		b, err := json.Marshal(hibapi.Response{V: 1, TaskReply: hibapi.TaskReply{
			Task: &hibapi.Task{ID: id.Task, Runs: []hibapi.TaskRun{w}}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"task":{"id":"t-aaaaaaaa"`) {
			t.Errorf("%s: the task isn't inline in the response: %s", name, b)
		}
	}
	if err := (hibapi.TaskRun{Phase: "sleeping"}).Check(); err == nil {
		t.Error("an unknown phase passed Check")
	}
}

func TestTaskReplyWire(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	q := hibapi.RequestOf(task.Request{ID: 7, Run: 0, Kind: task.RequestRemove,
		At: at, Actor: task.Actor{Owner: "private-owner", Name: "martin", Source: "cli"}})
	reply := hibapi.TaskReply{RequestID: "request-key", Request: &q,
		Task: &hibapi.Task{ID: "t-aaaaaaaa", Machine: task.MachinePaused,
			Runs:     []hibapi.TaskRun{{N: 1, Outcome: task.Succeeded}},
			Requests: []hibapi.TaskRequest{q}, Removal: &hibapi.TaskRemoval{Why: "rm", At: at}},
		TaskLog: &hibapi.TaskLog{Run: 1, Stage: task.StageCommand, From: 9, Next: 12, Data: []byte{0, 0xff, '\n'}, Done: true}}
	data, err := json.Marshal(hibapi.Response{V: 1, TaskReply: reply})
	if err != nil {
		t.Fatal(err)
	}
	var decoded hibapi.Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.TaskReply.Check(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.TaskReply, reply) {
		t.Fatalf("embedded task reply lost fields: %s", data)
	}
	for _, want := range []string{`"requestId":"request-key"`, `"request":{"id":7,"kind":"remove","run":0`,
		`"machine":"paused"`, `"removal":{"why":"rm"`, `"taskLog":{"run":1,"stage":"command","from":9`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("wire lacks %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), `"done":null`) || strings.Contains(string(data), "private-owner") {
		t.Errorf("wire exposes an owner or open removal's done: %s", data)
	}
	if strings.Count(string(data), `"done"`) != 1 {
		t.Errorf("open removal sends done: %s", data)
	}
	reply.Task.Removal.Done = &at
	data, err = json.Marshal(reply.Task.Removal)
	if err != nil || !strings.Contains(string(data), `"done":"2026-10-28T12:00:00Z"`) {
		t.Errorf("closed removal = %s, %v", data, err)
	}
}

func TestTaskReplyCheckEveryEnum(t *testing.T) {
	for name, reply := range map[string]hibapi.TaskReply{
		"code":                 {Code: "WHAT"},
		"machine":              {Task: &hibapi.Task{Machine: "sleeping"}},
		"list machine":         {Tasks: []hibapi.Task{{Machine: "sleeping"}}},
		"run phase":            {Task: &hibapi.Task{Runs: []hibapi.TaskRun{{Phase: "sleeping"}}}},
		"run outcome":          {Task: &hibapi.Task{Runs: []hibapi.TaskRun{{Outcome: "maybe"}}}},
		"run stage":            {Task: &hibapi.Task{Runs: []hibapi.TaskRun{{Phase: task.PhaseRunning, Stage: "other"}}}},
		"run step":             {Task: &hibapi.Task{Runs: []hibapi.TaskRun{{Phase: task.PhaseRunning, Step: "other"}}}},
		"request kind":         {Request: &hibapi.TaskRequest{Kind: "wake"}},
		"history request kind": {Task: &hibapi.Task{Requests: []hibapi.TaskRequest{{Kind: "wake"}}}},
		"log stage":            {TaskLog: &hibapi.TaskLog{Run: 1, Stage: "other"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := reply.Check(); err == nil {
				t.Fatal("unknown reply enum passed Check")
			}
		})
	}
	for _, state := range []task.MachineState{task.MachineUnknown, task.MachineRunning, task.MachinePaused, task.MachineGone} {
		if err := (hibapi.TaskReply{Task: &hibapi.Task{Machine: state}}).Check(); err != nil {
			t.Errorf("known machine state %q: %v", state, err)
		}
	}
	for _, kind := range []task.RequestKind{task.RequestCancel, task.RequestRescue, task.RequestRemove} {
		if err := (hibapi.TaskRequest{Kind: kind}).Check(); err != nil {
			t.Errorf("known request kind %q: %v", kind, err)
		}
	}
}

func TestCodes(t *testing.T) {
	if c := hibapi.CodeOf(fmt.Errorf("submit: %w", task.ErrInputFull)); c != hibapi.CodeInputFull || c.Retryable() {
		t.Errorf("input full is %q, retryable %v, want INPUT_FULL and false", c, c.Retryable())
	}
	if c := hibapi.CodeOf(task.ErrQueueFull); c != hibapi.CodeQueueFull || !c.Retryable() {
		t.Errorf("queue full is %q, retryable %v", c, c.Retryable())
	}
	if c := hibapi.CodeOf(task.ErrConflict); c.Retryable() {
		t.Error("a conflict must not be retried")
	}
}
