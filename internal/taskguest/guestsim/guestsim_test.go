package guestsim_test

import (
	"context"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
)

func machine(t *testing.T) (*guestsim.Sim, string, task.Run) {
	t.Helper()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	s := guestsim.New(func() time.Time { return now })
	s.Script = func(task.RunID, task.Stage) guestsim.Behavior {
		return guestsim.Behavior{Runs: time.Hour}
	}
	m, err := s.Create(context.Background(), engine.CreateOptions{Template: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	r := task.Run{ID: task.RunID{Task: "t-abcdefgh", N: 1},
		Spec: task.Spec{Command: []string{"sleep", "3600"}}}
	return s, m.ID, r
}

func execGuest(t *testing.T, s *guestsim.Sim, id string, argv []string) engine.ExecResult {
	t.Helper()
	res, err := s.Exec(context.Background(), id,
		engine.ExecRequest{Cmd: argv[0], Args: argv[1:], User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func look(t *testing.T, s *guestsim.Sim, id string, r task.Run) taskguest.Seen {
	t.Helper()
	res := execGuest(t, s, id, taskguest.Look(r.ID, task.StageCommand))
	seen, err := taskguest.ParseLook(res.Stdout)
	if res.ExitCode != 0 || err != nil {
		t.Fatalf("look: %+v, %v", res, err)
	}
	return seen
}

func TestFreezeKillWritesExitRecord(t *testing.T) {
	s, id, r := machine(t)
	for _, argv := range [][]string{
		taskguest.Launch(r, task.StageCommand, time.Hour), taskguest.Freeze(),
	} {
		if res := execGuest(t, s, id, argv); res.ExitCode != 0 {
			t.Fatalf("exec: %+v", res)
		}
	}
	if seen := look(t, s, id, r); !seen.Frozen() || seen.Verdict() != taskguest.Running {
		t.Fatalf("freeze: %+v", seen)
	}
	if res := execGuest(t, s, id, taskguest.Kill(r.ID, task.StageCommand)); res.ExitCode != 0 {
		t.Fatalf("kill: %+v", res)
	}
	seen := look(t, s, id, r)
	if s.Frozen(id) || seen.Verdict() != taskguest.Exited || seen.Exit.Result != "signal" ||
		seen.Exit.Code != "killed" || seen.Exit.Status != -1 {
		t.Fatalf("kill left frozen=%t, seen=%+v, exit=%+v", s.Frozen(id), seen, seen.Exit)
	}
}

func TestFreezeBeforeFirstStageDoesNotLatch(t *testing.T) {
	s, id, r := machine(t)
	if res := execGuest(t, s, id, taskguest.Freeze()); res.ExitCode != 0 {
		t.Fatalf("freeze: %+v", res)
	}
	if res := execGuest(t, s, id, taskguest.Launch(r, task.StageCommand, time.Hour)); res.ExitCode != 0 {
		t.Fatalf("launch: %+v", res)
	}
	if seen := look(t, s, id, r); seen.Frozen() || seen.Verdict() != taskguest.Running {
		t.Fatalf("freeze without a cgroup latched: %+v", seen)
	}
}

func TestLaunchRefusesFrozenSlice(t *testing.T) {
	s, id, first := machine(t)
	next := first
	next.ID.N++
	for _, argv := range [][]string{
		taskguest.Launch(first, task.StageCommand, time.Hour), taskguest.Freeze(),
	} {
		if res := execGuest(t, s, id, argv); res.ExitCode != 0 {
			t.Fatalf("exec: %+v", res)
		}
	}
	argv := taskguest.Launch(next, task.StageCommand, time.Hour)
	if res := execGuest(t, s, id, argv); res.ExitCode != taskguest.ExitFrozen {
		t.Fatalf("launch did not return ExitFrozen: %+v", res)
	}
	if !s.Frozen(id) || s.Starts(id, next.ID, task.StageCommand) != 0 {
		t.Fatal("launch thawed the slice or started the command")
	}
	if res := execGuest(t, s, id, taskguest.Thaw()); res.ExitCode != 0 {
		t.Fatalf("thaw: %+v", res)
	}
	if res := execGuest(t, s, id, argv); res.ExitCode != 0 {
		t.Fatalf("launch after thaw: %+v", res)
	}
	if seen := look(t, s, id, next); seen.Frozen() || seen.Verdict() != taskguest.Running {
		t.Fatalf("launch after thaw: %+v", seen)
	}
}
