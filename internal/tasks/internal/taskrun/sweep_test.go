package taskrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
)

type sweepCall struct {
	name   string
	exists bool
	step   task.Step
}

type sweepTrace struct {
	mu     sync.Mutex
	h      *harness
	id     task.RunID
	calls  []sweepCall
	exits  map[task.Stage]task.ExitRecord
	before func(sweepCall, int)
}

func (s *sweepTrace) call(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	res, err := e.Engine.Exec(ctx, id, req)
	if err == nil && res.ExitCode == 0 && strings.HasPrefix(name, "look-") {
		seen, parseErr := taskguest.ParseLook(res.Stdout)
		if parseErr != nil {
			e.trace.h.t.Fatal(parseErr)
		}
		if seen.Exit != nil {
			e.trace.mu.Lock()
			e.trace.exits[task.Stage(strings.TrimPrefix(name, "look-"))] = *seen.Exit
			e.trace.mu.Unlock()
		}
	}
	return res, err
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
	trace := &sweepTrace{h: h, id: id, exits: map[task.Stage]task.ExitRecord{}}
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
			finishExitLook := -1
			for i, call := range trace.calls {
				if call.name == "look-finish" && call.step == task.StepFinish {
					finishExitLook = i
				}
			}
			if finishExitLook < 0 {
				t.Fatal("clean trace never observed finish's exit")
			}
			exitCopyCall := "25-log-command"
			if scenario == "follow-up" {
				exitCopyCall = "23-log-command"
			}
			exitCopyFound := false
			held, live, exitedBeforeResume := 0, 0, 0
			for i, call := range trace.calls {
				t.Logf("trace %02d %s exists=%v step=%s", i+1, call.name, call.exists, call.step)
				exitClearsHold := fmt.Sprintf("%02d-%s", i+1, call.name) == exitCopyCall
				exitCopyFound = exitCopyFound || exitClearsHold
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
						var access task.Access
						var injectedAt int
						heldPolls := 0
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
							if !injected && n == i+1 {
								injected = true
								if !slices.Equal(s.calls, trace.calls[:i+1]) {
									t.Errorf("trace prefix changed at %d: got %+v, want %+v",
										n, s.calls, trace.calls[:i+1])
								}
								mid := h.machineID(s.id)
								switch event {
								case "host-pause", "host-pause-restart":
									if err := h.owner.Pause(ctx, mid); err != nil &&
										!errors.Is(err, engine.ErrNotRunning) {
										t.Error(err)
									}
									if event == "host-pause-restart" {
										h.stopSup()
									}
								case "engine-pause-wake", "engine-pause-rescue", "engine-pause-rm":
									r := h.run(s.id)
									hostFreeze = r.Frozen()
									if err := h.sim.Pause(ctx, mid); err != nil &&
										!errors.Is(err, engine.ErrNotRunning) {
										t.Error(err)
									}
									r.Machine = task.MachinePaused
									access = task.AccessOf(r, h.clock.Now())
									if !hostFreeze && !call.step.WrapUp() {
										// AccessOf holds only a live stage, as defined by LiveStage.
										switch access {
										case task.AccessHeld:
											held++
										case task.AccessLive:
											live++
										default:
											t.Errorf("unexpected engine-pause access %s", access)
										}
										t.Logf("engine-pause access=%s", access)
									}
								case "cancel":
									h.request(s.id, task.RequestCancel)
								case "deadline":
									deadline, _ := h.run(s.id).Deadline()
									h.clock.Advance(deadline.Sub(h.clock.Now()))
								}
							}
							if got.name == "resume" {
								r := h.run(s.id)
								nowAccess := task.AccessOf(r, h.clock.Now())
								if err := nowAccess.Allows(task.OpWake); err != nil {
									t.Errorf("hibd attempted a resume AccessOf refuses: %v", err)
								}
								if _, live := r.LiveStage(); !live && access == task.AccessHeld &&
									nowAccess == task.AccessLive && !woke && !released {
									// LiveStage ends at StageExit, even if its final log copy was lost.
									exitedBeforeResume++
									if !exitClearsHold {
										t.Errorf("StageExit cleared hold at %02d-%s, want only %s",
											i+1, call.name, exitCopyCall)
									}
									t.Log("held at injection; StageExit cleared LiveStage before automatic resume")
								}
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
								access == task.AccessHeld && !call.step.WrapUp() {
								r := h.run(s.id)
								if _, live := r.LiveStage(); !live && !woke && !released {
									if !exitClearsHold {
										t.Errorf("StageExit cleared hold at %02d-%s, want only %s",
											i+1, call.name, exitCopyCall)
									}
								}
								if !exitClearsHold && !woke && event == "engine-pause-wake" {
									// An external user wake is distinct from hibd's guarded automatic resume.
									if _, err := h.sim.Resume(ctx, h.machineID(s.id), time.Hour); err != nil {
										t.Fatal(err)
									}
									woke = true
								} else if !exitClearsHold && event != "engine-pause-wake" &&
									!released && passes >= injectedAt+2 {
									if _, stopped := r.Stop(); !stopped {
										deadline, _ := r.Deadline()
										h.clock.Advance(deadline.Sub(h.clock.Now()))
									} else if _, live := r.LiveStage(); live {
										if r.Ended() || r.Machine != task.MachinePaused || r.Frozen() {
											t.Error("unfrozen live stage did not stay held at its stop")
										}
										heldPolls++
										if heldPolls >= 3 {
											kind := task.RequestRescue
											if event == "engine-pause-rm" {
												kind = task.RequestRemove
											}
											h.request(s.id, kind)
											released = true
										}
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
						case "deadline":
							want = task.TimedOut
						case "engine-pause-rescue", "engine-pause-rm":
							if access == task.AccessHeld && !exitClearsHold {
								want = task.TimedOut
							}
						}
						// Steps ends the main line at finish's exit, before non-interruptible wrap-up.
						if i == finishExitLook && (event == "cancel" || event == "deadline") {
							want = task.Succeeded
						}
						if strings.HasPrefix(event, "engine-pause") && hostFreeze {
							want = task.Succeeded
							t.Log("earlier host freeze retained; engine pause cannot erase it")
						}
						if call.step.WrapUp() {
							// task.Steps makes wrap-up non-interruptible and collect never wakes.
							want = task.Succeeded
							if call.step == task.StepCollect &&
								(strings.HasPrefix(event, "host-pause") || strings.HasPrefix(event, "engine-pause")) {
								want = task.NoChanges
							}
							t.Logf("wrap-up observation: %s", sweepResult(h, s.id, passes))
						}
						if !r.Ended() || outcome(r) != want {
							t.Errorf("want %s; %s", want, sweepResult(h, s.id, passes))
						}
						for _, c := range r.Log {
							if exit, ok := c.Body.(*task.StageExit); ok {
								guest, found := s.exits[exit.Stage]
								if !found || exit.Exit != guest {
									t.Errorf("stage %s recorded exit %+v, guest exit %+v, observed=%v",
										exit.Stage, exit.Exit, guest, found)
								}
							}
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
						h.checkLog(s.id)
					})
				}
			}
			if !exitCopyFound {
				t.Fatalf("clean trace lost hold-clearing call %s", exitCopyCall)
			}
			t.Logf("skipped %d calls before the machine existed", skipped)
			t.Logf("unfrozen main-line engine pauses: held=%d live=%d", held, live)
			t.Logf("held injections whose StageExit preceded automatic resume: %d", exitedBeforeResume)
		})
	}
	t.Logf("swept %d injected cases", cases)
}

