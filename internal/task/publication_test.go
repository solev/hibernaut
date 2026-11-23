package task

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPublicationOutcome(t *testing.T) {
	tests := []struct {
		name   string
		mode   Publish
		bodies []Body
		want   Status
	}{
		{"refused draft", PublishDraftPR,
			[]Body{exited(StageFinish, true), &PublishResult{Failed: "GitHub: draft refused"}},
			Status{Outcome: Failed, Stage: StageFinish, Detail: "GitHub: draft refused"}},
		{"unread publication", PublishDraftPR,
			[]Body{exited(StageFinish, true), &CollectResult{Unread: []string{"publication"}}},
			Status{Outcome: Failed, Stage: StageFinish,
				Detail: "the host could not read what finish published; the work is in the kept machine"}},
		{"setup failed", PublishNone, []Body{exited(StageSetup, false)},
			Status{Outcome: Failed, Stage: StageSetup,
				Detail: "setup failed; see hib task logs t-abcdefgh"}},
		{"finish failed", PublishDraftPR, []Body{exited(StageFinish, false)},
			Status{Outcome: Failed, Stage: StageFinish,
				Detail: "finish failed; see hib task logs t-abcdefgh"}},
		{"stage timed out", PublishDraftPR,
			[]Body{&StageExit{Stage: StageFinish, Exit: ExitRecord{Result: "timeout"}}},
			Status{Outcome: TimedOut, Stage: StageFinish}},
		{"stop during draft finish", PublishDraftPR,
			[]Body{&StageIntent{Stage: StageFinish},
				&StopIntent{Outcome: Cancelled, Detail: "cancel requested"}},
			Status{Outcome: Cancelled, Stage: StageFinish,
				Detail: "cancel requested; hib/t-abcdefgh may already be on GitHub without a PR"}},
		{"stop during unpublished finish", PublishNone,
			[]Body{&StageIntent{Stage: StageFinish}, &StopIntent{Outcome: Cancelled}},
			Status{Outcome: Cancelled, Stage: StageFinish}},
		{"stop during command", PublishDraftPR,
			[]Body{&StageIntent{Stage: StageCommand}, &StopIntent{Outcome: TimedOut, Detail: "deadline"}},
			Status{Outcome: TimedOut, Stage: StageCommand, Detail: "deadline"}},
		{"stop wins over publish error", PublishDraftPR,
			[]Body{&StageIntent{Stage: StageCommand}, &StopIntent{Outcome: Cancelled},
				&PublishResult{Failed: "GitHub error"}},
			Status{Outcome: Cancelled, Stage: StageCommand}},
		{"first failed stage wins", PublishDraftPR,
			[]Body{exited(StageSetup, false), exited(StageFinish, false),
				&PublishResult{Failed: "GitHub error"}},
			Status{Outcome: Failed, Stage: StageSetup,
				Detail: "setup failed; see hib task logs t-abcdefgh"}},
		{"no changes", PublishDraftPR,
			[]Body{exited(StageFinish, true), &PublishResult{Publication: &Publication{}}},
			Status{Outcome: NoChanges}},
		{"published", PublishDraftPR,
			[]Body{exited(StageFinish, true), &CollectResult{Publication: &Publication{}},
				&PublishResult{Publication: &Publication{Changes: true}}},
			Status{Outcome: Succeeded}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRun(true)
			r.Spec.Publish = tt.mode
			for _, b := range append(tt.bodies, &KeepResult{}) {
				r.Log = append(r.Log, Checkpoint{Body: b})
			}
			if o, s := r.Outcome(); o != tt.want.Outcome || s != tt.want.Stage {
				t.Errorf("Outcome = %s at %s, want %s at %s", o, s, tt.want.Outcome, tt.want.Stage)
			}
			if st := r.Status(me); st != tt.want {
				t.Errorf("Status = %+v, want %+v", st, tt.want)
			}
		})
	}
}

