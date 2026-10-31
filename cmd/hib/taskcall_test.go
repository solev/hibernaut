package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

type taskHostReply struct {
	code int
	text string
	err  error
}

type taskCallHost struct {
	features string
	replies  []taskHostReply
	calls    [][]string
	inputs   [][]byte
	times    []time.Time
	versions int
}

func (h *taskCallHost) ssh(ctx context.Context, args []string, in io.Reader, out, _ io.Writer) (int, error) {
	if _, ok := ctx.Deadline(); !ok {
		return -1, errors.New("fake ssh received no deadline")
	}
	i := slices.Index(args, "--")
	words := shellFields(strings.Join(args[i+2:], " "))
	if slices.Equal(words, []string{"hibd", "version", "--json"}) {
		h.versions++
		fmt.Fprintf(out, `{"v":1,"features":%s}`+"\n", h.features)
		return 0, nil
	}
	h.calls = append(h.calls, words)
	h.times = append(h.times, time.Now())
	var input []byte
	if in != nil {
		var err error
		input, err = io.ReadAll(in)
		if err != nil {
			return -1, err
		}
	}
	h.inputs = append(h.inputs, input)
	reply := h.replies[min(len(h.calls)-1, len(h.replies)-1)]
	io.WriteString(out, reply.text)
	return reply.code, reply.err
}

func taskCallClient(t *testing.T, h *taskCallHost) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, stderr bytes.Buffer
	a := &app{stdout: &out, stderr: &stderr, ssh: h.ssh, configFlag: t.TempDir()}
	return a, &out, &stderr
}

func taskCallContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func taskCallExit(a *app, result taskCallResult, err error) int {
	if err != nil {
		return a.fail(err)
	}
	_, err = a.stdout.Write(result.raw)
	if err != nil {
		return a.fail(err)
	}
	return 0
}

func TestTaskCallRequiresTheTasksFeature(t *testing.T) {
	for _, path := range []string{"task ls", "recipe ls"} {
		t.Run(path, func(t *testing.T) {
			h := &taskCallHost{features: "[]", replies: []taskHostReply{{text: `{"v":1}`}}}
			a, out, stderr := taskCallClient(t, h)
			result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
				taskCallOptions{path: path, timeout: shortTimeout})
			if code := taskCallExit(a, result, err); code != 1 || out.Len() != 0 ||
				!strings.Contains(stderr.String(), "Update the host") {
				t.Errorf("%s = exit %d, stdout %q, stderr %q; want 1 and update guidance", path, code, out, stderr)
			}
			if h.versions != 1 || len(h.calls) != 0 {
				t.Errorf("old host received %d version calls and %q task calls; want only version", h.versions, h.calls)
			}
		})
	}
}

func TestTaskCallReplaysTheLostReply(t *testing.T) {
	for _, first := range []taskHostReply{
		{code: 255, text: `{"v":`},
		{code: -1, err: errors.New("connection reset")},
	} {
		for _, lostTwice := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", first.code, lostTwice), func(t *testing.T) {
				last := taskHostReply{text: "{ \"v\":1, \"requestId\":\"submit-1\" }\n"}
				if lostTwice {
					last = first
				}
				h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{first, last}}
				a, out, stderr := taskCallClient(t, h)
				input := []byte("{\"requestId\":\"submit-1\",\"command\":[\"true\"]}\n\x00prompt\xfffile")
				opens, closes := 0, 0
				replay := func() (io.ReadCloser, error) {
					opens++
					return &taskTestInput{Reader: bytes.NewReader(input), close: func() { closes++ }}, nil
				}
				result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
					taskCallOptions{path: "task start", requestID: "submit-1", input: replay, timeout: shortTimeout})
				code := taskCallExit(a, result, err)
				if lostTwice {
					if code != 1 || !strings.Contains(stderr.String(), "hib task show --request 'submit-1'") {
						t.Errorf("two lost replies = exit %d, stderr %q; want lookup guidance", code, stderr)
					}
				} else if code != 0 || out.String() != last.text || result.reply.RequestID != "submit-1" {
					t.Errorf("lost reply = exit %d, stdout %q, reply %+v; want exact second reply", code, out, result.reply)
				}
				if len(h.calls) != 2 || opens != 2 || closes != 2 {
					t.Fatalf("replay = %d sends, %d opens, %d closes; want two of each", len(h.calls), opens, closes)
				}
				if !slices.Equal(h.calls[0], h.calls[1]) || !bytes.Equal(h.inputs[0], input) ||
					!bytes.Equal(h.inputs[1], input) || result.requestID != "submit-1" {
					t.Errorf("replay changed request identity or bytes: %q, %q, %q", h.calls, h.inputs, result.requestID)
				}
				if !slices.Equal(h.calls[0], []string{"sudo", "-n", "hibd", "task", "start", "-json"}) {
					t.Errorf("start invocation = %q; want sudo hibd task start -json", h.calls[0])
				}
			})
		}
	}
}