func TestLaunchReadinessDoesNotPollOrLog(t *testing.T) {
	for _, scenario := range []string{"follow-up", "host-pause"} {
		t.Run(scenario, func(t *testing.T) {
			h, trace := newSweep(t, scenario == "follow-up")
			h.stop()
			var logs []string
			configure := h.configure
			h.configure = func(cfg *Config) {
				configure(cfg)
				cfg.Logf = func(format string, args ...any) {
					logs = append(logs, fmt.Sprintf(format, args...))
				}
			}
			nextLaunch := h.clock.Now()
			h.store.FailAppends(func(c task.Checkpoint) error {
				switch c.Body.(type) {
				case *task.StageIntent:
					if !c.At.Equal(nextLaunch) {
						t.Errorf("poll before %s intent: %s, want %s", c.Step(), c.At, nextLaunch)
					}
				case *task.StageExit:
					nextLaunch = c.At
				}
				return nil
			})
			h.sim.Script = func(id task.RunID, stage task.Stage) guestsim.Behavior {
				if now := h.clock.Now(); !now.Equal(nextLaunch) {
					t.Errorf("poll before %s start: %s, want %s", stage, now, nextLaunch)
				}
				return script(3*time.Second, 0)(id, stage)
			}
			paused := false
			trace.before = func(call sweepCall, _ int) {
				last, _ := h.run(trace.id).Last()
				if scenario == "host-pause" && !paused && call.name == "get" &&
					last.Step() == task.StepSetup && last.Kind() == task.Exit {
					paused = true
					if err := h.owner.Pause(context.Background(), h.machineID(trace.id)); err != nil {
						t.Error(err)
					}
				}
			}
			h.start()
			startSweep(t, h, trace)
			for pass := 0; pass < 30 && !h.ended(trace.id)(); pass++ {
				h.clock.Advance(time.Second)
				h.settle()
			}
			h.stop()
			if !h.ended(trace.id)() || outcome(h.run(trace.id)) != task.Succeeded {
				t.Fatal(sweepResult(h, trace.id, 30))
			}
			if len(logs) != 0 {
				t.Errorf("supervisor logged: %s", strings.Join(logs, "\n"))
			}
			if scenario == "host-pause" && !paused {
				t.Error("host pause was never injected")
			}
		})
	}
}

