package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
)

func recipeCommands() map[string]*command {
	return map[string]*command{
		"put": {
			args: "NAME -- CMD [ARG...]", nargs: 1, takesCommand: true,
			opts: "[--image IMAGE] [--size SIZE] [--timeout DURATION]",
			about: "Saves CMD as NAME, with optional image, size and timeout defaults for hib task start.\n" +
				"Replaces a recipe with the same name. A command's JSON is at most 16 KiB.",
			examples: []string{"hib recipe put check -- go test ./...",
				"hib recipe put review --image dev --size large --timeout 2h -- codex exec -"},
			flags: recipePutFlags,
		},
		"ls": {
			about:    "Lists recipes saved on the host.",
			examples: []string{"hib recipe ls", "hib recipe ls --json"},
			flags:    taskJSONOnly(recipeLS),
		},
		"show": {
			args: "NAME", nargs: 1,
			about:    "Shows a recipe's command, image, size and timeout.",
			examples: []string{"hib recipe show check"},
			flags:    taskJSONOnly(recipeShow),
		},
		"rm": {
			args: "NAME", nargs: 1,
			about:    "Removes a saved recipe and clears the default if it names this recipe.",
			examples: []string{"hib recipe rm check"},
			flags:    taskJSONOnly(recipeRM),
		},
		"default": {
			args: "[NAME]", nargs: anyArgs,
			about: "Shows the default recipe without NAME, or sets it to NAME. hib task start uses it when\n" +
				"neither -- CMD nor --recipe is given.",
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
	fl.StringVar(&o.image, "image", "", "the default `image` to make the task machine from")
	fl.StringVar(&o.size, "size", "", "the default machine `size` (small, medium or large)")
	fl.DurationVar(&o.timeout, "timeout", 0, "stop the run after this long, such as 1h")
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		return o.put(ctx, a, args)
	})
}

func (o recipePutOptions) put(ctx context.Context, a *app, args []string) int {
	if len(args) < 3 || args[1] != "--" {
		return a.mistake("give NAME and a command after --")
	}
	if o.timeout < 0 || o.timeout%time.Second != 0 {
		return a.mistake("--timeout must be non-negative whole seconds")
	}
	r := hibapi.Recipe{Name: args[0], Command: args[2:], Image: o.image, Size: o.size,
		TimeoutSeconds: int64(o.timeout / time.Second), Publish: task.PublishNone}
	if err := r.Check(); err != nil {
		return a.mistake(err.Error())
	}
	if r.Size != "" {
		if _, err := images.SizeNamed(r.Size); err != nil {
			return a.mistake(err.Error())
		}
	}
	return recipeCall(ctx, a, "put", args[:1], &r)
}

func recipeLS(ctx context.Context, a *app, args []string) int {
	return recipeCall(ctx, a, "ls", args, nil)
}

func recipeShow(ctx context.Context, a *app, args []string) int {
	return recipeCall(ctx, a, "show", args, nil)
}

func recipeRM(ctx context.Context, a *app, args []string) int {
	return recipeCall(ctx, a, "rm", args, nil)
}

func recipeDefault(ctx context.Context, a *app, args []string) int {
	if len(args) > 1 {
		return a.mistake("give at most one default recipe NAME")
	}
	return recipeCall(ctx, a, "default", args, nil)
}

func (a *app) needRecipes(ctx context.Context, cfg *config) error {
	features, err := a.features(ctx, cfg)
	if err != nil {
		return err
	}
	if !slices.Contains(features, hibapi.FeatureRecipes) {
		return errors.New("this host's hibd is too old for recipes. Update the host")
	}
	return nil
}

func recipeCall(ctx context.Context, a *app, leaf string, args []string, recipe *hibapi.Recipe) int {
	if !a.checkNames(args...) {
		return 2
	}
	cfg, err := a.settings()
	if err != nil {
		return a.fail(err)
	}
	if err := a.needRecipes(ctx, cfg); err != nil {
		return a.fail(err)
	}
	var opts callOptions
	if recipe != nil {
		data, err := json.Marshal(recipe)
		if err != nil {
			return a.fail(err)
		}
		opts.input = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(append(data, '\n'))), nil
		}
	}
	var raw bytes.Buffer
	opts.raw = &raw
	r, err := a.remoteSudo(ctx, cfg, shortTimeout, opts,
		append(append([]string{"recipe", leaf}, args...), "-json")...)
	if a.json && (err == nil || r.Error != "") {
		if _, writeErr := a.stdout.Write(raw.Bytes()); writeErr != nil {
			return a.fail(writeErr)
		}
	}
	if err != nil {
		return a.fail(err)
	}
	if a.json {
		return 0
	}
	switch leaf {
	case "put", "show":
		if r.Recipe == nil {
			return a.fail(errors.New("hibd answered without a recipe"))
		}
		err = hibapi.PrintRecipe(a.stdout, *r.Recipe)
	case "ls":
		for _, recipe := range r.Recipes {
			if _, err = fmt.Fprintln(a.stdout, termsafe.Line(recipe.Name)); err != nil {
				break
			}
		}
	case "default":
		name := r.DefaultRecipe
		if name == "" {
			name = "no default recipe"
		}
		_, err = fmt.Fprintln(a.stdout, termsafe.Line(name))
	}
	if err != nil {
		return a.fail(err)
	}
	return 0
}
