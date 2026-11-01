package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

const startReceipt = "{ \"v\":1, \"requestId\":\"start-1\", \"task\":{\"id\":\"t-abcdefgh\"," +
	"\"created\":\"2026-10-28T12:00:00Z\",\"runs\":[{\"n\":1,\"phase\":\"queued\"," +
	"\"command\":[\"true\"],\"submitted\":\"2026-10-28T12:00:00Z\"}]} }\n"

func taskStartClient(t *testing.T, h *taskCallHost) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a, out, stderr := taskCallClient(t, h)
	parent := t.TempDir()
	dir := filepath.Join(parent, "hibernaut")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"host":"test@localhost"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.getenv = func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return parent
		}
		return ""
	}
	return a, out, stderr
}

func startTask(t *testing.T, a *app, args ...string) int {
	t.Helper()
	return a.run(taskCallContext(t), append([]string{"task", "start"}, args...))
}

func startWire(t *testing.T, b []byte) (hibapi.TaskStart, []byte) {
	t.Helper()
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		t.Fatalf("host received no JSON line in %q", b)
	}
	var req hibapi.TaskStart
	if err := json.Unmarshal(b[:i], &req); err != nil {
		t.Fatal(err)
	}
	return req, b[i+1:]
}

func TestTaskStartSendsOrderedInput(t *testing.T) {
	for _, promptKind := range []string{"inline", "file", "empty", "none"} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", promptKind, asJSON), func(t *testing.T) {
				dir := t.TempDir()
				file := filepath.Join(dir, "data.bin")
				payload := []byte{0, 255, '\n', 1}
				if err := os.WriteFile(file, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: startReceipt}}}
				a, out, stderr := taskStartClient(t, h)
				args := []string{"--request-id", "start-1", "--file", file,
					"--image", "custom", "--size", "small", "--timeout", "1h"}
				var prompt []byte
				switch promptKind {
				case "inline":
					prompt = []byte("review\nthis")
					args = append(args, "--prompt", string(prompt))
				case "file":
					prompt = []byte("prompt\x00\xff")
					path := filepath.Join(dir, "instructions")
					if err := os.WriteFile(path, prompt, 0o600); err != nil {
						t.Fatal(err)
					}
					args = append(args, "--prompt-file", path)
				case "empty":
					args = append(args, "--prompt", "")
				}
				if asJSON {
					args = append(args, "--json")
				}
				args = append(args, "--", "echo", "a 'quote'", "--flag")
				code := startTask(t, a, args...)
				want := "t-abcdefgh  queued\n"
				if asJSON {
					want = startReceipt
				}
				if code != 0 || out.String() != want || stderr.Len() != 0 {
					t.Fatalf("start = exit %d, stdout %q, stderr %q; want saved receipt %q", code, out, stderr, want)
				}
				if h.versions != 1 || len(h.calls) != 1 ||
					!slices.Equal(h.calls[0], []string{"sudo", "-n", "hibd", "task", "start", "-json"}) {
					t.Fatalf("host calls = %d versions, %q; want one task start", h.versions, h.calls)
				}
				req, data := startWire(t, h.inputs[0])
				if req.RequestID != "start-1" || !slices.Equal(req.Command, []string{"echo", "a 'quote'", "--flag"}) ||
					req.Image != "custom" || req.Size != "small" || req.TimeoutSeconds != 3600 ||
					len(req.Files) != 1 || req.Files[0] != (hibapi.TaskFile{Name: "data.bin", Size: 4}) {
					t.Errorf("start header = %+v; want command, options and named file", req)
				}
				if (req.Prompt != nil) != (promptKind != "none") ||
					(req.Prompt != nil && req.Prompt.Size != int64(len(prompt))) ||
					!bytes.Equal(data, append(prompt, payload...)) {
					t.Errorf("wire prompt %+v, bytes %q; want prompt first then %q", req.Prompt, data, payload)
				}
			})
		}
	}
}

