package task

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWaitReason(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	queued := Run{ID: RunID{Task: "t-aaaaaaaa", N: 1}, Submitted: now, Spec: Spec{Timeout: time.Hour}}
	granted := queued
	granted.Log = []Checkpoint{{At: now, Body: &MachineIntent{}}, {Body: &MachineResult{ID: "machine"}}}
	paused := granted
	paused.Machine = MachinePaused
	live := paused
	live.Log = append(slices.Clone(paused.Log), Checkpoint{Body: &StageIntent{Stage: StageCommand}})
	for _, tc := range []struct {
		name string
		run  Run
		now  time.Time
		want string
	}{
		{"queued", queued, now, "waiting for room or its turn; 0 runs ahead; expires at 2026-11-03T12:00:00Z"},
		{"expired", queued, now.Add(MaxQueued), "queue limit passed at 2026-11-03T12:00:00Z; waiting for hibd to record the stop"},
		{"room refusal", Run{ID: queued.ID, Submitted: now, Log: []Checkpoint{
			{Body: &MachineIntent{}}, {Body: &MachineResult{Refused: "no room"}}}}, now,
			"waiting for room or its turn; 0 runs ahead; expires at 2026-11-03T12:00:00Z"},
		{"running", granted, now, ""},
		{"paused no stage", paused, now, "machine paused without recorded freeze proof; no live stage is recorded"},
		{"paused frozen no stage", Run{ID: paused.ID, Spec: paused.Spec, Log: paused.Log,
			Machine: MachinePaused, Freeze: Freeze{Frozen: true}}, now,
			"machine paused with recorded freeze proof; no live stage is recorded"},
		{"held live stage", live, now, ErrHeld.Error()},
		{"paused frozen live stage", Run{ID: live.ID, Spec: live.Spec, Log: live.Log,
			Machine: MachinePaused, Freeze: Freeze{Frozen: true}}, now,
			"machine paused with recorded freeze proof; waiting for hibd to resume or stop the run"},
		{"stopping", Run{Log: []Checkpoint{{Body: &StopIntent{Outcome: Cancelled}}}}, now, ""},
		{"stop requested", Run{Submitted: now, Requests: []Request{{Kind: RequestCancel, At: now}}}, now, ""},
		{"ended", Run{Log: []Checkpoint{{Body: &KeepResult{}}}}, now, ""},
		{"follow-up queued on kept pause", Run{ID: RunID{Task: queued.ID.Task, N: 2},
			Submitted: now, Kept: "kept", Machine: MachinePaused}, now,
			"waiting for room or its turn; 0 runs ahead; expires at 2026-11-03T12:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := QueueOf([]Run{tc.run}, tc.now)
			if got := WaitReason(tc.run, q, tc.now); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
	if got := WaitReason(queued, Queue{}, now); !strings.Contains(got, "queue position unavailable") {
		t.Fatalf("missing snapshot invented a position: %q", got)
	}
}

func TestWaitReasonAgreesWithPick(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	run := func(id ID, n int, submitted time.Time) Run {
		return Run{ID: RunID{Task: id, N: n}, Submitted: submitted}
	}
	a, b, c := run("t-aaaaaaaa", 3, now), run("t-bbbbbbbb", 1, now), run("t-cccccccc", 1, now.Add(time.Minute))
	ended := run("t-dddddddd", 1, now.Add(-time.Hour))
	ended.Log = []Checkpoint{{Body: &KeepResult{}}}
	stopping := run("t-eeeeeeee", 1, now.Add(-time.Hour))
	stopping.Requests = []Request{{Kind: RequestCancel, At: now}}
	stopped := stopping
	stopped.Log = []Checkpoint{{Body: &StopIntent{Outcome: Cancelled}}}
	for _, runs := range [][]Run{{c, a, b}, {a, c, b}, {b, a, c},
		{ended, stopping, stopped, run("t-ffffffff", 1, now.Add(-MaxQueued)), c, a, b}} {
		before := slices.Clone(runs)
		q := QueueOf(runs, now)
		var first RunID
		found := 0
		for _, r := range runs {
			if strings.Contains(WaitReason(r, q, now), "; 0 runs ahead;") {
				first = r.ID
				found++
			}
		}
		picked, ok := Pick(runs, now, func(Run) error { return nil })
		if !ok || found != 1 || first != picked {
			t.Fatalf("WaitReason puts %v first, Pick grants %v; first count %d", first, picked, found)
		}
		if got := WaitReason(c, q, now); !strings.Contains(got, "; 2 runs ahead;") {
			t.Fatalf("queue counted ended, expired or stopping runs: %q", got)
		}
		if !slices.EqualFunc(runs, before, func(a, b Run) bool { return a.ID == b.ID }) {
			t.Fatal("queue reordered its caller's records")
		}
	}
}
