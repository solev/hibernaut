package hibd

import (
	"context"
	"io"
	"runtime"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/failurelog"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/tasks"
	"github.com/solev/hibernaut/internal/termsafe"
)

func (l *lineReader) rest() io.Reader { return l.r }

func (a *App) taskManager(cfg config, e engine.Engine) *machines.Manager {
	return machines.New(machines.Config{
		Engine: e, StateDir: cfg.stateDir, AuthorizedKeys: cfg.authorizedKeys,
		Now: a.now, ReadRoom: a.ReadRoom, Awake: a.awake,
		TaskLimits: machines.TaskLimits{HostCPUs: runtime.NumCPU()},
		Durable: func(ctx context.Context, id string) error {
			return a.durableSnapshot(ctx, cfg, id)
		},
		Fenced: a.snapshotFence(cfg),
	})
}

func (a *App) startTasks(ctx context.Context, cfg config, e engine.Engine, m *machines.Manager,
	ts *tasks.Tasks, logf func(string, ...any)) {
	ticks := a.TaskTicks
	if ticks == nil {
		t := time.NewTicker(tasks.Poll)
		defer t.Stop()
		ticks = t.C
	}
	var d *tasks.Daemon
	defer func() {
		if d != nil {
			d.Close()
		}
	}()
	var failures failurelog.Limiter
	for ctx.Err() == nil {
		if a.awake() == nil {
			var err error
			if d == nil {
				d, err = ts.Daemon(ctx, tasks.DaemonConfig{
					Engine: e, Machines: taskMachines{a, cfg, e, m}, Logf: logf,
					Clock: taskDaemonClock{a.now}, Poll: a.taskPoll,
				})
			}
			if err == nil {
				err = d.Tick(ctx)
			}
			if ctx.Err() == nil {
				var line string
				var log bool
				if err != nil {
					line, log = failures.Failure(a.now(), termsafe.String(err.Error()))
				} else {
					line, log = failures.Success("recovered")
				}
				if log && ctx.Err() == nil {
					logf("tasks: %s", line)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

type taskDaemonClock struct{ now func() time.Time }

func (c taskDaemonClock) Now() time.Time { return c.now() }

func (taskDaemonClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}
