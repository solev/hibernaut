package taskrun

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
)

type failedKeepFreeze struct {
	engine.Engine
	delivered bool
	failed    bool
}

func (e *failedKeepFreeze) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if !e.failed && slices.Equal(append([]string{req.Cmd}, req.Args...), taskguest.Freeze()) {
		e.failed = true
		if e.delivered {
			if _, err := e.Engine.Exec(ctx, id, req); err != nil {
				return engine.ExecResult{}, err
			}
		}
		return engine.ExecResult{}, engine.ErrConnectionLost
	}
	return e.Engine.Exec(ctx, id, req)
}

func TestKeptUnfrozenFollowUpDoesNotBlockQueue(t *testing.T) {
	for _, mode := range []string{"freeze-failed", "freeze-reply-lost"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, script(3*time.Second, 0))
			freeze := &failedKeepFreeze{Engine: h.sim, delivered: mode == "freeze-reply-lost"}
			h.count.Engine = freeze
			var mu sync.Mutex
			var logs []string
			h.configure = func(cfg *Config) {
				cfg.Logf = func(format string, args ...any) {
					mu.Lock()
					defer mu.Unlock()
					logs = append(logs, fmt.Sprintf(format, args...))
				}
			}
			h.start()
			first := h.submit(10 * time.Minute)
			h.until(30, h.ended(first))
			h.stop()
			r := h.run(first)
			if !freeze.failed || r.Frozen() || r.Machine != task.MachinePaused ||
				h.sim.Frozen(r.MachineID()) != freeze.delivered {
				t.Fatalf("keep did not leave the intended unfrozen row: %+v", r.Freeze)
			}
			if outcome(r) != task.Succeeded {
				t.Fatalf("first run ended %s", outcome(r))
			}
			sp := r.Spec
			sp.Timeout = 24 * time.Hour
			rc, err := h.store.Submit(context.Background(), task.Submission{
				RequestID: "unfrozen-follow-up", Task: first.Task, Spec: sp,
				Actor: alice, At: h.clock.Now()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			followUp := rc.Run
			h.clock.Advance(time.Nanosecond)
			b := h.submit(10 * time.Minute)
			h.start()
			startSweep(t, h, &sweepTrace{h: h, id: followUp})
			grantedAt, passes := 0, 0
			for passes < 40 && (!h.ended(followUp)() || !h.ended(b)()) {
				passes++
				if passes == 5 && !h.ended(followUp)() {
					t.Logf("before deadline: status=%+v access=%s; %s",
						h.run(followUp).Status(h.sup.cfg.Instance),
						task.AccessOf(h.run(followUp), h.clock.Now()),
						sweepResult(h, followUp, passes-1))
					deadline, _ := h.run(followUp).Deadline()
					h.clock.Advance(deadline.Sub(h.clock.Now()))
					h.settle()
				}
				h.tick()
				if _, granted := h.run(b).Deadline(); granted && grantedAt == 0 {
					grantedAt = passes
				}
			}
			h.stop()
			// StepMachine resumes the kept machine; AccessOf permits its daemon wake.
			if !h.ended(followUp)() || outcome(h.run(followUp)) != task.Succeeded {
				t.Errorf("follow-up want succeeded; %s", sweepResult(h, followUp, passes))
			}
			for _, stage := range task.Stages() {
				want := 1
				if stage.Step().Skipped(followUp, sp) {
					want = 0
				}
				if got := h.sim.Starts(r.MachineID(), followUp, stage); got != want {
					t.Errorf("follow-up %s starts=%d, want %d", stage, got, want)
				}
			}
			// Pick grants the next fitting run once the earlier machine intent closes.
			if grantedAt == 0 || grantedAt > 1 {
				t.Errorf("B granted at pass %d, want no later than baseline pass 1", grantedAt)
			}
			if !h.ended(b)() || outcome(h.run(b)) != task.Succeeded {
				t.Errorf("B want succeeded; %s", sweepResult(h, b, passes))
			}
			var wantLogs []string
			if freeze.delivered {
				// The unfrozen row cannot prove a lost freeze did not land; ExitFrozen is the backstop.
				wantLogs = []string{fmt.Sprintf(
					"task %s: launch command of %s: slice thawed; retry launch", followUp, followUp)}
			}
			if !slices.Equal(logs, wantLogs) {
				t.Errorf("supervisor log=%q, want %q", logs, wantLogs)
			}
			t.Logf("B granted at pass %d; follow-up %s; B %s; supervisor log=%q",
				grantedAt, sweepResult(h, followUp, passes), sweepResult(h, b, passes),
				strings.Join(logs, "\n"))
			h.checkLog(followUp)
			h.checkLog(b)
		})
	}
}
