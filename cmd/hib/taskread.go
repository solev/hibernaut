package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
)

func taskReadCommands() map[string]*command {
	return map[string]*command{
		"ls": {
			about:    "Lists tasks newest first, with each latest run and why a waiting run waits." + taskDocs(),
			examples: []string{"hib task ls", "hib task ls --json"},
			flags:    taskJSONOnly(taskLS),
		},
		"show": {
			args: "TASK | --request ID", nargs: anyArgs,
			about: "Shows every run, request, hold and removal of TASK. Give --request ID instead of TASK\n" +
				"to look up a submission whose reply was lost." + taskDocs(),
			examples: []string{"hib task show t-abcdefgh", "hib task show --request submit-1 --json"},
			flags:    taskShowFlags,
		},
		"cancel": {
			args: "TASK", opts: "[--request-id ID]", nargs: 1,
			about: "Records a request for hibd to stop the task's live run. The host carries it out even\n" +
				"after this laptop disconnects.",
			examples: []string{"hib task cancel t-abcdefgh", "hib task cancel t-abcdefgh --request-id cancel-1"},
			flags:    taskCancelFlags,
		},
		"rescue": {
			args: "TASK", opts: "[--request-id ID]", nargs: 1,
			about: "Records consent for hibd to resume a machine paused without proof its command froze,\n" +
				"and stop the task's command running inside it.",
			examples: []string{"hib task rescue t-abcdefgh"},
			flags:    taskRescueFlags,
		},
		"rm": {
			args: "TASK", opts: "[--request-id ID] [--yes]", nargs: 1,
			about: "Records a request to delete the task and its machine. This can't be undone, so on a\n" +
				"terminal it asks first. --yes skips the question. Without a terminal, --yes is needed.",
			examples: []string{"hib task rm t-abcdefgh", "hib task rm t-abcdefgh --yes"},
			flags:    taskRMFlags,
		},
	}
}

func taskLS(ctx context.Context, a *app, args []string) int {
	return taskReadCall(ctx, a, taskCallOptions{path: "task ls", timeout: shortTimeout},
		func(r taskCallResult) error {
			if r.reply.Tasks == nil {
				return errors.New("hibd answered without a task list")
			}
			return hibapi.PrintTaskList(a.stdout, r.reply.Tasks, a.now())
		})
}

type taskShowOptions struct{ request string }

func taskShowFlags(fl *flag.FlagSet) runner {
	var o taskShowOptions
	fl.StringVar(&o.request, "request", "", "look up request `ID` instead of TASK")
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		return o.run(ctx, a, args)
	})
}

func (o taskShowOptions) run(ctx context.Context, a *app, args []string) int {
	var hostArgs []string
	switch {
	case len(args) == 1 && o.request == "":
		id, err := task.ParseID(args[0])
		if err != nil {
			return a.mistake(err.Error())
		}
		hostArgs = []string{string(id)}
	case len(args) == 0 && o.request != "":
		hostArgs = []string{"-request", o.request}
	default:
		return a.mistake("give TASK or --request ID")
	}
	return taskReadCall(ctx, a, taskCallOptions{path: "task show", args: hostArgs, timeout: shortTimeout},
		func(r taskCallResult) error {
			if r.reply.Task == nil {
				return errors.New("hibd answered without a task")
			}
			return hibapi.PrintTask(a.stdout, *r.reply.Task)
		})
}

type taskRequestOptions struct {
	requestID string
	yes       bool
}

func taskRequestFlags(fl *flag.FlagSet, o *taskRequestOptions) {
	fl.StringVar(&o.requestID, "request-id", "", "idempotency `ID` (default a generated ID)")
}

func taskCancelFlags(fl *flag.FlagSet) runner {
	var o taskRequestOptions
	taskRequestFlags(fl, &o)
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		return o.cancel(ctx, a, args)
	})
}

func taskRescueFlags(fl *flag.FlagSet) runner {
	var o taskRequestOptions
	taskRequestFlags(fl, &o)
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		return o.rescue(ctx, a, args)
	})
}

func taskRMFlags(fl *flag.FlagSet) runner {
	var o taskRequestOptions
	taskRequestFlags(fl, &o)
	fl.BoolVar(&o.yes, "yes", false, "delete without asking")
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		return o.rm(ctx, a, args)
	})
}

func (o taskRequestOptions) cancel(ctx context.Context, a *app, args []string) int {
	return o.record(ctx, a, "cancel", args)
}

func (o taskRequestOptions) rescue(ctx context.Context, a *app, args []string) int {
	return o.record(ctx, a, "rescue", args)
}

func (o taskRequestOptions) rm(ctx context.Context, a *app, args []string) int {
	return o.record(ctx, a, "rm", args)
}

func (o taskRequestOptions) record(ctx context.Context, a *app, kind string, args []string) int {
	id, err := task.ParseID(args[0])
	if err != nil {
		return a.mistake(err.Error())
	}
	if kind == "rm" && !o.yes {
		if !a.interactive {
			return a.mistake(fmt.Sprintf("deleting %s can't be undone, so pass --yes when there's no terminal to ask on", id))
		}
		if !a.confirm(ctx, fmt.Sprintf("Delete task %s and its machine for good? [y/N] ", id)) {
			fmt.Fprintf(a.stderr, "kept %s\n", id)
			return 1
		}
	}
	return taskReadCall(ctx, a, taskCallOptions{path: "task " + kind, args: []string{string(id)},
		requestID: o.requestID, timeout: shortTimeout}, func(r taskCallResult) error {
		if r.reply.Request == nil {
			return errors.New("hibd answered without a recorded task request")
		}
		return hibapi.PrintTaskRequest(a.stdout, id, r.requestID, *r.reply.Request)
	})
}

func taskReadCall(ctx context.Context, a *app, opts taskCallOptions, print func(taskCallResult) error) int {
	cfg, err := a.settings()
	if err != nil {
		return a.fail(err)
	}
	r, err := a.taskCall(ctx, cfg, opts)
	if a.json && (err == nil || r.reply.Error != "") {
		if _, writeErr := a.stdout.Write(r.raw); writeErr != nil {
			return a.fail(writeErr)
		}
	}
	if err != nil {
		if a.json && r.reply.Error != "" {
			fmt.Fprintln(a.stderr, termsafe.String(err.Error()))
			return 1
		}
		return a.fail(err)
	}
	if !a.json {
		if err := print(r); err != nil {
			return a.fail(err)
		}
	}
	return 0
}
