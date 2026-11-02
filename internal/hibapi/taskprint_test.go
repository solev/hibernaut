package hibapi

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

func TestPrintTaskGolden(t *testing.T) {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	done := at.Add(time.Hour)
	record := Task{ID: "t-aaaaaaaa", Repo: "acme/api", Machine: task.MachineRunning,
		Runs: []TaskRun{{N: 2, Outcome: task.Succeeded, Step: task.StepKeep,
			Command: []string{"true"}, Recipe: "check", Submitted: at}},
		Requests: []TaskRequest{{ID: 7, Kind: task.RequestRemove, Run: 0, At: at,
			Actor: TaskActor{Name: "martin", Source: "cli"}}},
		Removal: &TaskRemoval{Why: "rm", At: at, Done: &done}}
	var out bytes.Buffer
	if err := PrintTask(&out, record); err != nil {
		t.Fatal(err)
	}
	want := `task t-aaaaaaaa
repo acme/api
machine running
run 2 succeeded -
command "true"
recipe check
submitted 2026-10-28T12:00:00Z
deadline -
detail -
held -
step keep
  machine: wait for room, then create or resume
  upload: upload inputs and runner, check hashes
  setup: clone and branch (skipped: follow-up or no repo)
  command: run the command
  finish: commit, push, draft PR
  stop: kill the live stage, prove it gone
  collect: copy last logs and the publication
  keep: pause and keep, or remove on rm
request 7 remove run 0 by martin via cli at 2026-10-28T12:00:00Z
removal rm at 2026-10-28T12:00:00Z done at 2026-10-28T13:00:00Z
`
	if out.String() != want {
		t.Fatalf("full show differs\nwant:\n%s\ngot:\n%s", want, &out)
	}
	run := task.Run{ID: task.RunID{Task: record.ID, N: 2}, Spec: task.Spec{Recipe: "check"}}
	if got := RunOf(run, "").Recipe; got != "check" {
		t.Fatalf("wire recipe = %q, want check", got)
	}
}

func TestPrintTaskEscapesFields(t *testing.T) {
	unsafe := "guest\x1b]0;title\x07\n\t\r\u202e\xff"
	for _, tc := range []struct {
		name string
		set  func(*Task)
	}{
		{"id", func(r *Task) { r.ID = task.ID(unsafe) }},
		{"repo", func(r *Task) { r.Repo = unsafe }},
		{"machine", func(r *Task) { r.Machine = task.MachineState(unsafe) }},
		{"phase", func(r *Task) { r.Runs[0].Phase = task.Phase(unsafe) }},
		{"outcome", func(r *Task) { r.Runs[0].Outcome = task.Outcome(unsafe) }},
		{"stage", func(r *Task) { r.Runs[0].Stage = task.Stage(unsafe) }},
		{"step", func(r *Task) { r.Runs[0].Step = task.Step(unsafe) }},
		{"held", func(r *Task) { r.Runs[0].Held = unsafe }},
		{"detail", func(r *Task) { r.Runs[0].Detail = unsafe }},
		{"command", func(r *Task) { r.Runs[0].Command = []string{unsafe} }},
		{"recipe", func(r *Task) { r.Runs[0].Recipe = unsafe }},
		{"actor", func(r *Task) { r.Requests[0].Actor.Name = unsafe }},
		{"source", func(r *Task) { r.Requests[0].Actor.Source = unsafe }},
		{"kind", func(r *Task) { r.Requests[0].Kind = task.RequestKind(unsafe) }},
		{"removal", func(r *Task) { r.Removal.Why = unsafe }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := Task{ID: "t-aaaaaaaa", Runs: []TaskRun{{N: 1, Phase: task.PhaseQueued}},
				Requests: []TaskRequest{{Kind: task.RequestCancel}}, Removal: &TaskRemoval{Why: "rm"}}
			tc.set(&record)
			var out bytes.Buffer
			if err := PrintTask(&out, record); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if !strings.Contains(text, `\x1b]0;title`) || strings.ContainsAny(text, "\x1b\x07\t\r\u202e") ||
				!strings.Contains(text, `\xff`) || strings.Contains(text, "title\x07\n") {
				t.Fatalf("%s was not escaped: %q", tc.name, text)
			}
			out.Reset()
			if err := PrintTaskList(&out, []Task{record}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if strings.ContainsAny(out.String(), "\x1b\x07\t\r\u202e") {
				t.Fatalf("list %s was not escaped: %q", tc.name, &out)
			}
		})
	}
	var out bytes.Buffer
	if err := PrintTaskRequest(&out, task.ID(unsafe), unsafe,
		TaskRequest{Kind: task.RequestKind(unsafe)}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07\t\r\u202e") || !strings.Contains(out.String(), `\xff`) {
		t.Fatalf("request was not escaped: %q", &out)
	}
}

type taskPrintError struct{}

func (taskPrintError) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTaskPrintersReturnWriteErrors(t *testing.T) {
	for _, err := range []error{PrintTask(taskPrintError{}, Task{}),
		PrintTaskList(taskPrintError{}, nil, time.Now()),
		PrintTaskRequest(taskPrintError{}, "", "", TaskRequest{})} {
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("printer error = %v, want closed pipe", err)
		}
	}
}
