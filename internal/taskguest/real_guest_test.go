//go:build guest

package taskguest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

func TestGuestRunner(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("guest test needs root in a Linux dev machine")
	}
	for _, argv := range [][]string{{"systemctl", "--version"}, {"uname", "-r"}, {"uptime"}, {"nproc"}} {
		t.Log(strings.TrimSpace(guestOK(t, argv, ExecTimeout)))
	}
	t.Cleanup(func() { guestOK(t, Thaw(), ExecTimeout) })
	t.Run("NoCgroup", func(t *testing.T) {
		if group := guestSlice(t); group != "" {
			t.Skipf("hib-task.slice already has cgroup %s; use a fresh machine for this case", group)
		}
		out := guestOK(t, Freeze(), ExecTimeout)
		answer, err := ParseFreeze([]byte(out))
		if err != nil || answer != FreezeEmpty {
			t.Fatalf("absent cgroup freeze = %q, %v", out, err)
		}
		if group := guestSlice(t); group != "" {
			t.Fatalf("empty freeze created a cgroup: %s", group)
		}
		t.Log("freeze=empty before any stage has run since boot")
	})
	if !t.Run("PrepareSliceBeforeFirstStage", func(t *testing.T) {
		guestOK(t, []string{"systemctl", "start", Slice}, ExecTimeout)
		group := guestSlice(t)
		if group == "" {
			t.Fatal("systemctl start did not create the slice cgroup; image change is out of scope")
		}
		t.Logf("systemctl start created %s before any stage launch", group)
		guestFreeze(t)
		r := guestRun(t, []string{"true"}, nil)
		out, code := guestExec(t, Launch(r, task.StageCommand, time.Minute), ExecTimeout)
		if code != ExitFrozen {
			t.Fatalf("first launch into frozen slice = exit %d: %s", code, out)
		}
		if _, err := os.Stat(filepath.Join(RunDir(r.ID), "command.started")); !os.IsNotExist(err) {
			t.Fatalf("refused first launch made a marker: %v", err)
		}
		guestOK(t, Thaw(), ExecTimeout)
	}) {
		return
	}
	t.Run("CounterSurvivesReload", func(t *testing.T) {
		r := guestRun(t, []string{"sh", "-c",
			`trap 'echo term; exit 0' TERM; while :; do echo tick; sleep 0.1; done`}, nil)
		guestOK(t, Launch(r, task.StageCommand, time.Minute), ExecTimeout)
		guestWait(t, ExecTimeout, func() bool { return strings.Contains(guestLog(t, r), "tick\n") })
		group := guestStageGroup(t, r)
		guestFreeze(t)
		seen := guestLook(t, r)
		if seen.Verdict() != Running || !seen.Frozen() {
			t.Fatalf("look did not report a frozen running counter: %+v", seen)
		}
		before := guestLog(t, r)
		guestOK(t, []string{"systemctl", "daemon-reload"}, ExecTimeout)
		time.Sleep(500 * time.Millisecond)
		if after := guestLog(t, r); after != before || !guestLook(t, r).Frozen() {
			t.Fatalf("counter acted across daemon-reload: before %q, after %q", before, after)
		}
		t.Log("counter held across daemon-reload; look reported frozen=1")
		guestOK(t, Thaw(), ExecTimeout)
		guestWait(t, ExecTimeout, func() bool { return len(guestLog(t, r)) > len(before) })
		guestOK(t, Stop(r.ID, task.StageCommand), task.StopGrace+5*time.Second)
		guestExit(t, r)
		if !strings.Contains(guestLog(t, r), "term\n") {
			t.Fatal("systemctl stop did not deliver TERM")
		}
		guestEmpty(t, group)
		t.Log("thaw resumed the counter; stop delivered TERM")
	})
	t.Run("InputsAndFailedShow", func(t *testing.T) {
		r := guestRun(t, []string{"sh", "-c", `cat "$HIB_INPUTS/data"`}, []byte("guest input\n"))
		guestOK(t, Launch(r, task.StageCommand, time.Minute), ExecTimeout)
		seen := guestExit(t, r)
		if seen.Exit.Result != "success" || guestLog(t, r) != "guest input\n" {
			t.Fatalf("command could not read HIB_INPUTS: exit %+v, log %q", seen.Exit, guestLog(t, r))
		}
		argv := Look(r.ID, task.StageCommand)
		argv[len(argv)-1] = "--hib-guest-invalid-option"
		out := guestOK(t, argv, ExecTimeout)
		seen, err := ParseLook([]byte(out))
		if err != nil || seen.Verdict() != Exited || !strings.Contains(out, "show=failed\n") {
			t.Fatalf("real failed show hid exit: %q, %v", out, err)
		}
		live := guestRun(t, []string{"sleep", "60"}, nil)
		argv = Look(live.ID, task.StageCommand)
		argv[len(argv)-1] = "--hib-guest-invalid-option"
		for _, started := range []bool{false, true} {
			if started {
				guestOK(t, Launch(live, task.StageCommand, time.Minute), ExecTimeout)
				guestWait(t, ExecTimeout, func() bool { return guestLook(t, live).Marker })
			}
			out = guestOK(t, argv, ExecTimeout)
			if seen, err := ParseLook([]byte(out)); err == nil {
				t.Fatalf("real failed show without exit became %s: %q", seen.Verdict(), out)
			}
		}
		t.Log("real systemctl show failure preserved the exit and failed without one")
	})
	t.Run("RuntimeMax", func(t *testing.T) {
		r := guestRun(t, []string{"sleep", "60"}, nil)
		guestOK(t, Launch(r, task.StageCommand, 2*time.Second), ExecTimeout)
		guestWait(t, ExecTimeout, func() bool { return guestLook(t, r).Marker })
		group := guestStageGroup(t, r)
		seen := guestExit(t, r)
		if seen.Exit.Result != "timeout" {
			t.Fatalf("RuntimeMaxSec exit = %+v, want result timeout", seen.Exit)
		}
		guestRecord(t, "RuntimeMaxSec", r, 0)
		guestEmpty(t, group)
	})
	t.Run("StopEscalatesAfterGrace", func(t *testing.T) {
		r := guestRun(t, []string{"sh", "-c",
			`trap 'echo term' TERM; echo ready; while :; do sleep 0.1; done`}, nil)
		guestOK(t, Launch(r, task.StageCommand, time.Minute), ExecTimeout)
		guestWait(t, ExecTimeout, func() bool { return strings.Contains(guestLog(t, r), "ready\n") })
		group := guestStageGroup(t, r)
		begin := time.Now()
		guestOK(t, Stop(r.ID, task.StageCommand), task.StopGrace+5*time.Second)
		seen := guestExit(t, r)
		elapsed := time.Since(begin)
		guestRecord(t, "TimeoutStopSec", r, elapsed)
		if elapsed < task.StopGrace || elapsed > task.StopGrace+5*time.Second {
			t.Fatalf("TERM-to-exit took %s, want 15s through 20s", elapsed)
		}
		if seen.Exit.Result != "timeout" || seen.Exit.Code != "killed" ||
			!strings.Contains(guestLog(t, r), "term\n") {
			t.Fatalf("TERM-ignoring stage did not reach timeout KILL: %+v, log %q",
				seen.Exit, guestLog(t, r))
		}
		record, err := os.ReadFile(filepath.Join(RunDir(r.ID), "command.exit"))
		if err != nil || !bytes.Contains(record, []byte("status=KILL\n")) {
			t.Fatalf("stop exit did not record KILL: %q, %v", record, err)
		}
		guestEmpty(t, group)
	})
	t.Run("KillFrozenWritesExit", func(t *testing.T) {
		r := guestRun(t, []string{"sh", "-c", `echo ready; exec sleep 60`}, nil)
		guestOK(t, Launch(r, task.StageCommand, time.Minute), ExecTimeout)
		guestWait(t, ExecTimeout, func() bool { return strings.Contains(guestLog(t, r), "ready\n") })
		group := guestStageGroup(t, r)
		guestFreeze(t)
		if !guestLook(t, r).Frozen() {
			t.Fatal("kill case did not freeze the stage")
		}
		begin := time.Now()
		guestOK(t, Kill(r.ID, task.StageCommand), ExecTimeout)
		seen := guestExit(t, r)
		guestRecord(t, "runner kill frozen", r, time.Since(begin))
		if seen.Exit.Result != "signal" || seen.Exit.Code != "killed" || seen.Exit.Status != -1 {
			t.Fatalf("frozen kill exit = %+v", seen.Exit)
		}
		record, err := os.ReadFile(filepath.Join(RunDir(r.ID), "command.exit"))
		if err != nil || !bytes.Contains(record, []byte("status=KILL\n")) {
			t.Fatalf("frozen kill did not record KILL: %q, %v", record, err)
		}
		guestEmpty(t, group)
	})
}

