package taskrun

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
)

type sweepCall struct {
	name   string
	exists bool
	step   task.Step
}

type sweepTrace struct {
	h      *harness
	id     task.RunID
	calls  []sweepCall
	before func(sweepCall, int)
}

func (s *sweepTrace) call(name string) {
	r := s.h.run(s.id)
	var step task.Step
	if last, ok := r.Last(); ok {
		step = last.Step()
	}
	_, exists, err := s.h.m.Find(context.Background(), s.id.Task)
	if err != nil {
		s.h.t.Fatal(err)
	}
	c := sweepCall{name: name, exists: exists, step: step}
	s.calls = append(s.calls, c)
	if s.before != nil {
		s.before(c, len(s.calls))
	}
}

type sweepEngine struct {
	Engine
	trace *sweepTrace
}

func (e sweepEngine) Get(ctx context.Context, id string) (engine.Machine, error) {
	e.trace.call("get")
	return e.Engine.Get(ctx, id)
}

func (e sweepEngine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	name := req.Cmd
	switch {
	case req.Cmd == "sh" && len(req.Args) > 2 && req.Args[0] == "-c":
		name = req.Args[3]
	case req.Cmd == "sh" && len(req.Args) > 1:
		name = req.Args[1]
		if len(req.Args) > 3 && (name == "look" || name == "log") {
			name += "-" + req.Args[3]
		}
		if name == "launch" {
			for _, stage := range task.Stages() {
				if slices.Contains(req.Args, "--unit="+taskguest.Unit(e.trace.id, stage)) {
					name += "-" + string(stage)
				}
			}
		}
	}
	e.trace.call(name)
	return e.Engine.Exec(ctx, id, req)
}

func (e sweepEngine) Upload(ctx context.Context, id, user string, files []engine.File) error {
	e.trace.call("upload")
	return e.Engine.Upload(ctx, id, user, files)
}

func (e sweepEngine) Thaw(ctx context.Context, tid task.ID, id string) error {
	e.trace.call("thaw")
	return e.Engine.Thaw(ctx, tid, id)
}

type sweepMachines struct {
	Machines
	trace *sweepTrace
}

func (m sweepMachines) Fits(ctx context.Context) (func(task.Run) error, error) {
	m.trace.call("fits")
	return m.Machines.Fits(ctx)
}

func (m sweepMachines) Find(ctx context.Context, id task.ID) (engine.Machine, bool, error) {
	m.trace.call("find")
	return m.Machines.Find(ctx, id)
}

func (m sweepMachines) Create(ctx context.Context, r task.Run, timeout time.Duration) (engine.Machine, error) {
	m.trace.call("create")
	return m.Machines.Create(ctx, r, timeout)
}

func (m sweepMachines) Resume(ctx context.Context, r task.Run, id string, timeout time.Duration) (engine.Machine, error) {
	m.trace.call("resume")
	return m.Machines.Resume(ctx, r, id, timeout)
}

func (m sweepMachines) Pause(ctx context.Context, id task.ID) error {
	m.trace.call("pause")
	return m.Machines.Pause(ctx, id)
}

func (m sweepMachines) Remove(ctx context.Context, id task.ID) error {
	m.trace.call("remove")
	return m.Machines.Remove(ctx, id)
}

type sweepAudit struct {
	engine.Engine
	h  *harness
	id task.RunID
}

func (a sweepAudit) Exec(ctx context.Context, mid string, req engine.ExecRequest) (engine.ExecResult, error) {
	if slices.Equal(append([]string{req.Cmd}, req.Args...), taskguest.Thaw()) {
		r := a.h.run(a.id)
		_, stopped := r.Stop()
		_, due := task.StopFor(r, a.h.clock.Now())
		if stopped || due {
			a.h.t.Error("slice thaw reached the engine after a recorded or due stop")
		}
	}
	return a.Engine.Exec(ctx, mid, req)
}

