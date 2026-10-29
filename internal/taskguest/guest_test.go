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
	return runArgv(t, env, append([]string{"sh", script}, args...))
}

func runArgv(t *testing.T, env, argv []string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", `exec "$@"`, "guest"}, argv...)...)
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

	g := newShellGuest(t)
	out, code := g.run(t, []string{"sh", g.runner, "look", run, "command", "hib-x.service"})
	if code != 0 {
		t.Fatalf("look exited %d: %s", code, out)
	}
	seen, err := ParseLook([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := task.ExitRecord{Result: "success", Code: "exited", Status: 0}
	if seen.Exit == nil || *seen.Exit != want || !seen.Marker || seen.Verdict() != Exited {
		t.Fatalf("look = %+v exit %+v, want the first record kept", seen, seen.Exit)
	}
}

type shellGuest struct {
	cgroup string
	runner string
	bin    string
	env    []string
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newShellGuest(t *testing.T) shellGuest {
	t.Helper()
	dir := t.TempDir()
	g := shellGuest{cgroup: filepath.Join(dir, "cgroup"), runner: filepath.Join(dir, "runner"),
		bin: filepath.Join(dir, "bin")}
	writeTestFile(t, g.runner, strings.ReplaceAll(string(Runner), "/sys/fs/cgroup", g.cgroup))
	g.env = []string{"PATH=" + g.bin + ":" + os.Getenv("PATH"), "CGROOT=" + g.cgroup}
	writeTestFile(t, filepath.Join(g.bin, "systemctl"), `#!/bin/sh
set -eu
case $1 in
show)
	if [ "$2" = hib-task.slice ]; then
		echo /hib.slice/hib-task.slice
	else
		printf '%s\n' LoadState=loaded ActiveState=active SubState=running \
			ControlGroup=/hib.slice/hib-task.slice/stage.service FreezerState=running
	fi
	;;
freeze|thaw) echo 'Failed to connect to bus' >&2; exit 1 ;;
kill)
	[ "$2" = --kill-whom=all ] && [ "$3" = --signal=SIGKILL ] || exit 3
	[ ! -f "$CGROOT/kill-fails" ] || exit 4
	[ "$(cat "$CGROOT/hib.slice/hib-task.slice/cgroup.freeze")" = 1 ] || exit 5
	echo killed >"$CGROOT/killed"
	;;
*) echo "unexpected systemctl $*" >&2; exit 1 ;;
esac
`)
	writeTestFile(t, filepath.Join(g.bin, "sleep"), `#!/bin/sh
set -eu
cg="$CGROOT/hib.slice/hib-task.slice"
echo poll >>"$CGROOT/polls"
if [ ! -f "$CGROOT/stuck" ]; then
	printf 'populated 1\nfrozen %s\n' "$(cat "$cg/cgroup.freeze")" >"$cg/cgroup.events"
fi
`)
	return g
}

func TestKillThawsOnlyAfterSIGKILL(t *testing.T) {
	g := newShellGuest(t)
	cg := filepath.Join(g.cgroup, "hib.slice", Slice)
	writeTestFile(t, filepath.Join(cg, "cgroup.freeze"), "1\n")
	writeTestFile(t, filepath.Join(cg, "cgroup.events"), "frozen 1\n")
	id := task.RunID{Task: "t-abcdefgh", N: 1}
	argv := Kill(id, task.StageCommand)
	out, code := g.run(t, argv)
	if code != 0 {
		t.Fatalf("kill exited %d: %s", code, out)
	}
	killed, err := os.ReadFile(filepath.Join(g.cgroup, "killed"))
	if err != nil || string(killed) != "killed\n" {
		t.Fatalf("stage was not killed before thaw: %q, %v", killed, err)
	}
	events, err := os.ReadFile(filepath.Join(cg, "cgroup.events"))
	if err != nil || string(events) != "populated 1\nfrozen 0\n" {
		t.Fatalf("kill left ExecStopPost frozen: %q, %v", events, err)
	}
	writeTestFile(t, filepath.Join(cg, "cgroup.freeze"), "1\n")
	writeTestFile(t, filepath.Join(cg, "cgroup.events"), "frozen 1\n")
	writeTestFile(t, filepath.Join(g.cgroup, "kill-fails"), "")
	if out, code := g.run(t, argv); code == 0 {
		t.Fatalf("kill hid a failed SIGKILL: %s", out)
	}
	freeze, err := os.ReadFile(filepath.Join(cg, "cgroup.freeze"))
	if err != nil || string(freeze) != "1\n" {
		t.Fatalf("a failed kill thawed the stage: %q, %v", freeze, err)
	}
}

func TestLaunchRefusesFrozenSlice(t *testing.T) {
	for _, tt := range []struct {
		name   string
		freeze string
		frozen string
	}{
		{"frozen", "1", "1"},
		{"freezing", "1", "0"},
		{"thawing", "0", "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newShellGuest(t)
			cg := filepath.Join(g.cgroup, "hib.slice", Slice)
			writeTestFile(t, filepath.Join(cg, "cgroup.freeze"), tt.freeze+"\n")
			writeTestFile(t, filepath.Join(cg, "cgroup.events"), "frozen "+tt.frozen+"\n")
			command := []string{"sh", "-c", `printf '%s\n' "$@"`, "stage", "$$", "${HIB_RUN}"}
			r := task.Run{ID: task.RunID{Task: "t-abcdefgh", N: 2},
				Spec: task.Spec{Command: command}}
			argv := Launch(r, task.StageCommand, time.Hour)
			if argv[0] == "sh" {
				argv[1] = g.runner
			}
			writeTestFile(t, filepath.Join(g.bin, "systemd-run"), `#!/bin/sh
while [ "$1" != start ]; do shift; done
shift 3
[ "$1" = -- ] && shift
exec "$@"
`)
			out, code := g.run(t, argv)
			if code != ExitFrozen {
				t.Fatalf("launch did not return ExitFrozen for the %s slice: exit %d: %s",
					tt.name, code, out)
			}
			freeze, err := os.ReadFile(filepath.Join(cg, "cgroup.freeze"))
			if err != nil || string(freeze) != tt.freeze+"\n" {
				t.Fatalf("launch changed an intentional freeze: %q, %v", freeze, err)
			}
			if out, code := g.run(t, Thaw()); code != 0 {
				t.Fatalf("explicit thaw exited %d: %s", code, out)
			}
			if out, code := g.run(t, argv); code != 0 || out != "$$\n${HIB_RUN}\n" {
				t.Fatalf("launch after explicit thaw: exit %d: %q", code, out)
			}
		})
	}
}

func TestLaunchReservesFrozenExitCode(t *testing.T) {
	for _, tool := range []string{"systemctl", "systemd-run"} {
		t.Run(tool, func(t *testing.T) {
			g := newShellGuest(t)
			writeTestFile(t, filepath.Join(g.bin, "systemd-run"), "#!/bin/sh\nexit 0\n")
			writeTestFile(t, filepath.Join(g.bin, tool),
				"#!/bin/sh\nexit "+strconv.Itoa(ExitFrozen)+"\n")
			r := task.Run{ID: task.RunID{Task: "t-abcdefgh", N: 2},
				Spec: task.Spec{Command: []string{"true"}}}
			argv := Launch(r, task.StageCommand, time.Hour)
			argv[1] = g.runner
			if out, code := g.run(t, argv); code == 0 || code == ExitFrozen {
				t.Fatalf("%s failure became success or a frozen refusal: exit %d: %s",
					tool, code, out)
			}
		})
	}
}

func (g shellGuest) run(t *testing.T, argv []string) (string, int) {
	t.Helper()
	argv = slices.Clone(argv)
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "/sys/fs/cgroup", g.cgroup)
	}
	return runArgv(t, g.env, argv)
}

