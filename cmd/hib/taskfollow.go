package main

import (
	"context"
	"flag"
)

func taskFollowCommands() map[string]*command {
	return map[string]*command{
		"logs": {
			args: "TASK", opts: "[--run N] [-f]", nargs: 1,
			about: "Prints a run's setup, command and finish output. --run selects a run instead of the latest.\n" +
				"-f follows output until the run ends. This command isn't in this build yet.",
			examples: []string{"hib task logs t-abcdefgh", "hib task logs t-abcdefgh --run 1 -f"},
			flags:    taskLogsFlags,
		},
		"wait": {
			args: "TASK", opts: "[--run N]", nargs: 1,
			about: "Waits for the selected run to end, or the latest run if --run is absent. Prints its outcome.\n" +
				"Exits 0 for succeeded or no_changes, 1 for every other outcome or error, and 2 for a usage\n" +
				"error. --json prints hibd's reply unchanged, including the outcome.\n" +
				"This command isn't in this build yet.",
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
	return o.runLogs
}

func (o taskLogsOptions) runLogs(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

type taskWaitOptions struct{ run int }

func taskWaitFlags(fl *flag.FlagSet) runner {
	var o taskWaitOptions
	fl.IntVar(&o.run, "run", 0, "wait for run `N` instead of the latest")
	return withTaskJSON(fl, o.wait)
}

func (o taskWaitOptions) wait(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}
