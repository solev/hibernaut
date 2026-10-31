package main

import (
	"context"
	"flag"
	"time"
)

type taskStartOptions struct {
	prompt, promptFile, image, size, recipe, requestID string
	files                                              []string
	timeout                                            time.Duration
}

func taskStartCommand() *command {
	return &command{
		args: "[-- CMD ARG...]", takesCommand: true,
		opts: "[--prompt TEXT | --prompt-file F] [--file PATH] [--image I] [--size S] " +
			"[--timeout D] [--recipe NAME] [--request-id ID]",
		about: "Starts CMD in its own machine and saves the task before answering. The host supervises it\n" +
			"after this laptop disconnects. Without CMD, uses --recipe or the default recipe.\n" +
			"--prompt and --prompt-file are alternatives. --file may repeat. Inputs total at most\n" +
			"100 MiB. --timeout is the run's time limit, not the upload timeout.\n" +
			"This command isn't in this build yet.",
		examples: []string{"hib task start -- echo hello",
			"hib task start --prompt Review -- codex exec -",
			"hib task start --prompt-file prompt.txt --file notes.md --recipe review"},
		flags: taskStartFlags,
	}
}

func taskStartFlags(fl *flag.FlagSet) runner {
	var o taskStartOptions
	fl.StringVar(&o.prompt, "prompt", "", "prompt `text` to send on the command's stdin")
	fl.StringVar(&o.promptFile, "prompt-file", "", "read the prompt from `F` instead of --prompt")
	fl.Func("file", "copy an input `path` into the task machine (may repeat)", func(path string) error {
		o.files = append(o.files, path)
		return nil
	})
	fl.StringVar(&o.image, "image", "", "machine `I`mage (default recipe's image or dev)")
	fl.StringVar(&o.size, "size", "", "machine `S`ize (default recipe's size or medium)")
	fl.DurationVar(&o.timeout, "timeout", 0, "run time limit `D` (default recipe's timeout or 24h)")
	fl.StringVar(&o.recipe, "recipe", "", "use the named recipe `NAME`")
	fl.StringVar(&o.requestID, "request-id", "", "idempotency `ID` (default a generated ID)")
	return withTaskJSON(fl, o.run)
}

func (o taskStartOptions) run(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}
