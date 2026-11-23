package hibapi

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/clipperhouse/displaywidth"

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
  finish: commit and push
  stop: kill the live stage, prove it gone
  collect: copy last logs and the publication
  publish: find or open the draft PR (skipped: publish none)
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

func TestPrintTaskPublications(t *testing.T) {
	for _, tc := range []struct {
		name string
		pub  TaskPublication
		want string
	}{
		{"no changes", TaskPublication{Author: "martin"},
			"publication no changes\nauthor martin\n"},
		{"not pushed", TaskPublication{Changes: true, Commit: "abc123"},
			"commit abc123\npushed false\n"},
		{"no PR", TaskPublication{Changes: true, Commit: "abc123", Pushed: true, Branch: "hib/t-aaaaaaaa"},
			"commit abc123\npushed true\nbranch hib/t-aaaaaaaa\n"},
		{"draft PR", TaskPublication{Changes: true, Commit: "abc123", Pushed: true, Branch: "hib/t-aaaaaaaa",
			PR: "https://github.com/acme/api/pull/42", Author: "martin"},
			"commit abc123\npushed true\nbranch hib/t-aaaaaaaa\npr https://github.com/acme/api/pull/42 (draft)\nauthor martin\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := Task{ID: "t-aaaaaaaa", Repo: "acme/api", Runs: []TaskRun{
				{N: 1, Step: task.StepKeep, Publish: task.PublishDraftPR, Publication: &tc.pub},
				{N: 2, Step: task.StepKeep, Publish: task.PublishNone},
			}}
			var out bytes.Buffer
			if err := PrintTask(&out, record); err != nil {
				t.Fatal(err)
			}
			if want := "held -\n" + tc.want + "step keep\n"; !strings.Contains(out.String(), want) {
				t.Fatalf("show publication lacks %q:\n%s", want, &out)
			}
			if strings.Count(out.String(), tc.want) != 1 {
				t.Fatalf("show repeated a publication on another run:\n%s", &out)
			}
		})
	}
}

func TestPrintTaskPublishSteps(t *testing.T) {
	for _, mode := range []task.Publish{"", task.PublishNone, task.PublishDraftPR} {
		t.Run(string(mode), func(t *testing.T) {
			record := Task{ID: "t-aaaaaaaa", Repo: "acme/api",
				Runs: []TaskRun{{N: 1, Publish: mode}}}
			var out bytes.Buffer
			if err := PrintTask(&out, record); err != nil {
				t.Fatal(err)
			}
			want := "  publish: find or open the draft PR"
			if mode != task.PublishDraftPR {
				want += " (skipped: publish none)"
			}
			if !strings.Contains(out.String(), want+"\n") {
				t.Fatalf("show publish %q lacks %q:\n%s", mode, want, &out)
			}
		})
	}
}

