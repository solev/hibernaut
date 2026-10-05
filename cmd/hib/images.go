package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/solev/hibernaut/internal/hibapi"
)

const snapshotHelp = `The origin pauses for a few seconds. SSH sessions into it may drop.
An image carries everything in the machine. hib puts only placeholders and your public key there,
but a secret you typed in yourself goes into the image too.`

func (a *app) imageSave(ctx context.Context, cfg *config, args []string) int {
	if !a.checkNames(args...) {
		return 2
	}
	r, err := a.remoteSudo(ctx, cfg, 0, callOptions{}, "image", "save", args[0], args[1], "-json")
	if err != nil {
		return a.fail(err)
	}
	if r.Image == nil {
		return a.fail(errors.New("hibd didn't say which image it saved"))
	}
	hibapi.PrintSavedImage(a.stdout, *r.Image)
	return 0
}

func (a *app) imageList(ctx context.Context, cfg *config, _ []string) int {
	r, err := a.remote(ctx, cfg, shortTimeout, "image", "ls", "-json")
	if err != nil {
		return a.fail(err)
	}
	hibapi.PrintImages(a.stdout, r.Images)
	return 0
}

func (a *app) imageRemove(ctx context.Context, cfg *config, args []string) int {
	if !a.checkNames(args...) {
		return 2
	}
	if _, err := a.remote(ctx, cfg, 0, "image", "rm", args[0], "-json"); err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "removed image %s\n", args[0])
	return 0
}