type taskTestInput struct {
	io.Reader
	close func()
}

func (in *taskTestInput) Close() error { in.close(); return nil }

func TestTaskCallRetriesOnlyBusyRefusals(t *testing.T) {
	for _, code := range []hibapi.ErrorCode{hibapi.CodeBusy, hibapi.CodeQueueFull,
		hibapi.CodeInputFull, hibapi.CodeConflict, hibapi.CodeBadRequest} {
		t.Run(string(code), func(t *testing.T) {
			text := fmt.Sprintf(`{"v":1,"error":"host refuses %s","errorCode":%q,"requestId":"cancel-1","retryable":true}`+"\n", code, code)
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 1, text: text}}}
			a, out, stderr := taskCallClient(t, h)
			result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
				taskCallOptions{path: "task cancel", args: []string{"t-abcdefgh"}, requestID: "cancel-1", timeout: shortTimeout})
			if exit := taskCallExit(a, result, err); exit != 1 || !strings.Contains(stderr.String(), "host refuses "+string(code)) {
				t.Errorf("%s = exit %d, stdout %q, stderr %q; want hibd's refusal", code, exit, out, stderr)
			}
			want := 1
			if code == hibapi.CodeBusy {
				want = 4
			}
			if len(h.calls) != want {
				t.Fatalf("%s sent %d times, want %d", code, len(h.calls), want)
			}
			for i, call := range h.calls {
				if !slices.Equal(call, []string{"sudo", "-n", "hibd", "task", "cancel",
					"t-abcdefgh", "-request-id", "cancel-1", "-json"}) {
					t.Errorf("attempt %d changed invocation or request ID: %q", i, call)
				}
				if i > 0 && h.times[i].Sub(h.times[i-1]) < 100*time.Millisecond {
					t.Errorf("BUSY attempts %d and %d weren't spaced out", i-1, i)
				}
			}
			if string(result.raw) != text {
				t.Errorf("final raw reply = %q, want %q", result.raw, text)
			}
		})
	}
}

func TestTaskCallBusyThenSuccess(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		{code: 1, text: `{"v":1,"error":"locked","errorCode":"BUSY"}`},
		{text: "{ \"v\":1, \"requestId\":\"rescue-1\" }\n"},
	}}
	a, out, stderr := taskCallClient(t, h)
	result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
		taskCallOptions{path: "task rescue", args: []string{"t-abcdefgh"}, requestID: "rescue-1", timeout: shortTimeout})
	if code := taskCallExit(a, result, err); code != 0 || out.String() != h.replies[1].text ||
		stderr.Len() != 0 || len(h.calls) != 2 {
		t.Errorf("BUSY then success = exit %d, stdout %q, stderr %q, %d sends", code, out, stderr, len(h.calls))
	}
}

func TestTaskCallBoundsBusyWaiting(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		{code: 1, text: `{"v":1,"error":"locked","errorCode":"BUSY"}`},
	}}
	a, _, _ := taskCallClient(t, h)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := a.taskCall(ctx, &config{Host: testHost}, taskCallOptions{path: "task ls", timeout: shortTimeout})
	if !errors.Is(err, context.DeadlineExceeded) || len(h.calls) != 1 {
		t.Errorf("deadline = %v, %d sends; want deadline and no second send", err, len(h.calls))
	}
}

