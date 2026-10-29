package taskrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

type loseKeptOnFind struct {
	Machines
	h    *harness
	kept string
}

func (m loseKeptOnFind) Find(ctx context.Context, r task.Run) (engine.Machine, bool, error) {
	if err := m.h.sim.Kill(ctx, m.kept); err != nil {
		return engine.Machine{}, false, err
	}
	return m.Machines.Find(ctx, r)
}

func TestFollowUpLosesMissingKeptMachineDespiteDuplicate(t *testing.T) {
	for _, when := range []string{"before run", "during find"} {
		t.Run(when, func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			h.start()
			first := h.submit(time.Minute)
			h.until(30, h.ended(first))
			h.stop()
			r := h.run(first)
			rc, err := h.store.Submit(t.Context(), task.Submission{
				RequestID: "missing-kept", Task: first.Task, Spec: r.Spec, Actor: alice,
				At: h.clock.Now(),
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := h.sim.Create(t.Context(), engine.CreateOptions{Template: "dev",
				Metadata: map[string]string{"hib.name": string(first.Task), task.Label: string(first.Task)}})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.Append(t.Context(), task.Checkpoint{
				Run: rc.Run, Seq: 1, At: h.clock.Now(), By: "d1", Body: &task.MachineIntent{},
			}); err != nil {
				t.Fatal(err)
			}
			var ms Machines = h.m
			if when == "before run" {
				if err := h.sim.Kill(t.Context(), r.MachineID()); err != nil {
					t.Fatal(err)
				}
			} else {
				ms = loseKeptOnFind{Machines: h.m, h: h, kept: r.MachineID()}
			}
			x := executor{id: rc.Run, cfg: Config{
				Store: h.store, Logs: taskstore.MemoryLogs{M: h.store}, Engine: h.owner,
				Machines: ms, Clock: h.clock, Instance: "d1",
			}}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := x.Run(ctx); err != nil {
				t.Fatal(err)
			}
			followUp, err := h.store.Run(ctx, rc.Run)
			if err != nil {
				t.Fatal(err)
			}
			if o, _ := followUp.Outcome(); !followUp.Ended() || o != task.Lost {
				t.Fatalf("follow-up ended=%v outcome=%s machine=%s, want Lost on the kept machine",
					followUp.Ended(), o, followUp.MachineID())
			}
			for _, stage := range task.Stages() {
				if n := h.sim.Starts(duplicate.ID, rc.Run, stage); n != 0 {
					t.Fatalf("follow-up started %s %d times in the unused duplicate", stage, n)
				}
			}
			if when == "during find" {
				if _, err := h.sim.Get(ctx, duplicate.ID); !errors.Is(err, engine.ErrNotFound) {
					t.Fatalf("Find left the unused duplicate behind: %v", err)
				}
			}
		})
	}
}
