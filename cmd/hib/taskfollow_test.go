package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

func followClient(t *testing.T, h *taskCallHost) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a, out, stderr := taskCallClient(t, h)
	root := a.configFlag
	dir := filepath.Join(root, "hibernaut")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	a.getenv = func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return root
		}
		return ""
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"host":"`+testHost+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return a, out, stderr
}

func followLogReply(t *testing.T, n int, from int64, stage task.Stage, data []byte, done bool) taskHostReply {
	t.Helper()
	r := hibapi.Response{V: 1, TaskReply: hibapi.TaskReply{TaskLog: &hibapi.TaskLog{
		Run: n, From: from, Next: from + int64(len(data)), Stage: stage, Data: data, Done: done}}}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return taskHostReply{text: string(raw) + "\n"}
}

func followWaitReply(t *testing.T, outcome task.Outcome, held string) taskHostReply {
	t.Helper()
	run := hibapi.TaskRun{N: 1, Outcome: outcome, Held: held, Command: []string{"true"}}
	if outcome == "" {
		run.Phase = task.PhasePaused
	}
	raw, err := json.Marshal(hibapi.Response{V: 1, TaskReply: hibapi.TaskReply{
		Task: &hibapi.Task{ID: "t-abcdefgh", Runs: []hibapi.TaskRun{run}}}})
	if err != nil {
		t.Fatal(err)
	}
	return taskHostReply{text: string(raw) + "\n"}
}

func followCall(t *testing.T, call []string, path string, run int, from int64, hold bool) {
	t.Helper()
	want := append([]string{"sudo", "-n", "hibd", "task", path, "t-abcdefgh"}, "-run", strconv.Itoa(run))
	if path == "logs" {
		want = append(want, "-from", strconv.FormatInt(from, 10))
	}
	if hold {
		want = append(want, "-hold", "60s")
	}
	want = append(want, "-json")
	if !slices.Equal(call, want) {
		t.Fatalf("host call = %q; want %q", call, want)
	}
}

