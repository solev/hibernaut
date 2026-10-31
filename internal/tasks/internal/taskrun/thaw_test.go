package taskrun

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
)

type thawRecordAudit struct {
	engine.Engine
	h      *harness
	id     task.RunID
	seen   atomic.Bool
	before time.Time
}

func (a *thawRecordAudit) Exec(ctx context.Context, mid string, req engine.ExecRequest) (engine.ExecResult, error) {
	if slices.Equal(append([]string{req.Cmd}, req.Args...), taskguest.Thaw()) {
		a.seen.Store(true)
		r := a.h.run(a.id)
		if r.Frozen() || !r.Freeze.At.After(a.before) || r.Freeze.By != "test" {
			a.h.t.Errorf("guest thaw saw no current unfrozen row: %+v", r.Freeze)
			a.h.stopSup()
		}
	}
	return a.Engine.Exec(ctx, mid, req)
}

type freezeAtLaunch struct {
	Engine
	h        *harness
	id       task.RunID
	injected bool
	audit    *thawRecordAudit
}

func (e *freezeAtLaunch) Exec(ctx context.Context, mid string, req engine.ExecRequest) (engine.ExecResult, error) {
	if !e.injected && len(req.Args) > 1 && req.Args[1] == "launch" &&
		slices.Contains(req.Args, "--unit="+taskguest.Unit(e.id, task.StageCommand)) {
		e.injected = true
		e.audit.before = e.h.clock.Now()
		e.h.clock.Advance(time.Millisecond)
		argv := taskguest.Freeze()
		if _, err := e.h.sim.Exec(ctx, mid, engine.ExecRequest{Cmd: argv[0], Args: argv[1:], User: "root"}); err != nil {
			return engine.ExecResult{}, err
		}
	}
	return e.Engine.Exec(ctx, mid, req)
}

func TestThawRecordsBeforeGuestRuns(t *testing.T) {
	for _, site := range []string{"launch", "look", "ready"} {
		t.Run(site, func(t *testing.T) {
			h := newHarness(t, script(time.Hour, 0))
			h.start()
			id := h.submit(2 * time.Hour)
			h.until(30, h.launched(id, task.StageCommand))
			h.stop()
			mid := h.machineID(id)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if site == "ready" {
				if err := h.owner.Pause(ctx, mid); err != nil {
					t.Fatal(err)
				}
				if _, err := h.sim.Resume(ctx, mid, 2*time.Hour); err != nil {
					t.Fatal(err)
				}
			} else if site == "look" {
				argv := taskguest.Freeze()
				if _, err := h.sim.Exec(ctx, mid, engine.ExecRequest{Cmd: argv[0], Args: argv[1:], User: "root"}); err != nil {
					t.Fatal(err)
				}
			} else {
				h.sim.Script = script(0, 0)
				h.request(id, task.RequestCancel)
				h.start()
				h.until(30, h.ended(id))
				h.stop()
				rc2, err := h.store.Submit(ctx, task.Submission{RequestID: "thaw-follow-up",
					Task: id.Task, Spec: h.run(id).Spec, Actor: alice, At: h.clock.Now()}, nil)
				if err != nil {
					t.Fatal(err)
				}
				id = rc2.Run
			}
			before := h.clock.Now()
			h.clock.Advance(time.Millisecond)
			audit := &thawRecordAudit{Engine: h.sim, h: h, id: id, before: before}
			h.count.Engine = audit
			if site == "launch" {
				h.configure = func(cfg *Config) {
					cfg.Engine = &freezeAtLaunch{Engine: cfg.Engine, h: h, id: id, audit: audit}
				}
			}
			h.start()
			h.until(30, func() bool { return audit.seen.Load() })
			h.stop()
			if !audit.seen.Load() {
				t.Fatal("the scenario did not thaw")
			}
		})
	}
}
