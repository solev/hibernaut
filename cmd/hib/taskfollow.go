package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
)

func taskFollowCommands() map[string]*command {
	return map[string]*command{
		"logs": {
			args: "TASK", opts: "[--run N] [-f]", nargs: 1,
			about: "Prints a run's setup, command and finish output. --run selects a run instead of the latest.\n" +
				"-f follows output until the run ends, reconnecting after a dropped connection.\n" +
				"Terminal output escapes controls. Pipes receive the original bytes. --json prints each\n" +
				"range reply unchanged on its own line.",
			examples: []string{"hib task logs t-abcdefgh", "hib task logs t-abcdefgh --run 1 -f"},
			flags:    taskLogsFlags,
		},
		"wait": {
			args: "TASK", opts: "[--run N]", nargs: 1,
			about: "Waits for the selected run to end, or the latest run if --run is absent. Prints its outcome.\n" +
				"Exits 0 for succeeded or no_changes, 1 for every other outcome or error, and 2 for a usage\n" +
				"error. --json prints hibd's final reply unchanged, including the outcome.\n" +
				"Reconnects after a dropped connection. A held run's reason is printed once on stderr.",
			examples: []string{"hib task wait t-abcdefgh", "hib task wait t-abcdefgh --run 1 --json"},
			flags:    taskWaitFlags,
		},
	}
}

type taskLogsOptions struct {
	run    int
	follow bool
}

func taskLogsFlags(fl *flag.FlagSet) runner {
	var o taskLogsOptions
	fl.IntVar(&o.run, "run", 0, "read run `N` instead of the latest")
	fl.BoolVar(&o.follow, "f", false, "follow output until the run ends")
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		return o.runLogs(ctx, a, args)
	})
}

func (o taskLogsOptions) runLogs(ctx context.Context, a *app, args []string) (code int) {
	id, err := task.ParseID(args[0])
	if err != nil || o.run < 0 {
		return a.mistake("want a task ID and a nonnegative run number")
	}
	cfg, err := a.settings()
	if err != nil {
		return a.fail(err)
	}
	output := a.stdout
	if a.stdoutTerminal && !a.json {
		safe := termsafe.NewWriter(output)
		output = safe
		defer func() {
			if err := safe.Flush(); err != nil {
				code = a.fail(err)
			}
		}()
	}
	f := taskFollow{run: o.run}
	for {
		result, err := f.call(ctx, a, cfg, "logs", id, o.follow)
		if err != nil {
			return taskFollowFailure(a, result, err)
		}
		l := result.reply.TaskLog
		if l == nil || l.From != f.from {
			return a.fail(errors.New("hibd sent no matching task log range"))
		}
		if err := f.selectRun(l.Run); err != nil {
			return a.fail(err)
		}
		data := l.Data
		if a.json {
			data = result.raw
		}
		if _, err := output.Write(data); err != nil {
			return a.fail(err)
		}
		f.from = l.Next
		if l.Done || !o.follow && len(l.Data) == 0 {
			return 0
		}
	}
}

type taskWaitOptions struct{ run int }

func taskWaitFlags(fl *flag.FlagSet) runner {
	var o taskWaitOptions
	fl.IntVar(&o.run, "run", 0, "wait for run `N` instead of the latest")
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		return o.wait(ctx, a, args)
	})
}

func (o taskWaitOptions) wait(ctx context.Context, a *app, args []string) int {
	id, err := task.ParseID(args[0])
	if err != nil || o.run < 0 {
		return a.mistake("want a task ID and a nonnegative run number")
	}
	cfg, err := a.settings()
	if err != nil {
		return a.fail(err)
	}
	f := taskFollow{run: o.run}
	held := false
	for {
		result, err := f.call(ctx, a, cfg, "wait", id, true)
		if err != nil {
			return taskFollowFailure(a, result, err)
		}
		t := result.reply.Task
		if t == nil || t.ID != id || len(t.Runs) != 1 {
			return a.fail(errors.New("hibd sent no matching waited run"))
		}
		r := t.Runs[0]
		if err := f.selectRun(r.N); err != nil {
			return a.fail(err)
		}
		if r.Outcome != "" {
			data := []byte(string(r.Outcome) + "\n")
			if a.json {
				data = result.raw
			}
			if _, err := a.stdout.Write(data); err != nil {
				return a.fail(err)
			}
			if r.Outcome == task.Succeeded || r.Outcome == task.NoChanges {
				return 0
			}
			return 1
		}
		if r.Held != "" && !held {
			fmt.Fprintf(a.stderr, "hib: run %d is held: %s\n", r.N, termsafe.Line(r.Held))
			held = true
		}
	}
}

type taskFollow struct {
	run          int
	from         int64
	reconnecting bool
}

func (f *taskFollow) selectRun(n int) error {
	if n < 1 || f.run != 0 && f.run != n {
		return errors.New("hibd changed the selected task run")
	}
	f.run = n
	return nil
}

func (f *taskFollow) call(ctx context.Context, a *app, cfg *config, path string, id task.ID, follow bool) (taskCallResult, error) {
	backoff := 250 * time.Millisecond
	for {
		args := []string{string(id), "-run", strconv.Itoa(f.run)}
		if path == "logs" {
			args = append(args, "-from", strconv.FormatInt(f.from, 10))
		}
		if follow {
			args = append(args, "-hold", "60s")
		}
		result, err := a.taskCall(ctx, cfg, taskCallOptions{path: "task " + path,
			args: args, timeout: shortTimeout})
		var transport transportFailure
		if err == nil || !follow || !errors.As(err, &transport) || ctx.Err() != nil {
			return result, err
		}
		if !f.reconnecting {
			fmt.Fprintln(a.stderr, "hib: connection dropped; reconnecting from the last received reply")
			f.reconnecting = true
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func taskFollowFailure(a *app, result taskCallResult, err error) int {
	var remote *hibapi.RemoteError
	if a.json && errors.As(err, &remote) {
		if _, writeErr := a.stdout.Write(result.raw); writeErr != nil {
			return a.fail(writeErr)
		}
		return 1
	}
	return a.fail(err)
}