func TestOrdinaryRemoteCallsNeverResend(t *testing.T) {
	for _, reply := range []taskHostReply{
		{code: 255}, {code: -1, err: errors.New("connection reset")},
		{code: 1, text: `{"v":1,"error":"locked","errorCode":"BUSY"}`},
	} {
		h := &taskCallHost{replies: []taskHostReply{reply}}
		a, _, _ := taskCallClient(t, h)
		_, err := a.remote(taskCallContext(t), &config{Host: testHost}, shortTimeout, "ls", "--json")
		if err == nil || len(h.calls) != 1 {
			t.Errorf("ordinary remote = %v, %d sends; want error and one send", err, len(h.calls))
		}
	}
}

func TestGeneratedTaskRequestIDPassesHibd(t *testing.T) {
	id, err := taskRequestID("")
	if err != nil {
		t.Fatal(err)
	}
	h := newHost(t)
	c := newClient(t, h)
	body, err := json.Marshal(hibapi.TaskStart{RequestID: id, Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.taskCall(taskCallContext(t), &config{Host: testHost},
		taskCallOptions{path: "task start", requestID: id, timeout: shortTimeout,
			input: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(append(body, '\n'))), nil
			}})
	if err != nil || result.reply.Task == nil || result.reply.RequestID != id {
		t.Errorf("generated ID %q = %+v, %v; want hibd's accepted task receipt", id, result.reply, err)
	}
}

func TestTaskInputCanReplayAnOpenFileWithoutItsPath(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	input := []byte("header\nprompt\x00files")
	if _, err := file.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file.Name()); err != nil {
		t.Fatal(err)
	}
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 255}, {text: `{"v":1,"requestId":"file-1"}`}}}
	a, _, _ := taskCallClient(t, h)
	_, err = a.taskCall(taskCallContext(t), &config{Host: testHost},
		taskCallOptions{path: "task start", requestID: "file-1", timeout: shortTimeout,
			input: func() (io.ReadCloser, error) {
				return io.NopCloser(io.NewSectionReader(file, 0, int64(len(input)))), nil
			}})
	if err != nil || len(h.inputs) != 2 || !bytes.Equal(h.inputs[0], input) || !bytes.Equal(h.inputs[1], input) {
		t.Errorf("unlinked input replay = %q, %v; want two identical inputs", h.inputs, err)
	}
}

func TestTaskCallSharesOneTransportRetryAcrossBusyReplies(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		{code: 255}, {code: 1, text: `{"v":1,"error":"locked","errorCode":"BUSY"}`}, {code: 255},
	}}
	a, _, stderr := taskCallClient(t, h)
	result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
		taskCallOptions{path: "task rm", args: []string{"t-abcdefgh"}, requestID: "remove-1", timeout: shortTimeout})
	if code := taskCallExit(a, result, err); code != 1 || len(h.calls) != 3 ||
		!strings.Contains(stderr.String(), "hib task show --request 'remove-1'") {
		t.Errorf("lost, BUSY, lost = exit %d, %d sends, stderr %q; want one transport resend", code, len(h.calls), stderr)
	}
}

func TestTaskCallEveryFailureAfterResendHasLookupHint(t *testing.T) {
	cases := map[string]taskHostReply{
		"transport":  {code: 255},
		"version":    {code: 1, text: `{"v":99}`},
		"unreadable": {text: `{"v":`},
	}
	for _, code := range []hibapi.ErrorCode{hibapi.CodeBusy, hibapi.CodeNoTask} {
		cases[string(code)] = taskHostReply{code: 1,
			text: fmt.Sprintf(`{"v":1,"error":"host refuses %s","errorCode":%q}`, code, code)}
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
				{code: -1, err: errors.New("connection reset")}, reply,
			}}
			a, _, stderr := taskCallClient(t, h)
			result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
				taskCallOptions{path: "task start", requestID: "submit-1", timeout: shortTimeout})
			if code := taskCallExit(a, result, err); code != 1 ||
				!strings.Contains(stderr.String(), "hib task show --request 'submit-1'") {
				t.Errorf("lost then %s = exit %d, stderr %q; want lookup guidance", name, code, stderr)
			}
			sends := 2
			if name == string(hibapi.CodeBusy) {
				sends = 5
			}
			if len(h.calls) != sends || result.requestID != "submit-1" {
				t.Errorf("lost then %s = %d sends, ID %q; want %d sends and the same ID",
					name, len(h.calls), result.requestID, sends)
			}
		})
	}
}