func guestExec(t *testing.T, argv []string, limit time.Duration) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s exceeded %s: %v; %s", argv[0], limit, ctx.Err(), stderr.String())
	}
	if stderr.Len() > 0 {
		t.Logf("%s stderr: %s", argv[0], strings.TrimSpace(stderr.String()))
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), 0
}

func guestOK(t *testing.T, argv []string, limit time.Duration) string {
	t.Helper()
	out, code := guestExec(t, argv, limit)
	if code != 0 {
		t.Fatalf("%s exited %d: %s", argv[0], code, out)
	}
	return out
}

func guestRun(t *testing.T, command []string, input []byte) task.Run {
	t.Helper()
	var token [4]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	r := task.Run{ID: task.RunID{Task: task.ID("t-" + hex.EncodeToString(token[:])), N: 1},
		Spec: task.Spec{Command: command}}
	if input != nil {
		r.Spec.Inputs = []task.File{{Name: "data", Size: int64(len(input)), Hash: sha256.Sum256(input)}}
	}
	for _, file := range Uploads(r, func(task.Hash) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(input)), nil
	}) {
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read upload: %v, close: %v", err, closeErr)
		}
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.Path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		guestOK(t, Thaw(), ExecTimeout)
		if seen := guestLook(t, r); seen.Verdict() == Running {
			guestOK(t, Kill(r.ID, task.StageCommand), ExecTimeout)
			guestExit(t, r)
		}
		if err := os.RemoveAll(filepath.Join(Root, string(r.ID.Task))); err != nil {
			t.Fatal(err)
		}
	})
	guestOK(t, Verify(r.ID), ExecTimeout)
	return r
}