func TestPrintTaskListFits80Columns(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	held := "paused without the freeze: hib task rescue t-aaaaaaaa or hib task rm t-aaaaaaaa"
	reasons := []string{held, "stop unproven: the stage may still run when the machine wakes",
		"hib task cancel by " + strings.Repeat("x", 200) + "\x1b"}
	repo := strings.Repeat("o", 39) + "/" + strings.Repeat("r", 100)
	var records []Task
	for i, reason := range reasons {
		run := TaskRun{N: 999, Phase: task.PhasePaused, Stage: task.StageCommand, Detail: reason}
		if i == 0 {
			run.Held = reason
		}
		records = append(records, Task{ID: task.ID("t-aaaaaaa" + string(rune('a'+i))), Repo: repo, Created: at, Runs: []TaskRun{run}},
			Task{ID: "t-zzzzzzzz", Created: at, Runs: []TaskRun{{N: 20, Outcome: task.Succeeded}}})
	}
	records = append(records, Task{ID: "t-xxxxxxxx", Repo: repo, Created: at,
		Runs: []TaskRun{{N: 999, Outcome: task.NoChanges, Stage: task.StageCommand}}})
	var out bytes.Buffer
	if err := PrintTaskList(&out, records, at.Add(8760*time.Hour+59*time.Minute+59*time.Second)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	phaseColumn := strings.Index(lines[0], "PHASE")
	var columns []int
	start := 0
	for _, name := range strings.Fields(lines[0]) {
		start += strings.Index(lines[0][start:], name)
		columns = append(columns, start)
		start += len(name)
	}
	rows := 0
	for i, line := range lines {
		if n := displaywidth.String(line); n > 80 {
			t.Errorf("list line %d has %d columns: %q", i, n, line)
		}
		if strings.ContainsRune(line, '\x1b') {
			t.Errorf("raw terminal control on line %d: %q", i, line)
		}
		if i == 0 {
			continue
		}
		if strings.HasPrefix(line, "  ") {
			continue
		}
		phase := "paused"
		if rows%2 == 1 {
			phase = "succeeded"
		}
		if rows == 6 {
			phase = "no_changes"
		}
		if strings.Index(line, phase) != phaseColumn || len(strings.Fields(line)) != 6 {
			t.Errorf("list row %d is not aligned or holds its reason: %q", rows, line)
		}
		start = 0
		for column, value := range strings.Fields(line) {
			start += strings.Index(line[start:], value)
			if column < len(columns) && start != columns[column] {
				t.Errorf("list row %d column %d starts at %d, want %d", rows, column, start, columns[column])
			}
			start += len(value)
		}
		rows++
	}
	if rows != 7 || !strings.Contains(out.String(), "\n  paused without the freeze:") {
		t.Errorf("list omitted rows or separate reasons:\n%s", &out)
	}
	for _, command := range []string{"hib task rescue t-aaaaaaaa", "hib task rm t-aaaaaaaa"} {
		if !strings.Contains(out.String(), command) {
			t.Errorf("list split the pasteable command %q:\n%s", command, &out)
		}
	}
	out.Reset()
	if err := PrintTask(&out, records[4]); err != nil || !strings.Contains(out.String(), strings.Repeat("x", 200)+`\x1b`) {
		t.Fatalf("show cut the full reason: %v, %q", err, &out)
	}
	if !strings.Contains(out.String(), "repo "+repo+"\n") {
		t.Fatalf("show cut the full repo: %q", &out)
	}
}

func TestPrintTaskListRepoBudgetAdapts(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		n   int
		age time.Duration
	}{
		{1, time.Hour},
		{999, 8760*time.Hour + 59*time.Minute + 59*time.Second},
		{999999, 100 * 365 * 24 * time.Hour},
	} {
		record := Task{ID: "t-aaaaaaaa", Repo: strings.Repeat("o", 39) + "/" + strings.Repeat("r", 100),
			Created: at, Runs: []TaskRun{{N: tc.n, Outcome: task.NoChanges, Stage: task.StageCommand}}}
		var out bytes.Buffer
		if err := PrintTaskList(&out, []Task{record}, at.Add(tc.age)); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(out.String(), "\n")
		row := lines[1]
		if width := max(displaywidth.String(lines[0]), displaywidth.String(row)); width != 80 || len(strings.Fields(row)) != 6 ||
			!strings.HasSuffix(strings.Fields(row)[4], "...") {
			t.Errorf("run %d age %v has repo budget giving %d columns: %q; want 80 and six fields", tc.n, tc.age, width, row)
		}
	}
}

func TestPrintTaskListCutsBetweenEscapes(t *testing.T) {
	reason := strings.Repeat("x", 73) + "\x1b" + strings.Repeat("y", 20)
	record := Task{ID: "t-aaaaaaaa", Runs: []TaskRun{{N: 1, Phase: task.PhaseStopping, Detail: reason}}}
	var out bytes.Buffer
	if err := PrintTaskList(&out, []Task{record}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n  "+strings.Repeat("x", 73)+"...\n") {
		t.Fatalf("reason cut inside a terminal escape: %q", &out)
	}
}

func TestPrintTaskListWideReason(t *testing.T) {
	record := Task{ID: "t-aaaaaaaa", Runs: []TaskRun{{N: 1, Phase: task.PhaseStopping,
		Detail: strings.Repeat("界", 40)}}}
	var out bytes.Buffer
	if err := PrintTaskList(&out, []Task{record}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n  "+strings.Repeat("界", 37)+"...\n") {
		t.Fatalf("reason exceeds 80 terminal columns with wide characters: %q", &out)
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
		{"commit", func(r *Task) { r.Runs[0].Publication = &TaskPublication{Changes: true, Commit: unsafe} }},
		{"branch", func(r *Task) { r.Runs[0].Publication = &TaskPublication{Changes: true, Pushed: true, Branch: unsafe} }},
		{"PR", func(r *Task) { r.Runs[0].Publication = &TaskPublication{Changes: true, PR: unsafe} }},
		{"author", func(r *Task) { r.Runs[0].Publication = &TaskPublication{Author: unsafe} }},
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
