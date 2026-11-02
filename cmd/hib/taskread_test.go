package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/hibd"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks"
)

func TestTaskCompleteCLIPrinterParity(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return at.Add(time.Hour) }
	h.sudoApp = func(a *hibd.App) { a.Now = c.now }
	st := tasks.Open(statefile.Dir{Path: h.env["HIBD_STATE_DIR"], EUID: os.Geteuid}, c.now)
	defer st.Close()
	rec, err := st.Submit(taskCallContext(t), task.Submission{RequestID: "parity", At: at,
		Spec: task.Spec{Repo: task.Repo{Owner: "acme", Name: "api"}, Command: []string{"echo", "hi"},
			Image: "dev", Size: "small", Timeout: time.Hour, Publish: task.PublishNone},
		Actor: task.Actor{Owner: "martin", Name: "martin", Source: "cli"}}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"task", "ls"}, {"task", "show", string(rec.Run.Task)},
		{"task", "show", "--request", "parity"}} {
		var plain, raw, diagnostics bytes.Buffer
		a := &hibd.App{Stdin: strings.NewReader(""), Stdout: &raw, Stderr: &diagnostics,
			Getenv: func(k string) string { return h.env[k] }, Now: c.now,
			NewEngine: func() (engine.Engine, error) { return h.engine, nil }, ReadRoom: h.readRoom}
		if code := a.Run(taskCallContext(t), append(slices.Clone(args), "-json")); code != 0 {
			t.Fatalf("hibd JSON = exit %d, %q, %q", code, &raw, &diagnostics)
		}
		a.Stdout = &plain
		if code := a.Run(taskCallContext(t), args); code != 0 {
			t.Fatalf("hibd plain = exit %d, %q, %q", code, &plain, &diagnostics)
		}
		if code := c.hibContext(taskCallContext(t), args...); code != 0 ||
			c.out.String() != plain.String() || c.errOut.Len() != 0 {
			t.Fatalf("%v parity = exit %d, hib %q, hibd %q, stderr %q", args, code, c.out, &plain, c.errOut)
		}
		h.mu.Lock()
		gotRaw := h.sudoRuns[len(h.sudoRuns)-1].stdout.String()
		h.mu.Unlock()
		if gotRaw != raw.String() {
			t.Fatalf("parity used different replies: host %q, hibd %q", gotRaw, &raw)
		}
	}
}

func taskReadClient(t *testing.T, h *taskCallHost) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a, out, stderr := taskCallClient(t, h)
	home := a.configFlag
	a.getenv = func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	a.configFlag = filepath.Join(home, ".config", "hibernaut")
	a.now = func() time.Time { return time.Date(2026, 10, 28, 13, 0, 0, 0, time.UTC) }
	a.stdin = strings.NewReader("")
	if err := (&config{Host: testHost, dir: a.configFlag}).save("hib"); err != nil {
		t.Fatal(err)
	}
	return a, out, stderr
}

func TestTaskLSReadsHost(t *testing.T) {
	raw := "{ \"v\": 1, \"tasks\": [" +
		`{"id":"t-bbbbbbbb","repo":"acme/new","created":"2026-10-28T12:00:00Z","runs":[{"n":2,"phase":"running","stage":"command","command":["true"],"submitted":"2026-10-28T12:00:00Z"}]},` +
		`{"id":"t-aaaaaaaa","repo":"acme/old","created":"2026-10-28T11:00:00Z","runs":[{"n":1,"outcome":"succeeded","command":["true"],"submitted":"2026-10-28T11:00:00Z"}]}` +
		"] }\n"
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprint(asJSON), func(t *testing.T) {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: raw}}}
			a, out, stderr := taskReadClient(t, h)
			args := []string{"task", "ls"}
			if asJSON {
				args = append(args, "--json")
			}
			code := a.run(taskCallContext(t), args)
			want := "TASK        RUN  PHASE      STAGE    REPO      AGE\n" +
				"t-bbbbbbbb  2    running    command  acme/new  1h\n" +
				"t-aaaaaaaa  1    succeeded  -        acme/old  2h\n"
			if asJSON {
				want = raw
			}
			if code != 0 || out.String() != want || stderr.Len() != 0 {
				t.Fatalf("ls = exit %d, stdout %q, stderr %q; want %q", code, out, stderr, want)
			}
			if len(h.calls) != 1 || !slices.Equal(h.calls[0], []string{"sudo", "-n", "hibd", "task", "ls", "-json"}) {
				t.Fatalf("ls sent %q, want one JSON read", h.calls)
			}
		})
	}
}

