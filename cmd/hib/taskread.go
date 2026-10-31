package main

import (
	"context"
	"flag"
)

func taskReadCommands() map[string]*command {
	return map[string]*command{
		"ls": {
			about: "Lists tasks newest first, with each latest run and machine state.\n" +
				"This command isn't in this build yet." + taskDocs(),
			examples: []string{"hib task ls", "hib task ls --json"},
			flags:    taskJSONOnly(taskLS),
		},
		"show": {
			args: "TASK | --request ID", nargs: anyArgs,
			about: "Shows every run, request, hold and removal of TASK. Give --request ID instead of TASK\n" +
				"to look up a submission whose reply was lost.\n" +
				"This command isn't in this build yet." + taskDocs(),
			examples: []string{"hib task show t-abcdefgh", "hib task show --request submit-1 --json"},
			flags:    taskShowFlags,
		},
		"cancel": {
			args: "TASK", opts: "[--request-id ID]", nargs: 1,
			about: "Records a request for hibd to stop the task's live run. The host carries it out even\n" +
				"after this laptop disconnects. This command isn't in this build yet.",
			examples: []string{"hib task cancel t-abcdefgh", "hib task cancel t-abcdefgh --request-id cancel-1"},
			flags:    taskCancelFlags,
		},
		"rescue": {
			args: "TASK", opts: "[--request-id ID]", nargs: 1,
			about: "Records consent for hibd to resume a held task machine and kill its live stage.\n" +
				"This command isn't in this build yet.",
			examples: []string{"hib task rescue t-abcdefgh"},
			flags:    taskRescueFlags,
		},
		"rm": {
			args: "TASK", opts: "[--request-id ID] [--yes]", nargs: 1,
			about: "Deletes the task and its machine. This can't be undone, so on a terminal it asks first.\n" +
				"--yes skips the question. Without a terminal, --yes is needed.\n" +
				"This command isn't in this build yet.",
			examples: []string{"hib task rm t-abcdefgh", "hib task rm t-abcdefgh --yes"},
			flags:    taskRMFlags,
		},
	}
}

func taskLS(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

type taskShowOptions struct{ request string }

func taskShowFlags(fl *flag.FlagSet) runner {
	var o taskShowOptions
	fl.StringVar(&o.request, "request", "", "look up request `ID` instead of TASK")
	return withTaskJSON(fl, o.run)
}

func (o taskShowOptions) run(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
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
	return withTaskJSON(fl, o.cancel)
}

func taskRescueFlags(fl *flag.FlagSet) runner {
	var o taskRequestOptions
	taskRequestFlags(fl, &o)
	return withTaskJSON(fl, o.rescue)
}

func taskRMFlags(fl *flag.FlagSet) runner {
	var o taskRequestOptions
	taskRequestFlags(fl, &o)
	fl.BoolVar(&o.yes, "yes", false, "delete without asking")
	return withTaskJSON(fl, o.rm)
}

func (o taskRequestOptions) cancel(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

func (o taskRequestOptions) rescue(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

func (o taskRequestOptions) rm(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}
