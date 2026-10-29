package hibapi_test

import (
	"encoding/json"
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

func TestCodes(t *testing.T) {
	if c := hibapi.CodeOf(task.ErrQueueFull); c != hibapi.CodeQueueFull || !c.Retryable() {
		t.Errorf("queue full is %q, retryable %v", c, c.Retryable())
	}
	if c := hibapi.CodeOf(task.ErrConflict); c.Retryable() {
		t.Error("a conflict must not be retried")
	}
}