func TestTaskStartLostReply(t *testing.T) {
	for _, twice := range []bool{false, true} {
		t.Run(fmt.Sprint(twice), func(t *testing.T) {
			lost := taskHostReply{code: -1, err: errors.New("connection reset")}
			last := taskHostReply{text: startReceipt}
			if twice {
				last = lost
			}
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{lost, last}}
			a, out, stderr := taskStartClient(t, h)
			code := startTask(t, a, "--request-id", "start-1", "--prompt", "frozen", "--", "true")
			if twice {
				if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "hib task show --request 'start-1'") {
					t.Errorf("lost twice = exit %d, stdout %q, stderr %q; want lookup hint", code, out, stderr)
				}
			} else if code != 0 || out.String() != "t-abcdefgh  queued\n" || stderr.Len() != 0 {
				t.Errorf("lost reply = exit %d, stdout %q, stderr %q; want queued", code, out, stderr)
			}
			if len(h.inputs) != 2 || !bytes.Equal(h.inputs[0], h.inputs[1]) || !slices.Equal(h.calls[0], h.calls[1]) {
				t.Fatalf("lost reply sent %d inputs, calls %q; want two identical attempts", len(h.inputs), h.calls)
			}
			req, data := startWire(t, h.inputs[0])
			if req.RequestID != "start-1" || string(data) != "frozen" {
				t.Errorf("replay request %+v, body %q; want original ID and bytes", req, data)
			}
		})
	}
}

func TestTaskStartChangedFileReplaysSavedReceipt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note")
	if err := os.WriteFile(file, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{
		{code: -1, err: errors.New("saved but reply lost")}, {text: startReceipt},
	}}
	a, out, stderr := taskStartClient(t, h)
	ssh := a.ssh
	a.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		code, err := ssh(ctx, args, in, out, stderr)
		if len(h.calls) == 1 {
			if writeErr := os.WriteFile(file, []byte("changed during resend"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		} else if len(h.calls) == 2 && !bytes.Equal(h.inputs[0], h.inputs[1]) {
			return 1, errors.New("CONFLICT: the first request is already saved")
		}
		return code, err
	}
	if code := startTask(t, a, "--request-id", "start-1", "--file", file, "--", "true"); code != 0 || out.String() != "t-abcdefgh  queued\n" || stderr.Len() != 0 {
		t.Fatalf("edited file replay = exit %d, stdout %q, stderr %q; want original receipt", code, out, stderr)
	}
	if len(h.inputs) != 2 || !bytes.Equal(h.inputs[0], h.inputs[1]) {
		t.Fatalf("edited file sent %d inputs; want the first bytes twice", len(h.inputs))
	}
	_, data := startWire(t, h.inputs[1])
	if string(data) != "first" {
		t.Errorf("resend input = %q; want first", data)
	}
}

