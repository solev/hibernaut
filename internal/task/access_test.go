package task

import (
	"bytes"
	"os"
	"slices"
	"testing"
	"time"
)

func TestAccessOf(t *testing.T) {
	deadline := t0.Add(61 * time.Minute)
	running := cat(prepared(), launched(StageCommand))
	stopping := cat(running, []Body{&StopIntent{Outcome: TimedOut}})
	ended := cat(running, []Body{exited(StageCommand, true)}, wrapUp(nil))
	cancel := []Request{{ID: 1, Kind: RequestCancel, At: t0}}
	rescue := []Request{{ID: 2, Kind: RequestRescue, At: t0}}
	rm := []Request{{ID: 3, Kind: RequestRemove, At: t0}}
	tests := []struct {
		name     string
		followUp bool
		log      []Body
		machine  MachineState
		frozen   bool
		reqs     []Request
		now      time.Time
		want     Access
	}{
		{"a follow-up waits for its grant", true, nil, MachinePaused, false, nil, t0,
			AccessKept},
		{"a follow-up found no room", true, []Body{&MachineIntent{},
			&MachineResult{Refused: "full"}}, MachineRunning, false, nil, t0, AccessKept},
		{"a follow-up cancelled before its grant", true, []Body{&StopIntent{Outcome: Cancelled}},
			MachineRunning, false, cancel, t0, AccessKept},
		{"granted", false, []Body{&MachineIntent{}}, MachineRunning, false, nil, t0, AccessLive},
		{"stage runs", false, running, MachineRunning, false, nil, t0, AccessLive},
		{"stage runs past the deadline", false, running, MachineRunning, false, nil, deadline,
			AccessLive},
		{"no live stage", false, prepared(), MachinePaused, false, nil, deadline, AccessLive},
		{"frozen, time left", false, running, MachinePaused, true, nil, t0, AccessLive},
		{"frozen, cancel waiting", false, running, MachinePaused, true, cancel, t0, AccessLive},
		{"frozen, past the deadline", false, running, MachinePaused, true, nil, deadline,
			AccessOverdue},
		{"frozen, cancel waiting past the deadline", false, running, MachinePaused, true, cancel,
			deadline, AccessOverdue},
		{"frozen, stopping", false, stopping, MachinePaused, true, nil, deadline, AccessLive},
		{"unfrozen", false, running, MachinePaused, false, nil, t0, AccessHeld},
		{"unfrozen, past the deadline", false, running, MachinePaused, false, nil, deadline,
			AccessHeld},
		{"unfrozen, stopping", false, stopping, MachinePaused, false, nil, deadline, AccessHeld},
		{"unfrozen, rescued", false, stopping, MachinePaused, false, rescue, deadline,
			AccessLive},
		{"unfrozen, removing", false, stopping, MachinePaused, false, rm, deadline, AccessHeld},
		{"ended", false, ended, MachinePaused, false, nil, deadline, AccessKept},
		{"ended, running for a person", false, ended, MachineRunning, false, nil, deadline,
			AccessKept},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRun(false)
			if tt.followUp {
				r.ID.N, r.Kept = 2, "sbx1"
			}
			r.Freeze.Frozen, r.Requests, r.Machine = tt.frozen, tt.reqs, tt.machine
			r = build(t, r, me, tt.log...)
			if got := AccessOf(r, tt.now); got != tt.want {
				t.Fatalf("AccessOf = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestAllows checks every refusal is a sentinel callers can match, and that no access allows
// remove or copy. hib rm, hib fork and hib image save refuse a task machine by its label alone,
// which is right only while that holds.
func TestAllows(t *testing.T) {
	known := []error{ErrRunLive, ErrTaskMachine, ErrNoCopy, ErrHeld, ErrPastDeadline}
	for _, a := range Accesses() {
		for _, op := range Ops() {
			if err := a.Allows(op); err != nil && !slices.Contains(known, err) {
				t.Errorf("%s refuses %s with %v, which is no sentinel", a, op, err)
			}
		}
		if a.Allows(OpRemove) != ErrTaskMachine || a.Allows(OpCopy) != ErrNoCopy {
			t.Errorf("%s allows remove or copy", a)
		}
	}
	if AccessKept.Allows("fly") == nil || Access("asleep").Allows(OpWake) == nil ||
		noMachine.Allows(OpWake) == nil {
		t.Error("Allows passed an access or op it doesn't know")
	}
}

// moment is a run as AccessOf sees it at a time.
type moment struct {
	r  Run
	at time.Time
}

// TestAccessTransitions proves every edge of the diagram with a run before and after it, and
// that every fixture is an edge the diagram draws.
func TestAccessTransitions(t *testing.T) {
	deadline := t0.Add(61 * time.Minute)
	running := cat(prepared(), launched(StageCommand))
	wrapping := cat(running, []Body{exited(StageCommand, true), &CollectIntent{},
		&CollectResult{}, &KeepIntent{}})
	ended := cat(wrapping, []Body{&KeepResult{}})
	rescue := []Request{{ID: 1, Kind: RequestRescue, At: t0}}
	rm := []Request{{ID: 2, Kind: RequestRemove, At: t0}}
	run := func(m MachineState, frozen bool, reqs []Request, log ...Body) Run {
		r := newRun(false)
		r.Machine, r.Freeze.Frozen, r.Requests = m, frozen, reqs
		return build(t, r, me, log...)
	}
	followUp := func(log ...Body) Run {
		r := newRun(false)
		r.ID.N, r.Kept, r.Machine = 2, "sbx1", MachinePaused
		return build(t, r, me, log...)
	}
	endAt := run(MachinePaused, false, nil, ended...).Log[len(ended)-1].At
	tests := []struct {
		name          string
		from, to      Access
		before, after moment
	}{
		{"first grant", noMachine, AccessLive,
			moment{run(MachineUnknown, false, nil), t0},
			moment{run(MachineRunning, false, nil, &MachineIntent{},
				&MachineResult{ID: "sbx1"}), t0}},
		{"E2B pauses a stage", AccessLive, AccessHeld,
			moment{run(MachineRunning, false, nil, running...), t0},
			moment{run(MachinePaused, false, nil, running...), t0}},
		{"E2B pauses a stopping stage", AccessLive, AccessHeld,
			moment{run(MachineRunning, false, nil, cat(running,
				[]Body{&StopIntent{Outcome: Cancelled}})...), t0},
			moment{run(MachinePaused, false, nil, cat(running,
				[]Body{&StopIntent{Outcome: Cancelled}})...), t0}},
		{"the deadline passes while frozen", AccessLive, AccessOverdue,
			moment{run(MachinePaused, true, nil, running...), deadline.Add(-time.Second)},
			moment{run(MachinePaused, true, nil, running...), deadline}},
		{"the run ends", AccessLive, AccessKept,
			moment{run(MachineRunning, false, nil, wrapping...), t0},
			moment{run(MachineRunning, false, nil, ended...), t0}},
		{"a follow-up finds no room", AccessLive, AccessKept,
			moment{followUp(&MachineIntent{}), t0},
			moment{followUp(&MachineIntent{}, &MachineResult{Refused: "full"}), t0}},
		{"rescue", AccessHeld, AccessLive,
			moment{run(MachinePaused, false, nil, running...), t0},
			moment{run(MachinePaused, false, rescue, cat(running,
				[]Body{&StopIntent{Outcome: Cancelled, Request: 1}})...), t0}},
		{"rm ends a held stage", AccessHeld, AccessLive,
			moment{run(MachinePaused, false, rm, cat(running,
				[]Body{&StopIntent{Outcome: Cancelled, Request: 2}})...), t0},
			moment{run(MachinePaused, false, rm, cat(running,
				[]Body{&StopIntent{Outcome: Cancelled, Request: 2},
					&StopResult{How: "removed-paused"}})...), t0}},
		{"the daemon records the stop", AccessOverdue, AccessLive,
			moment{run(MachinePaused, true, nil, running...), deadline},
			moment{run(MachinePaused, true, nil, cat(running,
				[]Body{&StopIntent{Outcome: TimedOut}})...), deadline}},
		{"a follow-up's grant", AccessKept, AccessLive,
			moment{followUp(), t0},
			moment{followUp(&MachineIntent{}), t0}},
		{"hib task rm", AccessKept, removed,
			moment{run(MachinePaused, false, nil, ended...), endAt},
			moment{run(MachinePaused, false, rm, ended...), endAt}},
		{"7 days on", AccessKept, removed,
			moment{run(MachinePaused, false, nil, ended...), endAt},
			moment{run(MachinePaused, false, nil, ended...), endAt.Add(KeepFor)}},
	}
	proved := map[transition]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := slices.IndexFunc(transitions, func(e transition) bool {
				return e.from == tt.from && e.to == tt.to
			})
			if e < 0 {
				t.Fatalf("the diagram has no edge from %s to %s", tt.from, tt.to)
			}
			proved[transitions[e]] = true
			if tt.from != noMachine {
				if got := AccessOf(tt.before.r, tt.before.at); got != tt.from {
					t.Fatalf("before: AccessOf = %s, want %s", got, tt.from)
				}
			}
			if tt.to == removed {
				tk := Task{ID: tt.after.r.ID.Task, Runs: []Run{tt.after.r},
					Requests: tt.after.r.Requests}
				if _, ok := Retire(tk, tt.after.at); !ok {
					t.Fatal("after: Retire removes nothing")
				}
				return
			}
			if got := AccessOf(tt.after.r, tt.after.at); got != tt.to {
				t.Fatalf("after: AccessOf = %s, want %s", got, tt.to)
			}
		})
	}
	for _, e := range transitions {
		if !proved[e] {
			t.Errorf("no fixture proves %s to %s", e.from, e.to)
		}
	}
	for _, a := range Accesses() {
		into := slices.ContainsFunc(transitions, func(e transition) bool { return e.to == a })
		out := slices.ContainsFunc(transitions, func(e transition) bool { return e.from == a })
		if !into || !out {
			t.Errorf("%s needs an edge in and an edge out", a)
		}
	}
}

func TestAccessGolden(t *testing.T) {
	sketch, err := os.ReadFile("../../SKETCH.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []struct{ file, got, embed string }{
		{"testdata/access.mmd", AccessMermaid(), "```mermaid\n" + AccessMermaid() + "```"},
		{"testdata/access.md", AccessTable(), AccessTable()},
	} {
		want, err := os.ReadFile(g.file)
		if err != nil {
			t.Fatal(err)
		}
		if g.got != string(want) {
			t.Errorf("%s is stale. Regenerate it:\n%s", g.file, g.got)
		}
		if !bytes.Contains(sketch, []byte(g.embed)) {
			t.Errorf("SKETCH.md doesn't embed the generated %s", g.file)
		}
	}
}
