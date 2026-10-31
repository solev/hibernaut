package main

import (
	"context"
	"flag"
	"time"
)

func recipeCommands() map[string]*command {
	return map[string]*command{
		"put": {
			args: "NAME -- CMD [ARG...]", nargs: 1, takesCommand: true,
			opts: "[--image I] [--size S] [--timeout D]",
			about: "Saves CMD as NAME, with optional image, size and timeout defaults for hib task start.\n" +
				"Replaces a recipe with the same name. This command isn't in this build yet.",
			examples: []string{"hib recipe put check -- go test ./...",
				"hib recipe put review --image dev --size large --timeout 2h -- codex exec -"},
			flags: recipePutFlags,
		},
		"ls": {
			about:    "Lists saved recipes. This command isn't in this build yet.",
			examples: []string{"hib recipe ls", "hib recipe ls --json"},
			flags:    taskJSONOnly(recipeLS),
		},
		"show": {
			args: "NAME", nargs: 1,
			about:    "Shows a recipe's command, image, size and timeout. This command isn't in this build yet.",
			examples: []string{"hib recipe show check"},
			flags:    taskJSONOnly(recipeShow),
		},
		"rm": {
			args: "NAME", nargs: 1,
			about:    "Removes a saved recipe. This command isn't in this build yet.",
			examples: []string{"hib recipe rm check"},
			flags:    taskJSONOnly(recipeRM),
		},
		"default": {
			args: "[NAME]", nargs: anyArgs,
			about: "Shows the default recipe without NAME, or sets it to NAME. hib task start uses it when\n" +
				"neither -- CMD nor --recipe is given. This command isn't in this build yet.",
			examples: []string{"hib recipe default", "hib recipe default check"},
			flags:    taskJSONOnly(recipeDefault),
		},
	}
}

type recipePutOptions struct {
	image, size string
	timeout     time.Duration
}

func recipePutFlags(fl *flag.FlagSet) runner {
	var o recipePutOptions
	fl.StringVar(&o.image, "image", "", "default machine `I`mage")
	fl.StringVar(&o.size, "size", "", "default machine `S`ize")
	fl.DurationVar(&o.timeout, "timeout", 0, "default run time limit `D`")
	return withTaskJSON(fl, o.put)
}

func (o recipePutOptions) put(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

func recipeLS(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

func recipeShow(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

func recipeRM(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}

func recipeDefault(ctx context.Context, a *app, args []string) int {
	return taskNotInBuild(ctx, a, args)
}