func guestLook(t *testing.T, r task.Run) Seen {
	t.Helper()
	out := guestOK(t, Look(r.ID, task.StageCommand), ExecTimeout)
	seen, err := ParseLook([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	return seen
}

func guestLog(t *testing.T, r task.Run) string {
	t.Helper()
	out := guestOK(t, ReadLog(r.ID, task.StageCommand, 0, MaxLogChunk), ExecTimeout)
	_, data, err := ParseLog([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func guestWait(t *testing.T, limit time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("guest condition did not hold within %s", limit)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func guestExit(t *testing.T, r task.Run) Seen {
	t.Helper()
	var seen Seen
	guestWait(t, ExecTimeout, func() bool {
		seen = guestLook(t, r)
		return seen.Verdict() == Exited
	})
	return seen
}

func guestSlice(t *testing.T) string {
	t.Helper()
	group := strings.TrimSpace(guestOK(t,
		[]string{"systemctl", "show", Slice, "-p", "ControlGroup", "--value"}, ExecTimeout))
	if group == "" {
		return ""
	}
	dir := filepath.Join("/sys/fs/cgroup", group)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ""
	} else if err != nil {
		t.Fatal(err)
	}
	return dir
}

func guestStageGroup(t *testing.T, r task.Run) string {
	t.Helper()
	group := strings.TrimSpace(guestOK(t, []string{"systemctl", "show",
		Unit(r.ID, task.StageCommand), "-p", "ControlGroup", "--value"}, ExecTimeout))
	if group == "" {
		t.Fatal("live stage has no cgroup")
	}
	return filepath.Join("/sys/fs/cgroup", group)
}

func guestFreeze(t *testing.T) {
	t.Helper()
	out := guestOK(t, Freeze(), ExecTimeout)
	if answer, err := ParseFreeze([]byte(out)); err != nil || answer != FreezeFrozen {
		t.Fatalf("freeze did not confirm a held cgroup: %q, %v", out, err)
	}
}

func guestEmpty(t *testing.T, group string) {
	t.Helper()
	guestWait(t, ExecTimeout, func() bool {
		events, err := os.ReadFile(filepath.Join(group, "cgroup.events"))
		if os.IsNotExist(err) {
			return true
		}
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Contains(events, []byte("populated 0\n"))
	})
	t.Logf("stage cgroup empty or removed: %s", group)
}

func guestRecord(t *testing.T, reason string, r task.Run, elapsed time.Duration) {
	t.Helper()
	record, err := os.ReadFile(filepath.Join(RunDir(r.ID), "command.exit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s exit record, stop-to-record=%s\n%s", reason, elapsed, record)
}