func TestPublishTransitions(t *testing.T) {
	r := newRun(true)
	r.Spec.Publish = PublishDraftPR
	r = build(t, r, me, cat(prepared(), launched(StageSetup), []Body{exited(StageSetup, true)},
		launched(StageCommand), []Body{exited(StageCommand, true)}, launched(StageFinish),
		[]Body{exited(StageFinish, true), &CollectIntent{}, &CollectResult{
			Publication: &Publication{Changes: true}}})...)
	if err := CheckNext(r.ID, r.Spec, r.Log, &KeepIntent{}); err == nil {
		t.Error("keep bypassed draft publication")
	}
	r = build(t, r, me, &PublishIntent{})
	if st := r.Status(me); st.Phase != PhaseCollecting {
		t.Errorf("publish status = %+v, want collecting", st)
	}
	for _, b := range []Body{&KeepIntent{}, &StopIntent{Outcome: Cancelled}} {
		if err := CheckNext(r.ID, r.Spec, r.Log, b); err == nil {
			t.Errorf("%T cut an open publication", b)
		}
	}
	r = build(t, r, me, &PublishResult{Publication: &Publication{Changes: true}},
		&KeepIntent{}, &KeepResult{})
	if o, _ := r.Outcome(); o != Succeeded {
		t.Errorf("published outcome = %s", o)
	}
}

func TestPublishAfterUnsuccessfulWork(t *testing.T) {
	for _, stage := range Stages() {
		for _, stopped := range []bool{false, true} {
			name := string(stage)
			if stopped {
				name += " stopped"
			}
			t.Run(name, func(t *testing.T) {
				r := newRun(true)
				r.Spec.Publish = PublishDraftPR
				r = build(t, r, me, prepared()...)
				for _, s := range Stages() {
					r = build(t, r, me, launched(s)...)
					if s == stage {
						break
					}
					r = build(t, r, me, exited(s, true))
				}
				want := Failed
				if stopped {
					r = build(t, r, me, &StopIntent{Outcome: Cancelled}, &StopResult{})
					want = Cancelled
				} else {
					r = build(t, r, me, exited(stage, false))
				}
				r = build(t, r, me, &CollectIntent{}, &CollectResult{})
				if r.Publishable() {
					t.Error("unsuccessful work allowed a publisher action")
				}
				r = build(t, r, me, &PublishIntent{}, &PublishResult{}, &KeepIntent{}, &KeepResult{})
				if o, s := r.Outcome(); o != want || s != stage {
					t.Errorf("Outcome = %s at %s, want %s at %s", o, s, want, stage)
				}
			})
		}
	}
	r := newRun(true)
	r.Spec.Publish = PublishDraftPR
	r.Log = []Checkpoint{{Body: exited(StageFinish, true)}}
	if !r.Publishable() {
		t.Error("successful finish did not allow publication")
	}
}

func TestM14UpgradeLogs(t *testing.T) {
	for _, mode := range []Publish{PublishNone, ""} {
		t.Run(string(mode), func(t *testing.T) {
			r := newRun(false)
			r.Spec.Publish = mode
			old := cat(prepared(), launched(StageCommand), []Body{exited(StageCommand, true)},
				wrapUp(nil))
			for i, b := range old {
				data, err := json.Marshal(b)
				if err != nil {
					t.Fatal(err)
				}
				step, kind := b.checkpoint()
				decoded, err := DecodeBody(step, kind, data)
				if err != nil {
					t.Fatal(err)
				}
				r = build(t, r, "old-daemon", decoded)
				if r.Publishable() {
					t.Fatal("M14 run allowed publication")
				}
				if step == StepCollect && kind == Result {
					if err := CheckNext(r.ID, r.Spec, r.Log, &PublishIntent{}); err == nil {
						t.Error("unrequested publication was accepted")
					}
				}
				if step == StepCollect || step == StepKeep {
					st := r.Status(me)
					switch {
					case step == StepKeep && kind == Result:
						if st.Outcome != Succeeded {
							t.Errorf("ended M14 run = %+v", st)
						}
					case kind == Intent:
						if st.Phase != PhaseRecovering {
							t.Errorf("old open intent at %d = %+v", i, st)
						}
					default:
						if st.Phase != PhaseCollecting {
							t.Errorf("collect result = %+v", st)
						}
					}
				}
			}
			if o, s := r.Outcome(); o != Succeeded || s != "" {
				t.Errorf("M14 outcome = %s at %s", o, s)
			}
		})
	}
	r := newRun(true)
	r.Log = []Checkpoint{{Body: &KeepResult{}}}
	if o, _ := r.Outcome(); o != NoChanges {
		t.Errorf("keep-only fixture outcome = %s, want no_changes", o)
	}
}

