package hibd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

const taskHoldMax = 60 * time.Second

func (a *App) holdTask(ctx context.Context, d time.Duration, read func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, d+2*time.Second)
	defer cancel()
	end := a.now().Add(d)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := read(ctx)
		if err != nil || ready {
			return err
		}
		left := end.Sub(a.now())
		if left <= 0 {
			return nil
		}
		if err := a.waitTaskPoll(ctx, min(left, time.Second)); err != nil {
			return err
		}
	}
}

func (a *App) waitTaskPoll(ctx context.Context, d time.Duration) error {
	if a.TaskHoldWait != nil {
		return a.TaskHoldWait(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *App) taskWait(ctx context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	n := fl.Int("run", 0, "run number, latest by default")
	hold := fl.Duration("hold", taskHoldMax, "wait for outcome, at most 60s")
	rest, ok := a.parse(fl, args, 1, c.Path+" "+c.Args)
	if !ok {
		return 2
	}
	id, err := task.ParseID(rest[0])
	if err != nil || *n < 0 || *hold < 0 || *hold > taskHoldMax {
		return a.taskFail(*asJSON, c.Path, "", fmt.Errorf("%w: want a task ID, nonnegative run, and hold from 0 to 60s", errBadTaskRequest))
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	st := a.tasks(cfg)
	defer st.Close()
	var out hibapi.Task
	var selected task.Run
	var daemon task.Instance
	err = a.holdTask(ctx, *hold, func(ctx context.Context) (bool, error) {
		record, err := st.Task(ctx, id)
		if err != nil {
			return false, err
		}
		if *n == 0 && len(record.Runs) > 0 {
			*n = record.Runs[len(record.Runs)-1].ID.N
		}
		for _, run := range record.Runs {
			if run.ID.N != *n {
				continue
			}
			instance, err := st.Instance(ctx)
			if err != nil {
				return false, err
			}
			r := hibapi.RunOf(run, instance)
			selected, daemon = run, instance
			out = hibapi.Task{ID: id, Repo: run.Spec.Repo.String(), Created: record.Created,
				Runs: []hibapi.TaskRun{r}}
			return r.Outcome != "", nil
		}
		return false, fmt.Errorf("%w: no run %d", errBadTaskRequest, *n)
	})
	if err != nil {
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	if _, live := selected.LiveStage(); live && !selected.Ended() && selected.MachineID() != "" {
		e, err := a.engine()
		if err != nil {
			return a.taskFail(*asJSON, c.Path, "", err)
		}
		machineCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		m, err := e.Get(machineCtx, selected.MachineID())
		switch {
		case errors.Is(err, engine.ErrNotFound):
			selected.Machine = task.MachineGone
		case err != nil:
			return a.taskFail(*asJSON, c.Path, "", err)
		case m.State == engine.Paused:
			selected.Machine = task.MachinePaused
		case m.State == engine.Running:
			selected.Machine = task.MachineRunning
		}
		out.Machine = selected.Machine
		out.Runs[0] = hibapi.RunOf(selected, daemon)
	}
	if *asJSON {
		return a.reply(hibapi.Response{TaskReply: hibapi.TaskReply{Task: &out}})
	}
	r := out.Runs[0]
	text := string(r.Outcome)
	if text == "" {
		text = string(r.Phase)
	}
	if _, err := fmt.Fprintln(a.Stdout, text); err != nil {
		return a.taskFail(false, c.Path, "", err)
	}
	return 0
}