func TestTaskStartLocalRefusals(t *testing.T) {
	dir := t.TempDir()
	one, two := filepath.Join(dir, "a"), filepath.Join(dir, "nested")
	if err := os.Mkdir(two, 0o700); err != nil {
		t.Fatal(err)
	}
	two = filepath.Join(two, "a")
	hidden := filepath.Join(dir, ".env")
	reserved := filepath.Join(dir, task.PromptName)
	large := filepath.Join(dir, "large")
	for _, path := range []string{one, two, hidden, reserved, large} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Truncate(large, task.MaxInputBytes); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		exit int
		want string
	}{
		{"duplicate", []string{"--file", one, "--file", two}, 2, "duplicate"},
		{"hidden", []string{"--file", hidden}, 2, "bad file name"},
		{"reserved", []string{"--file", reserved}, 2, "prompt"},
		{"both prompts", []string{"--prompt", "", "--prompt-file", one}, 2, "alternatives"},
		{"oversized", []string{"--prompt", "x", "--file", large}, 1, "100"},
		{"missing file", []string{"--file", filepath.Join(dir, "missing")}, 1, "no such file"},
		{"directory", []string{"--file", dir}, 1, "regular file"},
		{"bad request ID", []string{"--request-id", string([]byte{255})}, 2, "UTF-8"},
		{"long request ID", []string{"--request-id", strings.Repeat("a", 129)}, 2, "128"},
		{"image", []string{"--image", "../dev"}, 2, "bad name"},
		{"size", []string{"--size", "huge"}, 2, "size"},
		{"timeout", []string{"--timeout", "-1s"}, 2, "timeout"},
		{"fractional timeout", []string{"--timeout", "1ms"}, 2, "seconds"},
		{"recipe", []string{"--recipe", "review"}, 1, "later build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: startReceipt}}}
			a, out, stderr := taskStartClient(t, h)
			getenv, settingsReads := a.getenv, 0
			a.getenv = func(key string) string {
				settingsReads++
				return getenv(key)
			}
			code := startTask(t, a, append(tc.args, "--", "true")...)
			if code != tc.exit || out.Len() != 0 || !strings.Contains(stderr.String(), tc.want) ||
				h.versions != 0 || len(h.calls) != 0 || settingsReads != 0 {
				t.Errorf("local refusal = exit %d, stdout %q, stderr %q, %d version calls, %d sends, %d settings reads",
					code, out, stderr, h.versions, len(h.calls), settingsReads)
			}
		})
	}
}

func TestTaskStartSizeChangeBeforeRead(t *testing.T) {
	for _, content := range []string{"", "grown"} {
		t.Run(fmt.Sprintf("size=%d", len(content)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note")
			if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
				t.Fatal(err)
			}
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: startReceipt}}}
			a, out, stderr := taskStartClient(t, h)
			getenv := a.getenv
			a.getenv = func(key string) string {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				return getenv(key)
			}
			code := startTask(t, a, "--file", path, "--", "true")
			if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "size changed") ||
				h.versions != 0 || len(h.calls) != 0 {
				t.Errorf("size change = exit %d, stdout %q, stderr %q, %d versions, %d sends; want local failure",
					code, out, stderr, h.versions, len(h.calls))
			}
		})
	}
}

func TestTaskStartHostRefusals(t *testing.T) {
	for _, code := range []hibapi.ErrorCode{hibapi.CodeBusy, hibapi.CodeQueueFull, hibapi.CodeInputFull} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", code, asJSON), func(t *testing.T) {
				reply := fmt.Sprintf("{ \"v\":1, \"error\":\"host refuses %s\", \"errorCode\":%q, \"requestId\":\"start-1\" }\n", code, code)
				h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{code: 1, text: reply}}}
				a, out, stderr := taskStartClient(t, h)
				args := []string{"--request-id", "start-1"}
				if asJSON {
					args = append(args, "--json")
				}
				exit := startTask(t, a, append(args, "--", "true")...)
				if exit != 1 {
					t.Errorf("host refusal = exit %d; want 1", exit)
				}
				if asJSON {
					if out.String() != reply {
						t.Errorf("JSON refusal = %q; want exact host reply %q", out, reply)
					}
				} else if out.Len() != 0 || !strings.Contains(stderr.String(), "host refuses "+string(code)) {
					t.Errorf("host refusal stdout %q, stderr %q; want host's words", out, stderr)
				}
				if strings.Contains(stderr.String(), "hib task show --request") != (code == hibapi.CodeBusy) {
					t.Errorf("host refusal lookup hint stderr %q; want hint only for BUSY", stderr)
				}
				want := 1
				if code == hibapi.CodeBusy {
					want = 4
				}
				if len(h.calls) != want {
					t.Fatalf("%s sent %d times; want %d", code, len(h.calls), want)
				}
				for i := 1; i < len(h.inputs); i++ {
					if !bytes.Equal(h.inputs[0], h.inputs[i]) || h.times[i].Sub(h.times[i-1]) < 100*time.Millisecond {
						t.Errorf("BUSY retry changed input or was not spaced out")
					}
				}
			})
		}
	}
}