func TestTaskCallUncertainFailureHasLookupHint(t *testing.T) {
	for _, code := range []hibapi.ErrorCode{hibapi.CodeBusy, hibapi.CodeNoTask, ""} {
		for _, path := range []string{"task start", "task cancel", "task rescue", "task rm"} {
			t.Run(path+"/"+string(code), func(t *testing.T) {
				h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 1,
					text: fmt.Sprintf(`{"v":1,"error":"locked","errorCode":%q}`, code)}}}
				a, out, stderr := taskCallClient(t, h)
				result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
					taskCallOptions{path: path, args: []string{"t-abcdefgh"}, requestID: "submit-1", timeout: shortTimeout})
				if exit := taskCallExit(a, result, err); exit != 1 || out.Len() != 0 ||
					!strings.Contains(stderr.String(), "hib task show --request 'submit-1'") {
					t.Errorf("%s = exit %d, stderr %q; want lookup guidance", code, exit, stderr)
				}
				sends := 1
				if code == hibapi.CodeBusy {
					sends = 4
				}
				if len(h.calls) != sends {
					t.Errorf("%s = %d sends, want %d", code, len(h.calls), sends)
				}
			})
		}
	}
}

func TestTaskCallRefusalsHaveNoLookupHint(t *testing.T) {
	for _, code := range []hibapi.ErrorCode{hibapi.CodeBadRequest, hibapi.CodeConflict,
		hibapi.CodeQueueFull, hibapi.CodeInputFull, hibapi.CodeRunLive, hibapi.CodeNotLive} {
		for _, resent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resent=%v", code, resent), func(t *testing.T) {
				replies := []taskHostReply{{code: 1,
					text: fmt.Sprintf(`{"v":1,"error":"refused","errorCode":%q}`, code)}}
				if resent {
					replies = append([]taskHostReply{{code: 255}}, replies...)
				}
				h := &taskCallHost{features: `["tasks"]`, replies: replies}
				a, out, stderr := taskCallClient(t, h)
				result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
					taskCallOptions{path: "task start", requestID: "submit-1", timeout: shortTimeout})
				if exit := taskCallExit(a, result, err); exit != 1 || out.Len() != 0 ||
					!strings.Contains(stderr.String(), "refused") ||
					strings.Contains(stderr.String(), "hib task show --request") || len(h.calls) != len(replies) {
					t.Errorf("%s = exit %d, %d sends, stderr %q; want refusal without lookup guidance",
						code, exit, len(h.calls), stderr)
				}
			})
		}
	}
}

func TestTaskCallCancelledBusyWaitHasLookupHint(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		{code: 1, text: `{"v":1,"error":"locked","errorCode":"BUSY"}`},
	}}
	a, _, stderr := taskCallClient(t, h)
	ctx, cancel := context.WithCancel(taskCallContext(t))
	defer cancel()
	ssh := a.ssh
	a.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		code, err := ssh(ctx, args, in, out, stderr)
		if len(h.calls) == 1 {
			cancel()
		}
		return code, err
	}
	result, err := a.taskCall(ctx, &config{Host: testHost},
		taskCallOptions{path: "task start", requestID: "submit-1", timeout: shortTimeout})
	if code := taskCallExit(a, result, err); code != 1 || !errors.Is(err, context.Canceled) ||
		!strings.Contains(stderr.String(), "hib task show --request 'submit-1'") || len(h.calls) != 1 {
		t.Errorf("cancelled BUSY wait = exit %d, %d sends, stderr %q; want cancellation and lookup guidance",
			code, len(h.calls), stderr)
	}
}

