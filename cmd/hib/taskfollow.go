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
				"-f follows output until the run ends, with at most about one login a second.\n" +
				"Terminal output escapes controls. Pipes and files receive the original bytes.\n" +
				"--json prints each range reply unchanged on its own line.\n" + taskFollowRecoveryHelp,
			examples: []string{"hib task logs t-abcdefgh", "hib task logs t-abcdefgh --run 1 -f"},
			flags:    taskLogsFlags,
		},
		"wait": {
			args: "TASK", opts: "[--run N]", nargs: 1,
			about: "Waits for the selected run to end, or the latest run if --run is absent. Prints its outcome.\n" +
				"Exits 0 for succeeded or no_changes, 1 for every other outcome or error, and 2 for a usage\n" +
				"error. --json prints only the final reply unchanged, including the outcome.\n" +
				"Text wait prints the run's detail on stderr when present, including an unproven-stop warning.\n" +
				"A held run's reason is printed once on stderr.\n" + taskFollowRecoveryHelp,
			examples: []string{"hib task wait t-abcdefgh", "hib task wait t-abcdefgh --run 1 --json"},
			flags:    taskWaitFlags,
		},
	}
}

const taskFollowRecoveryHelp = "Follows and waits retry dropped connections, interrupted holds during host restarts,\n" +
	"and retryable host replies such as a busy database. They keep the selected run and\n" +
	"last received cursor. Each outage prints these lines on stderr, the second on the next\n" +
	"successful reply, which can take up to 92 seconds plus SSH login time after the host\n" +
	"returns, if that call succeeds:\n" +
	"  hib: host temporarily unavailable; reconnecting from the last received reply\n" +
	"  hib: host available; resumed from the last received reply\n" +
	"An open follow or wait prevents idle auto-off because each login counts as activity.\n" +
	"A login after the nightly stop starts the VM again."

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
		if o.follow && len(l.Data) > 0 {
			if err := a.waitTaskFollow(ctx, time.Second); err != nil {
				return a.fail(err)
			}
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
			if !a.json && r.Detail != "" {
				if _, err := fmt.Fprintf(a.stderr, "hib: %s\n", termsafe.Line(r.Detail)); err != nil {
					return a.fail(err)
				}
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
		if checkErr := result.reply.TaskReply.Check(); checkErr != nil {
			return result, fmt.Errorf("unusable reply from hibd: %w", checkErr)
		}
		var transport transportFailure
		var remote *hibapi.RemoteError
		retryable := errors.As(err, &transport) || errors.As(err, &remote) && result.reply.Code.Retryable()
		if err == nil && f.reconnecting {
			fmt.Fprintln(a.stderr, "hib: host available; resumed from the last received reply")
			f.reconnecting = false
		}
		if err == nil || !follow || !retryable || ctx.Err() != nil {
			return result, err
		}
		if !f.reconnecting {
			fmt.Fprintln(a.stderr, "hib: host temporarily unavailable; reconnecting from the last received reply")
			f.reconnecting = true
		}
		if err := a.waitTaskFollow(ctx, backoff); err != nil {
			return result, err
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (a *app) waitTaskFollow(ctx context.Context, d time.Duration) error {
	if a.taskFollowSleep != nil {
		return a.taskFollowSleep(ctx, d)
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