func TestTaskStartHelpIsAvailable(t *testing.T) {
	h := &taskCallHost{}
	a, out, stderr := taskStartClient(t, h)
	if code := a.run(taskCallContext(t), []string{"help", "task", "start"}); code != 0 ||
		strings.Contains(out.String(), "This command isn't in this build yet.") ||
		!strings.Contains(out.String(), "Recipes come in a later build") || stderr.Len() != 0 || h.versions != 0 {
		t.Errorf("start help stdout %q, stderr %q; want available command and deferred recipes", out, stderr)
	}
}

func TestTaskStartGeneratesRequestID(t *testing.T) {
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: startReceipt}}}
	a, out, stderr := taskStartClient(t, h)
	ssh := a.ssh
	a.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		if in == nil {
			return ssh(ctx, args, in, out, stderr)
		}
		input, err := io.ReadAll(in)
		if err != nil {
			return -1, err
		}
		req, _ := startWire(t, input)
		if req.RequestID == "" || len(req.RequestID) > 128 || !utf8.ValidString(req.RequestID) {
			t.Errorf("generated request ID %q; want 1 to 128 UTF-8 bytes", req.RequestID)
		}
		h.replies[0].text = strings.ReplaceAll(startReceipt, "start-1", req.RequestID)
		return ssh(ctx, args, bytes.NewReader(input), out, stderr)
	}
	if code := startTask(t, a, "--", "true"); code != 0 || out.String() != "t-abcdefgh  queued\n" || stderr.Len() != 0 {
		t.Errorf("generated start = exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

func TestTaskStartEditedFileGetsDurableReceipt(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	file := filepath.Join(t.TempDir(), "note")
	if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh, sends := c.ssh, 0
	var inputs [][]byte
	var first hibapi.Response
	c.ssh = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		if in == nil {
			return ssh(ctx, args, in, out, stderr)
		}
		input, err := io.ReadAll(in)
		if err != nil {
			return -1, err
		}
		inputs = append(inputs, input)
		sends++
		if sends != 1 {
			return ssh(ctx, args, bytes.NewReader(input), out, stderr)
		}
		var saved bytes.Buffer
		code, err := ssh(ctx, args, bytes.NewReader(input), &saved, stderr)
		if code != 0 || err != nil {
			t.Fatalf("first host save = exit %d, %v, %s", code, err, &saved)
		}
		first, err = hibapi.DecodeResponse(saved.Bytes())
		if err != nil || first.Task == nil {
			t.Fatalf("saved receipt = %+v, %v", first, err)
		}
		if err := os.WriteFile(file, []byte("edited after save"), 0o600); err != nil {
			t.Fatal(err)
		}
		return -1, errors.New("saved but reply lost")
	}
	ctx := taskCallContext(t)
	code := c.hibContext(ctx, "task", "start", "--request-id", "saved-edit", "--file", file, "--", "true")
	if code != 0 || c.out.String() != string(first.Task.ID)+"  queued\n" || c.errOut.Len() != 0 ||
		sends != 2 || !bytes.Equal(inputs[0], inputs[1]) {
		t.Fatalf("durable replay = exit %d, stdout %q, stderr %q, %d sends; want original receipt", code, c.out, c.errOut, sends)
	}
	all, err := c.remoteSudo(ctx, &config{Host: testHost}, shortTimeout, callOptions{}, "task", "ls", "-json")
	if err != nil || len(all.Tasks) != 1 || all.Tasks[0].ID != first.Task.ID || len(all.Tasks[0].Runs) != 1 {
		t.Errorf("saved tasks = %+v, %v; want one task and one run", all.Tasks, err)
	}
}

func TestTaskStartCancelledPreparationLeavesNoSpool(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	file := filepath.Join(t.TempDir(), "note")
	if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: startReceipt}}}
	a, out, stderr := taskStartClient(t, h)
	ctx, cancel := context.WithCancel(taskCallContext(t))
	defer cancel()
	getenv := a.getenv
	a.getenv = func(key string) string { cancel(); return getenv(key) }
	code := a.run(ctx, []string{"task", "start", "--file", file, "--", "true"})
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "context canceled") ||
		h.versions != 0 || len(h.calls) != 0 {
		t.Errorf("cancelled prepare = exit %d, stdout %q, stderr %q, %d versions, %d sends", code, out, stderr, h.versions, len(h.calls))
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Errorf("cancelled prepare left temp files %v, %v", entries, err)
	}
}