func TestTaskReadPrinterParity(t *testing.T) {
	record := hibapi.Task{ID: "t-aaaaaaaa", Repo: "acme/api",
		Created: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		Runs:    []hibapi.TaskRun{{N: 1, Phase: task.PhaseQueued, Command: []string{"echo", "hi"}}}}
	for _, args := range [][]string{{"task", "ls"}, {"task", "show", string(record.ID)},
		{"task", "show", "--request", "submission-1"}} {
		reply := hibapi.Response{V: 1, TaskReply: hibapi.TaskReply{Task: &record}}
		if args[1] == "ls" {
			reply.Task, reply.Tasks = nil, []hibapi.Task{record}
		}
		data, err := json.Marshal(reply)
		if err != nil {
			t.Fatal(err)
		}
		h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: string(data) + "\n"}}}
		a, out, stderr := taskReadClient(t, h)
		var want bytes.Buffer
		if args[1] == "ls" {
			err = hibapi.PrintTaskList(&want, reply.Tasks, a.now())
		} else {
			err = hibapi.PrintTask(&want, record)
		}
		if err != nil {
			t.Fatal(err)
		}
		if code := a.run(taskCallContext(t), args); code != 0 || out.String() != want.String() || stderr.Len() != 0 {
			t.Fatalf("%v differs from shared printer: exit %d, stdout %q, stderr %q", args, code, out, stderr)
		}
		wantArgs := append([]string{"sudo", "-n", "hibd"}, args...)
		for i, arg := range wantArgs {
			if arg == "--request" {
				wantArgs[i] = "-request"
			}
		}
		wantArgs = append(wantArgs, "-json")
		if len(h.calls) != 1 || !slices.Equal(h.calls[0], wantArgs) {
			t.Fatalf("show sent %q, want %q", h.calls, wantArgs)
		}
		out.Reset()
		if code := a.run(taskCallContext(t), append(args, "--json")); code != 0 || out.String() != string(data)+"\n" {
			t.Fatalf("JSON show = exit %d, stdout %q, want unchanged reply", code, out)
		}
	}
}

func TestTaskRequestsRecordAndReplay(t *testing.T) {
	for _, kind := range []string{"cancel", "rescue", "rm"} {
		t.Run(kind, func(t *testing.T) {
			wireKind := kind
			if kind == "rm" {
				wireKind = "remove"
			}
			raw := fmt.Sprintf(`{"v":1,"requestId":"request-1","request":{"id":7,"kind":%q,"run":2,"at":"2026-10-28T12:00:00Z","actor":{"name":"martin","source":"cli"}}}`+"\n", wireKind)
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 255}, {text: raw}}}
			a, out, stderr := taskReadClient(t, h)
			args := []string{"task", kind, "t-aaaaaaaa", "--request-id", "request-1"}
			if kind == "rm" {
				args = append(args, "--yes")
			}
			want := fmt.Sprintf("recorded %s for t-aaaaaaaa run 2\nrequest request-1\n", wireKind)
			for i := 0; i < 2; i++ {
				out.Reset()
				if code := a.run(taskCallContext(t), args); code != 0 || out.String() != want || stderr.Len() != 0 {
					t.Fatalf("record %d = exit %d, stdout %q, stderr %q; want %q", i, code, out, stderr, want)
				}
			}
			wantCall := []string{"sudo", "-n", "hibd", "task", kind, "t-aaaaaaaa", "-request-id", "request-1", "-json"}
			if len(h.calls) != 3 {
				t.Fatalf("lost reply and repeat sent %d calls, want 3", len(h.calls))
			}
			for _, call := range h.calls {
				if !slices.Equal(call, wantCall) {
					t.Fatalf("replay changed identity: %q", h.calls)
				}
			}
			out.Reset()
			if code := a.run(taskCallContext(t), append(args, "--json")); code != 0 || out.String() != raw {
				t.Fatalf("JSON request = exit %d, stdout %q, want unchanged reply", code, out)
			}
		})
	}
}

func TestTaskRequestFailures(t *testing.T) {
	for _, kind := range []string{"cancel", "rescue", "rm"} {
		for _, code := range []hibapi.ErrorCode{hibapi.CodeNotLive, hibapi.CodeRunLive, hibapi.CodeNoTask, ""} {
			t.Run(kind+"/"+string(code), func(t *testing.T) {
				reply := taskHostReply{code: 1, text: fmt.Sprintf(`{"v":1,"error":"hibd refuses this request","errorCode":%q}`, code)}
				if code == "" {
					reply = taskHostReply{code: 255}
				}
				h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{reply}}
				a, out, stderr := taskReadClient(t, h)
				args := []string{"task", kind, "t-aaaaaaaa", "--request-id", "used-1"}
				if kind == "rm" {
					args = append(args, "--yes")
				}
				exit := a.run(taskCallContext(t), args)
				hint := strings.Contains(stderr.String(), "hib task show --request 'used-1'")
				wantHint := code == hibapi.CodeNoTask || code == ""
				if exit != 1 || out.Len() != 0 || hint != wantHint ||
					code != "" && !strings.Contains(stderr.String(), "hibd refuses this request") {
					t.Fatalf("failure = exit %d, stdout %q, stderr %q; want hint %v", exit, out, stderr, wantHint)
				}
				wantCalls := 1
				if code == "" {
					wantCalls = 2
				}
				if len(h.calls) != wantCalls {
					t.Fatalf("failure sent %d calls, want %d", len(h.calls), wantCalls)
				}
			})
		}
	}
}