func TestFreezerWaitsForCgroupEvents(t *testing.T) {
	for _, tt := range []struct {
		name string
		argv []string
		from string
		to   string
	}{
		{"freeze", Freeze(), "0", "1"},
		{"thaw", Thaw(), "1", "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newShellGuest(t)
			cg := filepath.Join(g.cgroup, "hib.slice", Slice)
			writeTestFile(t, filepath.Join(cg, "cgroup.freeze"), tt.from+"\n")
			writeTestFile(t, filepath.Join(cg, "cgroup.events"), "populated 1\nfrozen "+tt.from+"\n")
			out, code := g.run(t, tt.argv)
			if code != 0 {
				t.Fatalf("%s exited %d: %s", tt.name, code, out)
			}
			events, err := os.ReadFile(filepath.Join(cg, "cgroup.events"))
			if err != nil || !strings.Contains(string(events), "frozen "+tt.to+"\n") {
				t.Fatalf("%s returned before frozen %s: %q, %v", tt.name, tt.to, events, err)
			}
			polls, err := os.ReadFile(filepath.Join(g.cgroup, "polls"))
			if err != nil || string(polls) != "poll\n" {
				t.Fatalf("%s did not wait for the kernel: %q, %v", tt.name, polls, err)
			}
			writeTestFile(t, filepath.Join(g.cgroup, "stuck"), "")
			writeTestFile(t, filepath.Join(cg, "cgroup.events"), "frozen "+tt.from+"\n")
			out, code = g.run(t, tt.argv)
			if code == 0 || !strings.Contains(out, "did not reach frozen "+tt.to) {
				t.Fatalf("%s accepted a stuck freezer: exit %d: %s", tt.name, code, out)
			}
			polls, err = os.ReadFile(filepath.Join(g.cgroup, "polls"))
			if err != nil || strings.Count(string(polls), "poll\n") != 51 {
				t.Fatalf("%s did not bound its wait: %q, %v", tt.name, polls, err)
			}
		})
	}
}