func TestExecReadinessWaitsForRoom(t *testing.T) {
	for _, at := range []string{"upload", "launch-setup"} {
		t.Run(at, func(t *testing.T) {
			h, trace := newSweep(t, false)
			h.stop()
			var logs []string
			configure := h.configure
			h.configure = func(cfg *Config) {
				configure(cfg)
				cfg.Logf = func(format string, args ...any) {
					logs = append(logs, fmt.Sprintf(format, args...))
				}
			}
			injected := false
			trace.before = func(call sweepCall, _ int) {
				if !injected && call.name == at {
					injected = true
					if err := h.owner.Pause(context.Background(), h.machineID(trace.id)); err != nil {
						t.Error(err)
					}
					h.m.Room = 0
				}
			}
			h.start()
			startSweep(t, h, trace)
			for range 3 {
				h.clock.Advance(time.Second)
				h.settle()
			}
			if !injected || h.ended(trace.id)() || h.run(trace.id).Machine != task.MachinePaused {
				t.Fatal("run did not wait for room with its machine paused")
			}
			if n := h.sim.Starts(h.machineID(trace.id), trace.id, task.StageSetup); n != 0 {
				t.Fatalf("setup started while no room was available: %d", n)
			}
			h.m.Room = 4
			for pass := 0; pass < 30 && !h.ended(trace.id)(); pass++ {
				h.clock.Advance(time.Second)
				h.settle()
			}
			h.stop()
			if !h.ended(trace.id)() || outcome(h.run(trace.id)) != task.Succeeded {
				t.Fatal(sweepResult(h, trace.id, 30))
			}
			if len(logs) != 0 {
				t.Errorf("supervisor logged while waiting for room: %s", strings.Join(logs, "\n"))
			}
		})
	}
}