func TestPublicationAtRef(t *testing.T) {
	const start = "1111111111111111111111111111111111111111"
	const commit = "2222222222222222222222222222222222222222"
	tests := []struct {
		name string
		p    Publication
		ref  string
		want Publication
		bad  bool
	}{
		{"unchanged follow-up", Publication{Start: start, Commit: start, Changes: true,
			Pushed: true, PR: "old PR"}, start, Publication{Start: start}, false},
		{"this run pushed", Publication{Start: start, Commit: commit, Changes: true,
			Pushed: true, Author: "bot"}, commit,
			Publication{Start: start, Commit: commit, Changes: true, Pushed: true, Author: "bot"}, false},
		{"missing start", Publication{Commit: commit, Changes: true, Pushed: true},
			commit, Publication{}, true},
		{"ref moved", Publication{Start: start, Commit: commit, Changes: true, Pushed: true},
			strings.Repeat("3", 40), Publication{}, true},
		{"no push", Publication{Start: start, Commit: commit, Changes: true},
			commit, Publication{}, true},
		{"unchanged first run", Publication{Start: start}, "", Publication{Start: start}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := tt.p.AtRef(tt.ref)
			if (err != nil) != tt.bad || p != tt.want {
				t.Fatalf("AtRef = %+v, %v, want %+v, error %v", p, err, tt.want, tt.bad)
			}
			if tt.bad {
				return
			}
			r := newRun(true)
			r.ID.N = 2
			r.Log = []Checkpoint{{Body: exited(StageFinish, true)},
				{Body: &PublishResult{Publication: &p}}, {Body: &KeepResult{}}}
			want := NoChanges
			if tt.want.Changes {
				want = Succeeded
			}
			if o, _ := r.Outcome(); o != want {
				t.Errorf("follow-up outcome = %s, want %s", o, want)
			}
		})
	}
}

func TestPublishAndFinishBudgets(t *testing.T) {
	for _, tt := range []struct {
		repo    bool
		timeout time.Duration
		reserve time.Duration
	}{
		{false, time.Hour, 0}, {true, time.Hour, 2 * time.Minute},
		{true, 4 * time.Minute, time.Minute}, {true, 2 * time.Second, time.Second / 2},
	} {
		r := newRun(tt.repo)
		r.Spec.Timeout = tt.timeout
		r = build(t, r, me, &MachineIntent{})
		now := r.Log[0].At
		if got := FinishReserve(r.Spec); got != tt.reserve {
			t.Errorf("FinishReserve(%+v) = %s, want %s", tt, got, tt.reserve)
		}
		for _, stage := range Stages() {
			want := tt.timeout
			if stage == StageCommand {
				want -= tt.reserve
			}
			want = max(want.Truncate(time.Second), time.Second)
			if got := StageRuntimeMax(r, stage, now); got != want {
				t.Errorf("%s runtime = %s, want %s", stage, got, want)
			}
		}
	}
	r := newRun(true)
	r.Log = []Checkpoint{{At: t0, Body: &PublishIntent{}}}
	for _, start := range []time.Time{t0.Add(-time.Hour), t0.Add(24 * time.Hour)} {
		at := maxTime(t0, start)
		if left := PublishRemaining(r, start, at.Add(5*time.Minute-time.Second)); left != time.Second {
			t.Errorf("publish has %s, want 1s", left)
		}
		if left := PublishRemaining(r, start, at.Add(5*time.Minute)); left != 0 {
			t.Errorf("publish expired with %s left", left)
		}
	}
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