func TestTaskStartMissingReceiptHasLookupHint(t *testing.T) {
	for _, reply := range []string{`{"v":1}`, strings.ReplaceAll(startReceipt, "start-1", "another-id")} {
		for _, asJSON := range []bool{false, true} {
			h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: reply}}}
			a, out, stderr := taskStartClient(t, h)
			args := []string{"--request-id", "start-1"}
			if asJSON {
				args = append(args, "--json")
			}
			code := startTask(t, a, append(args, "--", "true")...)
			got := stderr.String()
			if asJSON {
				got = out.String()
			}
			if code != 1 || !strings.Contains(got, "hib task show --request") || !strings.Contains(got, "start-1") {
				t.Errorf("missing receipt = exit %d, stdout %q, stderr %q; want lookup hint", code, out, stderr)
			}
		}
	}
}

func BenchmarkTaskStart100MiB(b *testing.B) {
	file := filepath.Join(b.TempDir(), "input.bin")
	f, err := os.Create(file)
	if err != nil {
		b.Fatal(err)
	}
	if err := f.Truncate(task.MaxInputBytes); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(b.Context(), 2*time.Minute)
	defer cancel()
	expected := sha256.New()
	if _, err := io.CopyN(expected, &zeroStartInput{}, task.MaxInputBytes); err != nil {
		b.Fatal(err)
	}
	for _, kind := range []string{"spool", "start"} {
		b.Run(kind, func(b *testing.B) {
			var out, stderr bytes.Buffer
			opens := 0
			var spoolStarted time.Time
			var spoolTime time.Duration
			a := &app{stdout: &out, stderr: &stderr}
			parent := b.TempDir()
			dir := filepath.Join(parent, "hibernaut")
			if err := os.Mkdir(dir, 0o700); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"host":"test@localhost"}`), 0o600); err != nil {
				b.Fatal(err)
			}
			a.getenv = func(string) string { spoolStarted = time.Now(); return parent }
			a.ssh = func(ctx context.Context, _ []string, in io.Reader, out, _ io.Writer) (int, error) {
				if in == nil {
					spoolTime += time.Since(spoolStarted)
					_, err := io.WriteString(out, `{"v":1,"features":["tasks"]}`)
					return 0, err
				}
				opens++
				r, w, err := os.Pipe()
				if err != nil {
					return -1, err
				}
				defer r.Close()
				stop := context.AfterFunc(ctx, func() { _ = r.Close(); _ = w.Close() })
				defer stop()
				done := make(chan error, 1)
				go func() {
					_, err := io.Copy(w, in)
					done <- errors.Join(err, w.Close())
				}()
				stream := bufio.NewReader(r)
				header, err := stream.ReadBytes('\n')
				var req hibapi.TaskStart
				if err == nil {
					err = json.Unmarshal(header, &req)
				}
				sum := sha256.New()
				n, copyErr := io.Copy(sum, stream)
				err = errors.Join(err, copyErr, <-done, ctx.Err())
				if err != nil || n != task.MaxInputBytes || !bytes.Equal(sum.Sum(nil), expected.Sum(nil)) ||
					req.RequestID != "start-1" || len(req.Files) != 1 || req.Files[0].Size != task.MaxInputBytes {
					return -1, fmt.Errorf("pipe received %d bytes, request %+v, error %v", n, req, err)
				}
				_, err = io.WriteString(out, startReceipt)
				return 0, err
			}
			start, err := (taskStartOptions{files: []string{file}}).prepare(
				hibapi.TaskStart{RequestID: "start-1", Command: []string{"true"}})
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(task.MaxInputBytes)
			b.ResetTimer()
			for range b.N {
				if kind == "spool" {
					f, err := start.spool(ctx)
					if err != nil {
						b.Fatal(err)
					}
					info, statErr := f.Stat()
					closeErr := f.Close()
					if statErr != nil || closeErr != nil || info.Size() < task.MaxInputBytes {
						b.Fatalf("spool = %v, %v, %v", info, statErr, closeErr)
					}
				} else {
					out.Reset()
					stderr.Reset()
					if code := a.run(ctx, []string{"task", "start", "--request-id", "start-1", "--file", file, "--", "true"}); code != 0 || out.String() != "t-abcdefgh  queued\n" || stderr.Len() != 0 {
						b.Fatalf("100 MiB start = exit %d, stdout %q, stderr %q", code, &out, &stderr)
					}
				}
			}
			if kind == "start" && opens != b.N {
				b.Fatalf("pipe received %d starts; want %d", opens, b.N)
			}
			b.StopTimer()
			if kind == "start" {
				b.ReportMetric(float64(spoolTime.Nanoseconds())/float64(b.N), "prepare-spool-ns/op")
				b.ReportMetric(100*spoolTime.Seconds()/b.Elapsed().Seconds(), "prepare-spool-%")
			}
		})
	}
}

type zeroStartInput struct{}

func (*zeroStartInput) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestTaskStartInvalidCommandAndHeader(t *testing.T) {
	for _, tc := range []struct {
		args []string
		exit int
		want string
	}{
		{[]string{""}, 2, "executable"},
		{[]string{"echo", "\x00"}, 2, "NUL"},
		{[]string{"echo", string([]byte{255})}, 2, "UTF-8"},
		{[]string{"echo", strings.Repeat("a", 1<<20)}, 1, "JSON"},
	} {
		h := &taskCallHost{features: `["tasks"]`, replies: []taskHostReply{{text: startReceipt}}}
		a, out, stderr := taskStartClient(t, h)
		code := startTask(t, a, append([]string{"--"}, tc.args...)...)
		if code != tc.exit || out.Len() != 0 || !strings.Contains(stderr.String(), tc.want) ||
			h.versions != 0 || len(h.calls) != 0 {
			t.Errorf("invalid command = exit %d, stdout %q, stderr %q, %d versions, %d sends", code, out, stderr, h.versions, len(h.calls))
		}
	}
}

type taskStartCopySink func([]byte) (int, error)

func (f taskStartCopySink) Write(b []byte) (int, error) { return f(b) }

func TestTaskStartSizeChangeDuringRead(t *testing.T) {
	for _, size := range []int64{0, 128<<10 + 1} {
		path := filepath.Join(t.TempDir(), "input")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 128<<10), 0o600); err != nil {
			t.Fatal(err)
		}
		h := &taskCallHost{}
		a, out, stderr := taskStartClient(t, h)
		var readBytes int
		err := (taskStartInput{path: path, size: 128 << 10}).copy(taskCallContext(t), taskStartCopySink(func(b []byte) (int, error) {
			if readBytes == 0 {
				if err := os.Truncate(path, size); err != nil {
					return 0, err
				}
			}
			readBytes += len(b)
			return len(b), nil
		}))
		if err == nil {
			t.Fatal("file size changed during the first read but was accepted")
		}
		if code := a.fail(err); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "size changed") ||
			readBytes == 0 || h.versions != 0 || len(h.calls) != 0 {
			t.Errorf("changed source = exit %d, stdout %q, stderr %q, %d bytes read, %d versions, %d sends",
				code, out, stderr, readBytes, h.versions, len(h.calls))
		}
	}
}
