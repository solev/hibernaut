package task

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2025, 10, 28, 9, 0, 0, 0, time.UTC)

const me Instance = "daemon-2"

// build appends bodies to a run one minute apart, failing the test if CheckNext refuses one.
func build(t *testing.T, r Run, by Instance, bodies ...Body) Run {
	t.Helper()
	for _, b := range bodies {
		if err := CheckNext(r.ID, r.Spec, r.Log, b); err != nil {
			t.Fatalf("appending %T: %v", b, err)
		}
		at := r.Submitted.Add(time.Duration(len(r.Log)+1) * time.Minute)
		r.Log = append(r.Log, Checkpoint{Run: r.ID, Seq: len(r.Log) + 1, At: at, By: by, Body: b})
	}
	return r
}

func newRun(repo bool) Run {
	sp := Spec{Command: []string{"codex", "exec"}, Timeout: time.Hour, Size: "dev", Image: "dev"}
	if repo {
		sp.Repo = Repo{"solev", "hibernaut"}
	}
	return Run{ID: RunID{Task: "t-abcdefgh", N: 1}, Spec: sp, Submitted: t0}
}

func launched(s Stage) []Body {
	return []Body{&StageIntent{Stage: s}, &StageLaunched{Stage: s}}
}

func exited(s Stage, ok bool) Body {
	e := ExitRecord{Result: "success", Code: "exited"}
	if !ok {
		e = ExitRecord{Result: "exit-code", Code: "exited", Status: 1}
	}
	return &StageExit{Stage: s, Exit: e}
}

func wrapUp(pub *Publication) []Body {
	return []Body{&CollectIntent{}, &CollectResult{Publication: pub},
		&KeepIntent{}, &KeepResult{}}
}

func prepared() []Body {
	return []Body{&MachineIntent{}, &MachineResult{ID: "sbx1"}, &UploadIntent{}, &UploadResult{}}
}

