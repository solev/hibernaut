package hibd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

// hib pause and hib rm ask the task rules before they touch a machine with the task label, and
// refuse when tasks.db can't say. A person's machine never asks, as TestMachineCommands shows.
func TestPauseAndRmAskTheTaskRules(t *testing.T) {
	e := newEngine(newKey(t))
	a, _, errOut := newApp(t, e)
	const tid = "t-abcdefgh"
	e.Add(engine.Machine{ID: "sbx-task", State: engine.Running, StartedAt: time.Now(),
		EndAt: time.Now().Add(time.Hour), MemoryMB: 2048,
		Metadata: map[string]string{"hib.name": tid, task.Label: tid}})
	for _, c := range []struct{ cmd, op string }{{"pause", "pause"}, {"rm", "remove"}} {
		errOut.Reset()
		code := a.Run(t.Context(), []string{c.cmd, tid})
		if want := "refused to " + c.op + " task machine " + tid; code != 1 ||
			!strings.Contains(errOut.String(), want) {
			t.Errorf("hibd %s %s = exit %d, stderr %q; want 1 and %q", c.cmd, tid, code, errOut, want)
		}
	}
	if m, err := e.Get(t.Context(), "sbx-task"); err != nil || m.State != engine.Running {
		t.Errorf("task machine after pause and rm = %+v, %v; want it running", m, err)
	}
}

// TestTaskCommandsDispatchAndUsage checks the task and recipe table is the one list: every row
// dispatches to its own handler, appears in its group's usage and is named in hibd's usage.
func TestTaskCommandsDispatchAndUsage(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	seen := map[string]bool{}
	for _, c := range taskCommands {
		if seen[c.path] {
			t.Errorf("%q is in taskCommands twice", c.path)
		}
		seen[c.path] = true
		group, leaf, _ := strings.Cut(c.path, " ")
		if !strings.Contains(taskUsage(group), "  "+strings.TrimSpace(leaf+" "+c.args)+"\n") {
			t.Errorf("hibd %s usage lacks %q", group, leaf)
		}
		if !strings.Contains(usage, leaf) {
			t.Errorf("hibd usage doesn't name %q", c.path)
		}
		out.Reset()
		errOut.Reset()
		args := append(strings.Fields(c.path), "-json")
		if strings.HasPrefix(c.args, "TASK") || strings.HasPrefix(c.args, "NAME") {
			args = append(args, "t-aaaaaaaa")
		}
		code, r, err := runJSON(t, a, out, args...)
		if check := r.TaskReply.Check(); check != nil {
			t.Errorf("hibd %v reply: %v", args, check)
		}
		switch c.path {
		case "task ls":
			if code != 0 || err != nil || len(r.Tasks) != 0 {
				t.Errorf("hibd %v = exit %d, %+v, %v; want no tasks", args, code, r, err)
			}
		case "task show", "task logs", "task cancel", "task rescue", "task rm":
			if code != 1 || err == nil || r.Code != hibapi.CodeNoTask {
				t.Errorf("hibd %v = exit %d, code %s, %v; want NO_TASK", args, code, r.Code, err)
			}
		case "task start", "task run":
			if code != 1 || err == nil || r.Code != hibapi.CodeBadRequest {
				t.Errorf("hibd %v = exit %d, code %s, %v; want BAD_REQUEST", args, code, r.Code, err)
			}
		default:
			if code != 1 || err == nil {
				t.Errorf("hibd %v = exit %d, %+v, %v; want a JSON failure", args, code, r, err)
			}
		}
	}
	for _, args := range [][]string{{"task"}, {"task", "nope"}, {"recipe"}} {
		errOut.Reset()
		if code := a.Run(t.Context(), args); code != 2 || !strings.Contains(errOut.String(), "commands:") {
			t.Errorf("hibd %v = exit %d, stderr %q; want 2 and the group's usage", args, code, errOut)
		}
	}
}

func TestSubmissionOf(t *testing.T) {
	at := time.Date(2025, 10, 28, 9, 0, 0, 0, time.UTC)
	actor := task.Actor{Owner: "martin", Name: "martin", Source: "cli"}
	ok := hibapi.TaskStart{RequestID: "r1", Repo: "solev/hibernaut", Command: []string{"codex"},
		Prompt: &hibapi.TaskFile{Size: 12}, Files: []hibapi.TaskFile{{Name: "notes.md", Size: 3}}}
	sub, err := submissionOf(ok, actor, at)
	if err != nil {
		t.Fatal(err)
	}
	want := task.Spec{Repo: task.Repo{Owner: "solev", Name: "hibernaut"}, Command: []string{"codex"},
		Image: "dev", Size: "medium", Timeout: task.DefaultTimeout, Publish: task.PublishDraftPR,
		Inputs: []task.File{{Name: task.PromptName, Size: 12}, {Name: "notes.md", Size: 3}}}
	if sub.Spec.Repo != want.Repo || sub.Spec.Image != want.Image || sub.Spec.Size != want.Size ||
		sub.Spec.Timeout != want.Timeout || sub.Spec.Publish != want.Publish ||
		len(sub.Spec.Inputs) != 2 || sub.Spec.Inputs[0] != want.Inputs[0] || sub.Spec.Inputs[1] != want.Inputs[1] {
		t.Errorf("spec = %+v, want %+v", sub.Spec, want)
	}
	noRepo := ok
	noRepo.Repo = ""
	if sub, err := submissionOf(noRepo, actor, at); err != nil || sub.Spec.Publish != task.PublishNone {
		t.Errorf("no repo = %+v, %v; want publish none", sub.Spec, err)
	}
	for name, edit := range map[string]func(*hibapi.TaskStart){
		"no request ID":   func(r *hibapi.TaskStart) { r.RequestID = "" },
		"no command":      func(r *hibapi.TaskStart) { r.Command = nil },
		"bad repo":        func(r *hibapi.TaskStart) { r.Repo = "hibernaut" },
		"bad size":        func(r *hibapi.TaskStart) { r.Size = "huge" },
		"bad publish":     func(r *hibapi.TaskStart) { r.Publish = "pr" },
		"pr without repo": func(r *hibapi.TaskStart) { r.Repo, r.Publish = "", task.PublishDraftPR },
		"bad task":        func(r *hibapi.TaskStart) { r.Task = "app" },
		"hidden file":     func(r *hibapi.TaskStart) { r.Files = []hibapi.TaskFile{{Name: ".env"}} },
		"nested file":     func(r *hibapi.TaskStart) { r.Files = []hibapi.TaskFile{{Name: "a/b"}} },
		"prompt's name":   func(r *hibapi.TaskStart) { r.Files = []hibapi.TaskFile{{Name: task.PromptName}} },
		"too big": func(r *hibapi.TaskStart) {
			r.Files = []hibapi.TaskFile{{Name: "big", Size: task.MaxInputBytes}}
		},
	} {
		req := ok
		req.Files = append([]hibapi.TaskFile(nil), ok.Files...)
		edit(&req)
		if _, err := submissionOf(req, actor, at); !errors.Is(err, errBadTaskRequest) {
			t.Errorf("%s: err = %v, want a bad request", name, err)
		}
	}
}