type taskConfirmInput struct {
	*strings.Reader
	before func()
}

func (in taskConfirmInput) Read(p []byte) (int, error) {
	in.before()
	return in.Reader.Read(p)
}

func TestTaskRMConfirmsBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name, answer  string
		terminal, yes bool
		code, sends   int
	}{
		{"no", "n\n", true, false, 1, 0},
		{"yes", "yes\n", true, false, 0, 1},
		{"no terminal", "", false, false, 2, 0},
		{"yes flag", "", false, true, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
				{text: `{"v":1,"requestId":"remove-1","request":{"id":1,"kind":"remove","run":0}}`}}}
			a, out, stderr := taskReadClient(t, h)
			a.interactive = tc.terminal
			a.stdin = taskConfirmInput{Reader: strings.NewReader(tc.answer), before: func() {
				if len(h.calls) != 0 || h.versions != 0 {
					t.Errorf("rm contacted the host before the answer: %q", h.calls)
				}
			}}
			args := []string{"task", "rm", "t-aaaaaaaa", "--request-id", "remove-1"}
			if tc.yes {
				args = append(args, "--yes")
			}
			code := a.run(taskCallContext(t), args)
			if code != tc.code || len(h.calls) != tc.sends {
				t.Fatalf("rm = exit %d, %d sends, stdout %q, stderr %q; want exit %d, %d sends",
					code, len(h.calls), out, stderr, tc.code, tc.sends)
			}
			if tc.sends == 0 && out.Len() != 0 {
				t.Fatalf("declined rm printed success: %q", out)
			}
			if tc.terminal && !tc.yes && !strings.Contains(stderr.String(), "[y/N]") {
				t.Fatalf("no confirmation question: %q", stderr)
			}
			if !tc.terminal && !tc.yes && !strings.Contains(stderr.String(), "pass --yes") {
				t.Fatalf("nonterminal rm lacks guidance: %q", stderr)
			}
			if tc.yes && strings.Contains(stderr.String(), "[y/N]") {
				t.Fatalf("--yes asked anyway: %q", stderr)
			}
		})
	}
}

func TestTaskReadBadArgumentsStayLocal(t *testing.T) {
	for _, args := range [][]string{{"task", "show"}, {"task", "show", "bad"},
		{"task", "show", "t-aaaaaaaa", "--request", "id"}, {"task", "cancel", "bad"},
		{"task", "rescue", "bad"}, {"task", "rm", "bad", "--yes"}} {
		h := &taskCallHost{features: `["tasks"]`}
		a, _, stderr := taskReadClient(t, h)
		if code := a.run(taskCallContext(t), args); code != 2 || stderr.Len() == 0 || h.versions != 0 {
			t.Fatalf("bad %v = exit %d, stderr %q, %d contacts", args, code, stderr, h.versions)
		}
	}

}

func TestTaskJSONRefusalIsUnchanged(t *testing.T) {
	raw := "{ \"v\":1, \"error\":\"hibd refuses\", \"errorCode\":\"NO_TASK\" }\n"
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 1, text: raw}}}
	a, out, stderr := taskReadClient(t, h)
	args := []string{"task", "cancel", "t-aaaaaaaa", "--request-id", "used-1", "--json"}
	if code := a.run(taskCallContext(t), args); code != 1 || out.String() != raw ||
		!strings.Contains(stderr.String(), "hib task show --request 'used-1'") {
		t.Fatalf("JSON refusal = exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

func TestTaskGeneratedRequestHintUsesSentID(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 255}}}
	a, _, stderr := taskReadClient(t, h)
	if code := a.run(taskCallContext(t), []string{"task", "cancel", "t-aaaaaaaa"}); code != 1 || len(h.calls) != 2 {
		t.Fatalf("lost reply = exit %d, calls %q, stderr %q", code, h.calls, stderr)
	}
	i := slices.Index(h.calls[0], "-request-id")
	if i < 0 || i+1 >= len(h.calls[0]) {
		t.Fatalf("no generated ID sent: %q", h.calls)
	}
	id := h.calls[0][i+1]
	if id == "" || !slices.Equal(h.calls[0], h.calls[1]) ||
		!strings.Contains(stderr.String(), "hib task show --request '"+id+"'") {
		t.Fatalf("hint did not identify the replayed request: %q, %q", h.calls, stderr)
	}
}
