package taskguest

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

// runner runs runner.sh locally with sh, as the machine would, with env added.
func runner(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "runner")
	if err := os.WriteFile(script, Runner, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

func TestStageStartsOnce(t *testing.T) {
	run := t.TempDir()
	out, code := runner(t, []string{"INVOCATION_ID=one"}, "start", run, "command", "--",
		"sh", "-c", "echo ran")
	if code != 0 || out != "ran\n" {
		t.Fatalf("first start = %q %d", out, code)
	}
	out, code = runner(t, []string{"INVOCATION_ID=two"}, "start", run, "command", "--",
		"sh", "-c", "echo ran again")
	if code != ExitTryAgain || out != "" {
		t.Fatalf("second start = %q %d, want exit %d and nothing run", out, code, ExitTryAgain)
	}
}

func TestExitRecordIsWrittenOnceByItsInvocation(t *testing.T) {
	run := t.TempDir()
	if _, code := runner(t, []string{"INVOCATION_ID=one"}, "start", run, "command", "--",
		"true"); code != 0 {
		t.Fatal("start failed")
	}
	// A refused second launch stops too. Its ExecStopPost must not record anything.
	runner(t, []string{"INVOCATION_ID=two", "SERVICE_RESULT=exit-code", "EXIT_CODE=exited",
		"EXIT_STATUS=75"}, "exit", run, "command")
	if _, err := os.Stat(filepath.Join(run, "command.exit")); !os.IsNotExist(err) {
		t.Fatal("another invocation wrote the exit record")
	}
	ok := []string{"INVOCATION_ID=one", "SERVICE_RESULT=success", "EXIT_CODE=exited",
		"EXIT_STATUS=0"}
	runner(t, ok, "exit", run, "command")
	runner(t, append(ok[:1:1], "SERVICE_RESULT=signal", "EXIT_CODE=killed", "EXIT_STATUS=KILL"),
		"exit", run, "command")

	out, _ := runner(t, nil, "look", run, "command", "hib-x.service")
	seen, err := ParseLook([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := task.ExitRecord{Result: "success", Code: "exited", Status: 0}
	if seen.Exit == nil || *seen.Exit != want || !seen.Marker || seen.Verdict() != Exited {
		t.Fatalf("look = %+v exit %+v, want the first record kept", seen, seen.Exit)
	}
}

func TestLogIsReadInBoundedRanges(t *testing.T) {
	run := t.TempDir()
	content := bytes.Repeat([]byte("0123456789\x00\xff\n"), 1000)
	if err := os.WriteFile(filepath.Join(run, "command.log"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for {
		out, code := runner(t, nil, "log", run, "command",
			strconv.Itoa(len(got)), "4096")
		if code != 0 {
			t.Fatalf("log exited %d: %s", code, out)
		}
		size, data, err := ParseLog([]byte(out))
		if err != nil {
			t.Fatal(err)
		}
		if size != int64(len(content)) || len(data) > 4096 {
			t.Fatalf("size %d, read %d", size, len(data))
		}
		if len(data) == 0 {
			break
		}
		got = append(got, data...)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("the ranges don't add up to the log")
	}
}

func TestVerifyChecksEveryUpload(t *testing.T) {
	root := t.TempDir()
	prompt := []byte("fix it: \"quotes\"\n-- and dashes\n")
	sum := sha256.Sum256(prompt)
	r := task.Run{ID: task.RunID{Task: "t-abcdefgh", N: 1}, Spec: task.Spec{
		Inputs: []task.File{{Name: task.PromptName, Size: int64(len(prompt)), Hash: sum}}}}
	blob := func(h task.Hash) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(prompt)), nil
	}
	for _, f := range Uploads(r, blob) {
		local := filepath.Join(root, strings.TrimPrefix(f.Path, Root))
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := filepath.Join(root, strings.TrimPrefix(RunDir(r.ID), Root))
	if out, code := runner(t, nil, "verify", run); code != 0 {
		t.Fatalf("verify refused whole uploads: %s", out)
	}
	if err := os.WriteFile(filepath.Join(run, "in", "prompt"), []byte("cut"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := runner(t, nil, "verify", run); code == 0 {
		t.Fatal("verify accepted a short upload")
	}
}

func TestLaunchKeepsThePromptOutOfArgv(t *testing.T) {
	r := task.Run{ID: task.RunID{Task: "t-abcdefgh", N: 2}, Spec: task.Spec{
		Command: []string{"codex", "exec", "-"},
		Inputs:  []task.File{{Name: task.PromptName}}}}
	argv := Launch(r, task.StageCommand, 90*time.Minute)
	want := []string{"--unit=hib-t-abcdefgh-r2-command.service", "--slice=hib-task.slice",
		"--property=RuntimeMaxSec=5400",
		"--property=StandardInput=file:/var/lib/hib-task/t-abcdefgh/r2/in/prompt"}
	for _, w := range want {
		if !slices.Contains(argv, w) {
			t.Errorf("Launch argv lacks %q", w)
		}
	}
	if !slices.Equal(argv[len(argv)-3:], r.Spec.Command) {
		t.Errorf("Launch argv ends %q, want the command", argv[len(argv)-3:])
	}
	setup := Launch(r, task.StageSetup, time.Hour)
	if !slices.Contains(setup, "--property=StandardInput=null") {
		t.Error("setup reads the prompt")
	}
}

func TestVerdict(t *testing.T) {
	exit := &task.ExitRecord{Result: "success"}
	tests := []struct {
		seen Seen
		want Verdict
	}{
		{Seen{}, NotStarted},
		{Seen{Load: "not-found", Active: "inactive"}, NotStarted},
		{Seen{Load: "loaded", Active: "activating"}, Running},
		{Seen{Marker: true, Load: "loaded", Active: "active", Freezer: "frozen"}, Running},
		{Seen{Marker: true, Load: "loaded", Active: "deactivating"}, Running},
		{Seen{Marker: true, Exit: exit, Load: "not-found"}, Exited},
		{Seen{Marker: true, Load: "not-found", Active: "inactive"}, Vanished},
		{Seen{Marker: true, Load: "loaded", Active: "failed"}, Vanished},
	}
	for _, tt := range tests {
		if got := tt.seen.Verdict(); got != tt.want {
			t.Errorf("%+v: Verdict = %s, want %s", tt.seen, got, tt.want)
		}
	}
}