func newSweep(t *testing.T, followUp bool) (*harness, *sweepTrace) {
	t.Helper()
	h := newHarness(t, script(3*time.Second, 0))
	h.start()
	id := h.submit(10 * time.Minute)
	if followUp {
		h.until(30, h.ended(id))
		h.stop()
		rc, err := h.store.Submit(context.Background(), task.Submission{
			RequestID: "sweep-follow-up", Task: id.Task, Spec: h.run(id).Spec,
			Actor: alice, At: h.clock.Now()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		id = rc.Run
	}
	h.stop()
	trace := &sweepTrace{h: h, id: id}
	h.count.Engine = sweepAudit{Engine: h.sim, h: h, id: id}
	h.configure = func(cfg *Config) {
		cfg.Engine = sweepEngine{Engine: h.owner, trace: trace}
		cfg.Machines = sweepMachines{Machines: h.m, trace: trace}
	}
	h.start()
	return h, trace
}

func startSweep(t *testing.T, h *harness, s *sweepTrace) {
	t.Helper()
	ctx := context.Background()
	r := h.run(s.id)
	// Grant before starting the workflow so the trace cannot depend on goroutine scheduling.
	if err := h.sup.grant(ctx, []task.Run{r}, s.id); err != nil {
		t.Fatal(err)
	}
	fits, err := h.sup.cfg.Machines.Fits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fits(r); err != nil {
		t.Fatal(err)
	}
	if err := h.sup.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	h.settle()
}

func sweepResult(h *harness, id task.RunID, passes int) string {
	r := h.run(id)
	last, _ := r.Last()
	o, at := r.Outcome()
	var starts []string
	for _, stage := range task.Stages() {
		starts = append(starts, fmt.Sprint(h.sim.Starts(r.MachineID(), id, stage)))
	}
	return fmt.Sprintf("ended=%v outcome=%s at=%s step=%s/%s passes=%d starts=%s",
		r.Ended(), o, at, last.Step(), last.Kind(), passes, strings.Join(starts, ","))
}

func TestExecBoundarySweep(t *testing.T) {
	events := []string{"host-pause", "host-pause-restart", "engine-pause-wake",
		"engine-pause-rescue", "engine-pause-rm", "cancel", "deadline"}
	cases := 0
	for _, scenario := range []string{"fresh", "follow-up"} {
		t.Run(scenario, func(t *testing.T) {
			clean, trace := newSweep(t, scenario == "follow-up")
			startSweep(t, clean, trace)
			for pass := 0; pass < 40 && !clean.ended(trace.id)(); pass++ {
				clean.clock.Advance(time.Second)
				clean.settle()
			}
			clean.stop()
			if !clean.ended(trace.id)() || outcome(clean.run(trace.id)) != task.Succeeded {
				t.Fatal("clean scenario failed: " + sweepResult(clean, trace.id, 40))
			}
			skipped := 0
			for i, call := range trace.calls {
				t.Logf("trace %02d %s exists=%v step=%s", i+1, call.name, call.exists, call.step)
				if !call.exists {
					skipped++
					continue
				}
				for _, event := range events {
					cases++
					t.Run(fmt.Sprintf("%s/%02d-%s", event, i+1, call.name), func(t *testing.T) {
						h, s := newSweep(t, scenario == "follow-up")
						ctx := context.Background()
						injected, restarted, woke, released := false, false, false, false
						hostFreeze := false
						var injectedAt int
						var stopStarts []int
						h.store.FailAppends(func(c task.Checkpoint) error {
							if _, ok := c.Body.(*task.StopIntent); ok {
								for _, stage := range task.Stages() {
									stopStarts = append(stopStarts, h.sim.Starts(h.machineID(s.id), s.id, stage))
								}
							}
							return nil
						})
						s.before = func(got sweepCall, n int) {
							if injected && strings.HasPrefix(event, "engine-pause") &&
								(got.name == "resume" || got.name == "thaw") &&
								!hostFreeze && !woke && !released {
								t.Error("hibd tried to resume an engine-paused machine without user intent")
							}
							if injected || n != i+1 {
								return
							}
							injected = true
							if got.name != call.name {
								t.Errorf("trace changed at %d: got %s, want %s", n, got.name, call.name)
							}
							mid := h.machineID(s.id)
							switch event {
							case "host-pause", "host-pause-restart":
								if err := h.owner.Pause(ctx, mid); err != nil {
									t.Error(err)
								}
								if event == "host-pause-restart" {
									h.stopSup()
								}
							case "engine-pause-wake", "engine-pause-rescue", "engine-pause-rm":
								hostFreeze = h.run(s.id).Frozen()
								if err := h.sim.Pause(ctx, mid); err != nil {
									t.Error(err)
								}
							case "cancel":
								h.request(s.id, task.RequestCancel)
							case "deadline":
								deadline, _ := h.run(s.id).Deadline()
								h.clock.Advance(deadline.Sub(h.clock.Now()))
							}
						}
						startSweep(t, h, s)
						passes := 0
						for ; passes < 40 && !h.ended(s.id)(); passes++ {
							if injected && injectedAt == 0 {
								injectedAt = passes + 1
							}
							if injected && event == "host-pause-restart" && !restarted {
								h.stop()
								h.start()
								if err := h.sup.Tick(ctx); err != nil {
									t.Fatal(err)
								}
								h.settle()
								restarted = true
							}
							if injected && strings.HasPrefix(event, "engine-pause") &&
								!hostFreeze && !call.step.WrapUp() {
								r := h.run(s.id)
								if !woke && event == "engine-pause-wake" {
									// An external user wake is distinct from hibd's guarded automatic resume.
									if _, err := h.sim.Resume(ctx, h.machineID(s.id), time.Hour); err != nil {
										t.Fatal(err)
									}
									woke = true
								} else if event != "engine-pause-wake" && !released && passes >= injectedAt+2 {
									if _, stopped := r.Stop(); !stopped {
										deadline, _ := r.Deadline()
										h.clock.Advance(deadline.Sub(h.clock.Now()))
									} else if _, live := r.LiveStage(); live {
										kind := task.RequestRescue
										if event == "engine-pause-rm" {
											kind = task.RequestRemove
										}
										h.request(s.id, kind)
										released = true
									}
								}
							}
							h.clock.Advance(time.Second)
							h.settle()
						}
						h.stop()
						if !injected {
							t.Fatal("event was never injected")
						}
						r := h.run(s.id)
						want := task.Succeeded
						switch event {
						case "cancel":
							want = task.Cancelled
						case "deadline", "engine-pause-rescue", "engine-pause-rm":
							want = task.TimedOut
						}
						if strings.HasPrefix(event, "engine-pause") && hostFreeze {
							want = task.Succeeded
						}
						if call.step.WrapUp() {
							// task.Steps makes wrap-up non-interruptible and collect never wakes.
							want = outcome(r)
							if want != task.Succeeded && want != task.NoChanges {
								t.Errorf("wrap-up changed outcome to %s", want)
							}
							t.Logf("wrap-up observation: %s", sweepResult(h, s.id, passes))
						}
						if !r.Ended() || outcome(r) != want {
							t.Errorf("want %s; %s", want, sweepResult(h, s.id, passes))
						}
						for j, stage := range task.Stages() {
							starts := h.sim.Starts(r.MachineID(), s.id, stage)
							if starts > 1 || (len(stopStarts) > 0 && starts != stopStarts[j]) {
								t.Errorf("stage %s started after stop or more than once: %d", stage, starts)
							}
							if want == task.Succeeded && !stage.Step().Skipped(s.id, r.Spec) && starts != 1 {
								t.Errorf("stage %s starts=%d, want 1; %s", stage, starts, sweepResult(h, s.id, passes))
							}
						}
						if event == "engine-pause-wake" && !hostFreeze && !call.step.WrapUp() && !woke {
							t.Error("run ended without the external user wake")
						}
						h.checkLog(s.id)
					})
				}
			}
			t.Logf("skipped %d calls before the machine existed", skipped)
		})
	}
	t.Logf("swept %d injected cases", cases)
}