func TestFreezerWithoutCgroupIsNoop(t *testing.T) {
	g := newShellGuest(t)
	for _, argv := range [][]string{Freeze(), Thaw()} {
		if out, code := g.run(t, argv); code != 0 {
			t.Fatalf("missing cgroup: exit %d: %s", code, out)
		}
	}
}

func TestLookReadsKernelFrozen(t *testing.T) {
	for _, frozen := range []string{"0", "1"} {
		t.Run(frozen, func(t *testing.T) {
			g := newShellGuest(t)
			cg := filepath.Join(g.cgroup, "hib.slice", Slice, "stage.service")
			writeTestFile(t, filepath.Join(cg, "cgroup.events"), "populated 1\nfrozen "+frozen+"\n")
			out, code := g.run(t, []string{"sh", g.runner, "look", t.TempDir(), "command", "stage.service"})
			if code != 0 {
				t.Fatalf("look exited %d: %s", code, out)
			}
			seen, err := ParseLook([]byte(out))
			if err != nil || seen.Frozen() != (frozen == "1") {
				t.Fatalf("look ignored the kernel's frozen %s: %+v, %v", frozen, seen, err)
			}
		})
	}
	if _, err := ParseLook([]byte("frozen=invalid\n")); err == nil {
		t.Fatal("look accepted a malformed frozen value")
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

func TestLaunchPreservesCommand(t *testing.T) {
	r := task.Run{ID: task.RunID{Task: "t-abcdefgh", N: 2}, Spec: task.Spec{
		Command: []string{"sh", "-c", `printf '%s\n' '$$|${HIB_RUN}'`}}}
	argv := Launch(r, task.StageCommand, time.Hour)
	if !slices.Contains(argv, "--expand-environment=no") {
		t.Fatal("Launch lets systemd expand $$ and ${HIB_RUN}")
	}
	if !slices.Equal(argv[len(argv)-len(r.Spec.Command):], r.Spec.Command) {
		t.Fatalf("Launch changed the command: %q", argv)
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
		{Seen{Marker: true, Load: "loaded", Active: "active", CgroupFrozen: true}, Running},
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