func TestTaskCallCancelledBusyWaitAfterResendHasLookupHint(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		{code: -1, err: errors.New("connection reset")},
		{code: 1, text: `{"v":1,"error":"locked","errorCode":"BUSY"}`},
	}}
	a, _, stderr := taskCallClient(t, h)
	ctx, cancel := context.WithCancel(taskCallContext(t))
	defer cancel()
	ssh := a.ssh
	a.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		code, err := ssh(ctx, args, in, out, stderr)
		if len(h.calls) == 2 {
			cancel()
		}
		return code, err
	}
	result, err := a.taskCall(ctx, &config{Host: testHost},
		taskCallOptions{path: "task start", requestID: "submit-1", timeout: shortTimeout})
	if code := taskCallExit(a, result, err); code != 1 || !errors.Is(err, context.Canceled) ||
		!strings.Contains(stderr.String(), "hib task show --request 'submit-1'") || len(h.calls) != 2 {
		t.Errorf("cancelled BUSY wait after resend = exit %d, %d sends, stderr %q; want cancellation and lookup guidance",
			code, len(h.calls), stderr)
	}
}

func TestTaskCallGeneratesAndReusesARequestID(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 255}, {text: `{"v":1}`}}}
	a, _, _ := taskCallClient(t, h)
	result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
		taskCallOptions{path: "task cancel", args: []string{"t-abcdefgh"}, timeout: shortTimeout})
	if err != nil || result.requestID == "" || len(result.requestID) > 128 || len(h.calls) != 2 {
		t.Fatalf("generated request = %q, %d sends, %v", result.requestID, len(h.calls), err)
	}
	for _, call := range h.calls {
		i := slices.Index(call, "-request-id")
		if i < 0 || i+1 >= len(call) || call[i+1] != result.requestID {
			t.Errorf("request ID %q isn't the generated ID %q", call, result.requestID)
		}
	}
}

func TestTaskCallUsesItsTimeoutAndHonorsCancellation(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 255}}}
	a, _, _ := taskCallClient(t, h)
	ssh := a.ssh
	var duration time.Duration
	a.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		if strings.Contains(strings.Join(args, " "), "'task'") {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("task SSH attempt has no deadline")
			}
			duration = time.Until(deadline)
		}
		return ssh(ctx, args, in, out, stderr)
	}
	_, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
		taskCallOptions{path: "task ls", timeout: 250 * time.Millisecond})
	if err == nil || duration <= 0 || duration > 250*time.Millisecond {
		t.Errorf("call timeout = %v, %v; want the supplied timeout", duration, err)
	}
	ctx, cancel := context.WithCancel(taskCallContext(t))
	a.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		code, err := ssh(ctx, args, in, out, stderr)
		if code == 255 {
			cancel()
			return -1, ctx.Err()
		}
		return code, err
	}
	h.calls = nil
	_, err = a.taskCall(ctx, &config{Host: testHost}, taskCallOptions{path: "task ls", timeout: shortTimeout})
	if !errors.Is(err, context.Canceled) || len(h.calls) != 1 {
		t.Errorf("cancelled transport = %v, %d sends; want no resend", err, len(h.calls))
	}
}

func TestTaskCallSurfacesInputErrorsWithoutResending(t *testing.T) {
	for _, input := range []func() (io.ReadCloser, error){
		func() (io.ReadCloser, error) { return nil, errors.New("cannot open input") },
		func() (io.ReadCloser, error) { return nil, nil },
	} {
		h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: `{"v":1}`}}}
		a, _, stderr := taskCallClient(t, h)
		result, err := a.taskCall(taskCallContext(t), &config{Host: testHost},
			taskCallOptions{path: "task start", requestID: "input-1", input: input, timeout: shortTimeout})
		if code := taskCallExit(a, result, err); code != 1 || stderr.Len() == 0 || len(h.calls) != 0 {
			t.Errorf("input error = exit %d, %q, %d sends; want a visible local failure", code, stderr, len(h.calls))
		}
	}
}
