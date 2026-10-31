package task

import (
	"strings"
	"testing"
)

func TestOutcomeSystemdTimeout(t *testing.T) {
	r := build(t, newRun(false), me, cat(prepared(), launched(StageCommand),
		[]Body{&StageExit{Stage: StageCommand, Exit: ExitRecord{Result: "timeout"}}},
		wrapUp(nil))...)
	if o, stage := r.Outcome(); o != TimedOut || stage != StageCommand {
		t.Fatalf("systemd timeout outcome=%s stage=%s, want timed_out at command", o, stage)
	}
}

func TestStatusUnprovenStop(t *testing.T) {
	r := build(t, newRun(false), me, cat(prepared(), launched(StageCommand),
		[]Body{&StopIntent{Outcome: Cancelled, Detail: "cancel requested"},
			&StopResult{How: "unproven"}, &CollectIntent{}})...)
	for _, ended := range []bool{false, true} {
		if ended {
			r = build(t, r, me, &CollectResult{}, &KeepIntent{}, &KeepResult{})
		}
		st := r.Status(me)
		if !strings.Contains(st.Detail, "the stage may still run when the machine wakes") ||
			!strings.Contains(st.Detail, "cancel requested") {
			t.Fatalf("ended=%v status detail=%q, want cancel and wake warning", ended, st.Detail)
		}
	}
}