func cat(parts ...[]Body) []Body {
	var out []Body
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestCheckNext(t *testing.T) {
	happy := cat(prepared(), launched(StageSetup), []Body{exited(StageSetup, true)},
		launched(StageCommand), []Body{exited(StageCommand, true)},
		launched(StageFinish), []Body{exited(StageFinish, true)},
		wrapUp(&Publication{Changes: true}))
	build(t, newRun(true), me, happy...)
	inCommand := cat(prepared(), launched(StageSetup), []Body{exited(StageSetup, true)},
		launched(StageCommand))
	commandDone := cat(inCommand, []Body{exited(StageCommand, true)})

	tests := []struct {
		name string
		log  []Body
		next Body
		ok   bool
	}{
		{"starts with machine", nil, &MachineIntent{}, true},
		{"starts with an expired stop", nil, &StopIntent{Outcome: Expired}, true},
		{"can't start with upload", nil, &UploadIntent{}, false},
		{"result needs its intent", nil, &MachineResult{}, false},
		{"refused machine retries", []Body{&MachineIntent{}, &MachineResult{Refused: "full"}},
			&MachineIntent{}, true},
		{"granted machine doesn't retry", []Body{&MachineIntent{}, &MachineResult{ID: "s"}},
			&MachineIntent{}, false},
		{"open intent takes its result", []Body{&MachineIntent{}}, &MachineResult{ID: "s"}, true},
		{"open intent takes no other step", []Body{&MachineIntent{}}, &UploadIntent{}, false},
		{"stop supersedes an open intent", []Body{&MachineIntent{}},
			&StopIntent{Outcome: Cancelled}, true},
		{"stop needs an early outcome", []Body{&MachineIntent{}},
			&StopIntent{Outcome: Succeeded}, false},
		{"live stage takes its exit", inCommand, exited(StageCommand, true), true},
		{"live stage takes a stop", inCommand, &StopIntent{Outcome: TimedOut}, true},
		{"live stage takes no next stage", inCommand, &StageIntent{Stage: StageFinish}, false},
		{"steps never go back", commandDone, &StageIntent{Stage: StageSetup}, false},
		{"a stage never relaunches", commandDone, &StageIntent{Stage: StageCommand}, false},
		{"a closed stage goes to the next", commandDone, &StageIntent{Stage: StageFinish}, true},
		{"no stop after collect", cat(prepared(), []Body{&StopIntent{Outcome: Cancelled},
			&StopResult{}, &CollectIntent{}}), &StopIntent{Outcome: Cancelled}, false},
		{"no step is passed over", prepared(), &StageIntent{Stage: StageCommand}, false},
		{"the main line doesn't jump to the wrap-up", prepared(), &CollectIntent{}, false},
		{"a failed stage goes to the wrap-up", cat(prepared(), launched(StageSetup),
			[]Body{exited(StageSetup, false)}), &CollectIntent{}, true},
		{"a failed stage runs no later stage", cat(prepared(), launched(StageSetup),
			[]Body{exited(StageSetup, false)}), &StageIntent{Stage: StageCommand}, false},
		{"a refused machine takes no upload", []Body{&MachineIntent{},
			&MachineResult{Refused: "full"}}, &UploadIntent{}, false},
		{"nothing after keep", happy, &StopIntent{Outcome: Cancelled}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := build(t, newRun(true), me, tt.log...)
			err := CheckNext(r.ID, r.Spec, r.Log, tt.next)
			if (err == nil) != tt.ok {
				t.Fatalf("CheckNext = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

// TestCheckNextSkips checks the table passes over exactly the steps a run's spec skips.
func TestCheckNextSkips(t *testing.T) {
	followUp := newRun(true)
	followUp.ID.N = 2
	tests := []struct {
		name string
		r    Run
		log  []Body
		next Body
		ok   bool
	}{
		{"no repo skips setup", newRun(false), prepared(), &StageIntent{Stage: StageCommand}, true},
		{"no repo runs no setup", newRun(false), prepared(), &StageIntent{Stage: StageSetup},
			false},
		{"no repo goes from command to the wrap-up", newRun(false), cat(prepared(),
			launched(StageCommand), []Body{exited(StageCommand, true)}), &CollectIntent{}, true},
		{"no repo runs no finish", newRun(false), cat(prepared(), launched(StageCommand),
			[]Body{exited(StageCommand, true)}), &StageIntent{Stage: StageFinish}, false},
		{"a follow-up skips setup", followUp, prepared(), &StageIntent{Stage: StageCommand}, true},
		{"a follow-up still finishes", followUp, cat(prepared(), launched(StageCommand),
			[]Body{exited(StageCommand, true)}), &CollectIntent{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := build(t, tt.r, me, tt.log...)
			err := CheckNext(r.ID, r.Spec, r.Log, tt.next)
			if (err == nil) != tt.ok {
				t.Fatalf("CheckNext = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name    string
		repo    bool
		log     []Body
		by      Instance
		machine MachineState
		frozen  bool
		reqs    []Request
		want    Status
	}{
		{name: "nothing yet", want: Status{Phase: PhaseQueued}},
		{name: "refused", log: []Body{&MachineIntent{}, &MachineResult{Refused: "no room"}},
			want: Status{Phase: PhaseQueued, Detail: "no room"}},
		{name: "creating", log: []Body{&MachineIntent{}}, want: Status{Phase: PhasePreparing}},
		{name: "intent of a dead daemon", log: []Body{&MachineIntent{}}, by: "daemon-1",
			want: Status{Phase: PhaseRecovering}},
		{name: "command runs", log: cat(prepared(), launched(StageCommand)),
			machine: MachineRunning, want: Status{Phase: PhaseRunning, Stage: StageCommand}},
		{name: "paused frozen", log: cat(prepared(), launched(StageCommand)),
			machine: MachinePaused, frozen: true,
			want: Status{Phase: PhasePaused, Stage: StageCommand}},
		{name: "paused by E2B", log: cat(prepared(), launched(StageCommand)),
			machine: MachinePaused,
			want:    Status{Phase: PhasePaused, Stage: StageCommand, Held: ErrHeld.Error()}},
		{name: "cancel not yet carried out", log: cat(prepared(), launched(StageCommand)),
			reqs: []Request{{ID: 7, Kind: RequestCancel, At: t0}},
			want: Status{Phase: PhaseStopping, Stage: StageCommand}},
		{name: "stopping", log: cat(prepared(), launched(StageCommand),
			[]Body{&StopIntent{Outcome: Cancelled, Detail: "by m"}}),
			want: Status{Phase: PhaseStopping, Stage: StageCommand, Detail: "by m"}},
		{name: "collecting", log: cat(prepared(), launched(StageCommand),
			[]Body{exited(StageCommand, true), &CollectIntent{}}),
			want: Status{Phase: PhaseCollecting}},
		{name: "succeeded without a repo", log: cat(prepared(), launched(StageCommand),
			[]Body{exited(StageCommand, true)}, wrapUp(nil)),
			want: Status{Outcome: Succeeded}},
		{name: "no changes", repo: true, log: cat(prepared(), launched(StageSetup),
			[]Body{exited(StageSetup, true)}, launched(StageCommand),
			[]Body{exited(StageCommand, true)}, launched(StageFinish),
			[]Body{exited(StageFinish, true)}, wrapUp(&Publication{})),
			want: Status{Outcome: NoChanges}},
		{name: "setup failed", repo: true, log: cat(prepared(), launched(StageSetup),
			[]Body{exited(StageSetup, false)}, wrapUp(nil)),
			want: Status{Outcome: Failed, Stage: StageSetup}},
		{name: "timed out", log: cat(prepared(), launched(StageCommand),
			[]Body{&StopIntent{Outcome: TimedOut}, &StopResult{How: "term"}}, wrapUp(nil)),
			want: Status{Outcome: TimedOut, Stage: StageCommand}},
		{name: "expired in the queue", log: cat([]Body{&StopIntent{Outcome: Expired},
			&StopResult{How: "not-started"}}, wrapUp(nil)),
			want: Status{Outcome: Expired}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRun(tt.repo)
			r.Machine, r.Freeze.Frozen, r.Requests = tt.machine, tt.frozen, tt.reqs
			r = build(t, r, cmpOr(tt.by, me), tt.log...)
			if got := r.Status(me); got != tt.want {
				t.Fatalf("Status = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func cmpOr(a, b Instance) Instance {
	if a != "" {
		return a
	}
	return b
}

func TestStopFor(t *testing.T) {
	// build stamps checkpoints a minute apart from t0, so the machine intent is at t0+1m and
	// the one-hour deadline at t0+61m.
	deadline := t0.Add(61 * time.Minute)
	running := cat(prepared(), launched(StageCommand))
	tests := []struct {
		name    string
		log     []Body
		reqs    []Request
		machine MachineState
		now     time.Time
		want    Outcome
		request int64
	}{
		{name: "nothing due", log: running, now: deadline.Add(-time.Second)},
		{name: "deadline", log: running, now: deadline, want: TimedOut},
		{name: "cancel", log: running, now: t0.Add(time.Hour),
			reqs: []Request{{ID: 3, Kind: RequestCancel, At: t0.Add(30 * time.Minute)}},
			want: Cancelled, request: 3},
		{name: "cancel after the deadline is a timeout", log: running, now: deadline.Add(time.Hour),
			reqs: []Request{{ID: 3, Kind: RequestCancel, At: deadline.Add(time.Minute)}},
			want: TimedOut},
		{name: "rescue cancels", log: running, now: t0.Add(10 * time.Minute),
			reqs: []Request{{ID: 4, Kind: RequestRescue, At: t0.Add(9 * time.Minute)}},
			want: Cancelled, request: 4},
		{name: "machine gone", log: running, machine: MachineGone, now: t0, want: Lost},
		{name: "queued too long", now: t0.Add(MaxQueued), want: Expired},
		{name: "refused then queued too long",
			log: []Body{&MachineIntent{}, &MachineResult{Refused: "full"}},
			now: t0.Add(MaxQueued), want: Expired},
		{name: "queue limit stops once granted", log: running, now: t0.Add(MaxQueued),
			want: TimedOut},
		{name: "already stopping", log: cat(running, []Body{&StopIntent{Outcome: Cancelled}}),
			now: deadline.Add(time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRun(false)
			r.Requests, r.Machine = tt.reqs, tt.machine
			r = build(t, r, me, tt.log...)
			got, ok := StopFor(r, tt.now)
			if ok != (tt.want != "") || got.Outcome != tt.want || got.Request != tt.request {
				t.Fatalf("StopFor = %+v %v, want %q request %d", got, ok, tt.want, tt.request)
			}
		})
	}
}

func TestDecidePaused(t *testing.T) {
	running := cat(prepared(), launched(StageCommand))
	stopping := cat(running, []Body{&StopIntent{Outcome: TimedOut}})
	rescue := []Request{{ID: 1, Kind: RequestRescue, At: t0}}
	rm := []Request{{ID: 2, Kind: RequestRemove, At: t0}}
	tests := []struct {
		name   string
		log    []Body
		frozen bool
		reqs   []Request
		act    PausedAction
	}{
		{"frozen", running, true, nil, ResumeAndThaw},
		{"frozen, stopping", stopping, true, nil, ResumeToStop},
		{"unfrozen", running, false, nil, Hold},
		{"unfrozen, stopping", stopping, false, nil, Hold},
		{"unfrozen, rescued", stopping, false, rescue, ResumeToStop},
		{"unfrozen, removing", stopping, false, rm, StopPaused},
		{"frozen, removing", stopping, true, rm, StopPaused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRun(false)
			r.Freeze.Frozen, r.Requests, r.Machine = tt.frozen, tt.reqs, MachinePaused
			r = build(t, r, me, tt.log...)
			if got := DecidePaused(r); got != tt.act {
				t.Errorf("DecidePaused = %s, want %s", got, tt.act)
			}
		})
	}
}

func TestPick(t *testing.T) {
	mk := func(task ID, age time.Duration, size string, log ...Body) Run {
		r := Run{ID: RunID{Task: task, N: 1}, Submitted: t0.Add(-age),
			Spec: Spec{Size: size, Timeout: time.Hour}}
		return build(t, r, me, log...)
	}
	runs := []Run{
		mk("t-cccccccc", time.Hour, "dev"),
		mk("t-aaaaaaaa", 3*time.Hour, "large"),
		mk("t-bbbbbbbb", 2*time.Hour, "dev", &MachineIntent{}),
		mk("t-dddddddd", MaxQueued+time.Hour, "dev"),
	}
	fitsDev := func(r Run) error {
		if r.Spec.Size == "dev" {
			return nil
		}
		return ErrQueueFull
	}
	got, ok := Pick(runs, t0, fitsDev)
	if !ok || got.Task != "t-cccccccc" {
		t.Fatalf("Pick = %v %v, want the oldest queued run that fits", got, ok)
	}
	if _, ok := Pick(runs, t0, func(Run) error { return ErrQueueFull }); ok {
		t.Fatal("Pick granted a run that doesn't fit")
	}
}

func TestRetire(t *testing.T) {
	ended := build(t, newRun(false), me, cat(prepared(), launched(StageCommand),
		[]Body{exited(StageCommand, true)}, wrapUp(nil))...)
	endAt := ended.Log[len(ended.Log)-1].At
	task := Task{ID: "t-abcdefgh", Runs: []Run{ended}}
	if _, ok := Retire(task, endAt.Add(KeepFor-time.Second)); ok {
		t.Fatal("retired before KeepFor")
	}
	if r, ok := Retire(task, endAt.Add(KeepFor)); !ok || r.Why != "retention" {
		t.Fatalf("Retire = %+v %v, want retention", r, ok)
	}
	task.Requests = []Request{{ID: 9, Kind: RequestRemove}}
	if r, ok := Retire(task, endAt); !ok || r.Why != "rm" || r.Request != 9 {
		t.Fatalf("Retire = %+v %v, want rm", r, ok)
	}
	live := build(t, newRun(false), me, &MachineIntent{})
	task.Runs = append(task.Runs, live)
	if _, ok := Retire(task, endAt.Add(2*KeepFor)); ok {
		t.Fatal("retired a task with a live run")
	}
}

func TestBodiesRoundTrip(t *testing.T) {
	for _, s := range Steps() {
		for _, k := range []Kind{Intent, Result, Exit} {
			b, err := NewBody(s, k)
			if err != nil {
				if k == Exit && s.Stage() == "" {
					continue
				}
				t.Fatalf("NewBody(%s, %s): %v", s, k, err)
			}
			if gs, gk := b.checkpoint(); gs != s || gk != k {
				t.Fatalf("NewBody(%s, %s) files itself under %s %s", s, k, gs, gk)
			}
			data, _ := json.Marshal(b)
			if _, err := DecodeBody(s, k, data); err != nil {
				t.Fatalf("DecodeBody(%s, %s): %v", s, k, err)
			}
		}
	}
}

func TestStepOrderIsTheTable(t *testing.T) {
	want := []Step{StepMachine, StepUpload, StepSetup, StepCommand, StepFinish, StepStop,
		StepCollect, StepKeep}
	if got := Steps(); !slicesEqual(got, want) {
		t.Fatalf("Steps = %v, want %v", got, want)
	}
	if got := Stages(); len(got) != 3 || got[0] != StageSetup || got[2] != StageFinish {
		t.Fatalf("Stages = %v", got)
	}
	for _, s := range Stages() {
		if s.Step().Stage() != s {
			t.Fatalf("stage %s isn't run by its step", s)
		}
	}
	for _, s := range Steps() {
		if !slices.Contains(Phases(), s.Phase()) {
			t.Fatalf("step %s has phase %q, which Phases doesn't list", s, s.Phase())
		}
	}
}

func slicesEqual(a, b []Step) bool {
	return strings.Join(stepStrings(a), ",") == strings.Join(stepStrings(b), ",")
}

func stepStrings(s []Step) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = string(x)
	}
	return out
}

func TestMermaidGolden(t *testing.T) {
	got := Mermaid()
	want, err := os.ReadFile("testdata/steps.mmd")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("testdata/steps.mmd is stale. Regenerate it from task.Mermaid():\n%s", got)
	}
	sketch, err := os.ReadFile("../../SKETCH.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sketch, []byte("```mermaid\n"+got+"```")) {
		t.Fatal("SKETCH.md doesn't embed the generated step diagram")
	}
}

func TestIDs(t *testing.T) {
	id, err := NewID(bytes.NewReader([]byte{1, 2, 3, 4, 5}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseID(string(id)); err != nil {
		t.Fatalf("NewID made %q, which ParseID refuses: %v", id, err)
	}
	for _, bad := range []string{"", "t-", "t-ABCDEFGH", "t-abcdefg1", "dev", "t-abcdefghi"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted it", bad)
		}
	}
	run, err := ParseRunID("t-abcdefgh/2")
	if err != nil || !run.FollowUp() || run.String() != "t-abcdefgh/2" {
		t.Fatalf("ParseRunID = %v %v", run, err)
	}
}