func TestTaskLogsAllCopiedBytes(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`}
	var want []byte
	var offsets []int64
	for i, stage := range task.Stages() {
		data := bytes.Repeat([]byte{byte('a' + i)}, (1<<20)+17+i)
		for len(data) > 0 {
			n := min(len(data), 1<<20)
			offsets = append(offsets, int64(len(want)))
			h.replies = append(h.replies, followLogReply(t, 2, int64(len(want)), stage, data[:n], false))
			want = append(want, data[:n]...)
			data = data[n:]
		}
	}
	h.replies[len(h.replies)-1] = followLogReply(t, 2, offsets[len(offsets)-1],
		task.StageFinish, want[offsets[len(offsets)-1]:], true)
	a, out, stderr := followClient(t, h)
	code := a.run(taskCallContext(t), []string{"task", "logs", "t-abcdefgh", "--run", "2"})
	if code != 0 || !bytes.Equal(out.Bytes(), want) || stderr.Len() != 0 {
		t.Fatalf("F1 logs = exit %d, %d bytes, stderr %q; want %d exact bytes", code, out.Len(), stderr, len(want))
	}
	if len(h.calls) != len(offsets) {
		t.Fatalf("F1 calls = %d; want %d stage-local ranges", len(h.calls), len(offsets))
	}
	for i, call := range h.calls {
		followCall(t, call, "logs", 2, offsets[i], false)
	}
}

func TestTaskLogsFollowPinsRun(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		followLogReply(t, 1, 0, "", []byte{}, false),
		followLogReply(t, 1, 0, task.StageSetup, []byte("setup\n"), false),
		followLogReply(t, 1, 6, task.StageCommand, []byte("command\n"), false),
		followLogReply(t, 1, 14, task.StageFinish, []byte("finish\n"), true),
	}}
	a, out, stderr := followClient(t, h)
	a.ssh = func(ctx context.Context, args []string, in io.Reader, stdout, stderr io.Writer) (int, error) {
		if len(h.calls) == 2 && out.String() != "setup\n" ||
			len(h.calls) == 3 && out.String() != "setup\ncommand\n" {
			t.Fatalf("F2 next hold started before printing available output: %q", out)
		}
		if len(h.calls) >= 2 && !strings.Contains(strings.Join(args, " "), "'-run' '1'") {
			h.replies[len(h.calls)] = followLogReply(t, 2, 6, task.StageCommand, []byte("run two\n"), true)
		}
		return h.ssh(ctx, args, in, stdout, stderr)
	}
	code := a.run(taskCallContext(t), []string{"task", "logs", "t-abcdefgh", "-f"})
	if code != 0 || out.String() != "setup\ncommand\nfinish\n" || stderr.Len() != 0 || len(h.calls) != 4 {
		t.Fatalf("F2 follow = exit %d, stdout %q, stderr %q, %d calls", code, out, stderr, len(h.calls))
	}
	for i, from := range []int64{0, 0, 6, 14} {
		run := 1
		if i == 0 {
			run = 0
		}
		followCall(t, h.calls[i], "logs", run, from, true)
	}
	plain := &taskCallHost{features: `["tasks"]`, replies: h.replies[1:]}
	b, copied, _ := followClient(t, plain)
	if code := b.run(taskCallContext(t), []string{"task", "logs", "t-abcdefgh"}); code != 0 ||
		!bytes.Equal(copied.Bytes(), out.Bytes()) {
		t.Fatalf("F2 plain and follow differ, exit %d, plain %q, follow %q", code, copied, out)
	}
}

func TestTaskWaitOutcomes(t *testing.T) {
	for _, outcome := range task.Outcomes() {
		for _, asJSON := range []bool{false, true} {
			t.Run(string(outcome)+"/"+strconv.FormatBool(asJSON), func(t *testing.T) {
				h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{followWaitReply(t, outcome, "")}}
				h.replies[0].text = " \t" + h.replies[0].text + "\n"
				a, out, stderr := followClient(t, h)
				args := []string{"task", "wait", "t-abcdefgh", "--run", "1"}
				want := string(outcome) + "\n"
				if asJSON {
					args = append(args, "--json")
					want = h.replies[0].text
				}
				code := a.run(taskCallContext(t), args)
				exit := 1
				if outcome == task.Succeeded || outcome == task.NoChanges {
					exit = 0
				}
				if code != exit || out.String() != want || stderr.Len() != 0 || len(h.calls) != 1 {
					t.Fatalf("F4 %s = exit %d, stdout %q, stderr %q; want %d, %q", outcome, code, out, stderr, exit, want)
				}
				followCall(t, h.calls[0], "wait", 1, 0, true)
			})
		}
	}
}

func TestTaskFollowRetriesAcknowledgedCursor(t *testing.T) {
	lost := taskHostReply{code: -1, err: errors.New("connection reset"), text: `{"v":1,"taskLog":`}
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		followLogReply(t, 1, 0, task.StageCommand, []byte("one"), false), lost, lost,
		followLogReply(t, 1, 3, task.StageCommand, []byte("two"), false), lost, lost,
		followLogReply(t, 1, 6, task.StageCommand, []byte("three"), true),
	}}
	a, out, stderr := followClient(t, h)
	code := a.run(taskCallContext(t), []string{"task", "logs", "t-abcdefgh", "-f"})
	if code != 0 || out.String() != "onetwothree" || strings.Count(stderr.String(), "reconnecting") != 1 {
		t.Fatalf("F6 follow = exit %d, stdout %q, stderr %q; want exact bytes and one retry notice", code, out, stderr)
	}
	if len(h.calls) != 7 {
		t.Fatalf("F6 calls = %d; want 7 including taskCall's immediate resends", len(h.calls))
	}
	for i, from := range []int64{0, 3, 3, 3, 6, 6, 6} {
		run := 1
		if i == 0 {
			run = 0
		}
		followCall(t, h.calls[i], "logs", run, from, true)
	}
}

func TestTaskWaitRetriesPinnedRun(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		followWaitReply(t, "", ""), {code: 255}, {code: 255}, followWaitReply(t, task.Succeeded, ""),
	}}
	a, out, stderr := followClient(t, h)
	code := a.run(taskCallContext(t), []string{"task", "wait", "t-abcdefgh"})
	if code != 0 || out.String() != "succeeded\n" || strings.Count(stderr.String(), "reconnecting") != 1 || len(h.calls) != 4 {
		t.Fatalf("F6 wait = exit %d, stdout %q, stderr %q, %d calls", code, out, stderr, len(h.calls))
	}
	for i, call := range h.calls {
		run := 1
		if i == 0 {
			run = 0
		}
		followCall(t, call, "wait", run, 0, true)
	}
}

func TestTaskFollowChecksFeaturesOnce(t *testing.T) {
	pending := followWaitReply(t, "", "")
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		pending, pending, pending, followWaitReply(t, task.Succeeded, ""),
	}}
	a, out, stderr := followClient(t, h)
	if code := a.run(taskCallContext(t), []string{"task", "wait", "t-abcdefgh"}); code != 0 ||
		out.String() != "succeeded\n" || stderr.Len() != 0 {
		t.Fatalf("F7 wait = exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if h.versions != 1 || len(h.calls) != 4 {
		t.Fatalf("F7 holds = %d task logins, %d feature logins; want 4 and 1", len(h.calls), h.versions)
	}
}

func TestTaskWaitReportsRescueHoldOnce(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		held := followWaitReply(t, "", "stage needs rescue")
		h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{held, held, followWaitReply(t, task.Succeeded, "")}}
		a, out, stderr := followClient(t, h)
		args, want := []string{"task", "wait", "t-abcdefgh"}, "succeeded\n"
		if asJSON {
			args, want = append(args, "--json"), h.replies[2].text
		}
		code := a.run(taskCallContext(t), args)
		if code != 0 || out.String() != want || strings.Count(stderr.String(), "stage needs rescue") != 1 || len(h.calls) != 3 {
			t.Fatalf("F8 held wait = exit %d, stdout %q, stderr %q, %d calls", code, out, stderr, len(h.calls))
		}
	}
}

func TestTaskLogsTerminalSafety(t *testing.T) {
	data := []byte("hello\x1b]52;c;c2VjcmV0\x07\n")
	for _, destination := range []string{"terminal", "pipe", "file", "json"} {
		t.Run(destination, func(t *testing.T) {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
				followLogReply(t, 1, 0, task.StageCommand, data[:6], false),
				followLogReply(t, 1, 6, task.StageCommand, data[6:], true),
			}}
			a, out, stderr := followClient(t, h)
			a.stdoutTerminal = destination == "terminal" || destination == "json"
			args := []string{"task", "logs", "t-abcdefgh"}
			want := string(data)
			var file *os.File
			if destination == "terminal" {
				want = "hello\\x1b]52;c;c2VjcmV0\\x07\n"
			}
			if destination == "json" {
				args = append(args, "--json")
				want = h.replies[0].text + h.replies[1].text
			}
			if destination == "file" {
				var err error
				file, err = os.CreateTemp(t.TempDir(), "log")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				a.stdout = file
			}
			code := a.run(taskCallContext(t), args)
			if file != nil {
				if _, err := file.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(out, file); err != nil {
					t.Fatal(err)
				}
			}
			if code != 0 || out.String() != want || stderr.Len() != 0 {
				t.Fatalf("F9 %s = exit %d, stdout %q, stderr %q; want %q", destination, code, out, stderr, want)
			}
		})
	}
}

func TestTaskFollowUsageRefusalsAndCancellation(t *testing.T) {
	for _, cmd := range []string{"logs", "wait"} {
		for _, args := range [][]string{{"bad"}, {"t-abcdefgh", "--run", "-1"}, {"t-abcdefgh", "--run", "oops"}} {
			h := &taskCallHost{features: `["tasks"]`}
			a, out, stderr := followClient(t, h)
			code := a.run(taskCallContext(t), append([]string{"task", cmd}, args...))
			if code != 2 || out.Len() != 0 || stderr.Len() == 0 || len(h.calls) != 0 || h.versions != 0 {
				t.Fatalf("%s usage = exit %d, stdout %q, stderr %q; want offline usage error", cmd, code, out, stderr)
			}
		}
		for _, reply := range []taskHostReply{
			{code: 1, text: `{"v":1,"error":"no such task","errorCode":"NO_TASK"}`},
			{text: `{"v":1}`},
		} {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{reply}}
			a, _, stderr := followClient(t, h)
			args := []string{"task", cmd, "t-abcdefgh"}
			if cmd == "logs" {
				args = append(args, "-f")
			}
			if code := a.run(taskCallContext(t), args); code != 1 || stderr.Len() == 0 || len(h.calls) != 1 {
				t.Fatalf("%s refusal = exit %d, stderr %q, %d calls; want error without retry", cmd, code, stderr, len(h.calls))
			}
		}
		h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: -1, err: errors.New("connection reset")}}}
		a, _, _ := followClient(t, h)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		args := []string{"task", cmd, "t-abcdefgh"}
		if cmd == "logs" {
			args = append(args, "-f")
		}
		if code := a.run(ctx, args); code != 1 || ctx.Err() == nil || len(h.calls) != 2 {
			cancel()
			t.Fatalf("%s cancelled retry = exit %d, %d calls", cmd, code, len(h.calls))
		}
		cancel()
	}
}
