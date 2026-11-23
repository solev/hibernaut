package task

import (
	"testing"
	"time"
)

func TestWrapUpBudgetsUseRecordedIntent(t *testing.T) {
	r := newRun(false)
	if StopUnproven(r, t0, t0.Add(time.Hour)) {
		t.Fatal("a run without a stop intent became unproven")
	}
	r = build(t, r, me, &StopIntent{Outcome: Cancelled})
	at := r.Log[0].At
	if StopUnproven(r, t0, at.Add(StopLimit-time.Nanosecond)) ||
		!StopUnproven(r, t0, at.Add(StopLimit)) {
		t.Fatal("stop proof did not expire exactly at the recorded intent's limit")
	}
	r = build(t, r, me, &StopResult{How: "unproven"}, &CollectIntent{})
	at = r.Log[len(r.Log)-1].At
	if left := CollectRemaining(r, t0, at.Add(CollectLimit-time.Second)); left != time.Second {
		t.Fatalf("collect has %s remaining, want 1s from the recorded intent", left)
	}
	if left := CollectRemaining(r, t0, at.Add(CollectLimit)); left != 0 {
		t.Fatalf("expired collect has %s remaining", left)
	}
	startedAt := at.Add(8 * time.Hour)
	if StopUnproven(r, startedAt, startedAt.Add(StopLimit-time.Nanosecond)) ||
		!StopUnproven(r, startedAt, startedAt.Add(StopLimit)) {
		t.Fatal("restarted stop proof did not expire exactly at the daemon start's limit")
	}
	if left := CollectRemaining(r, startedAt, startedAt.Add(CollectLimit-time.Second)); left != time.Second {
		t.Fatalf("restarted collect has %s remaining, want 1s from daemon start", left)
	}
	if left := CollectRemaining(r, startedAt, startedAt.Add(CollectLimit)); left != 0 {
		t.Fatalf("expired restarted collect has %s remaining", left)
	}
}
