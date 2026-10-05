package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/solev/hibernaut/internal/hibapi"
)

const imageUsage = `usage: hib image build NAME [-force]
       hib image save MACHINE NAME
       hib image ls
       hib image rm NAME`

const snapshotHelp = `The origin pauses for a few seconds. SSH sessions into it may drop.
An image carries everything in the machine. hib puts only placeholders and your public key there,
but a secret you typed in yourself goes into the image too.`

func (a *app) imageSave(ctx context.Context, cfg *config, args []string) int {
	fl := a.flags("image save")
	fl.Usage = func() {
		fmt.Fprintf(a.stderr, "usage: hib image save MACHINE NAME\n\n%s\n", snapshotHelp)
		fl.PrintDefaults()
	}
	names, err := a.parseCommand(fl, args, 2, "image save MACHINE NAME")
	if err != nil {
		return argumentExit(err)
	}
	if !a.checkNames(names...) {
		return 2
	}
	r, err := a.remoteSudo(ctx, cfg, 0, nil, "image", "save", names[0], names[1], "-json")
	if err != nil {
		return a.fail(err)
	}
	if r.Image == nil {
		return a.fail(errors.New("hibd didn't say which image it saved"))
	}
	hibapi.PrintSavedImage(a.stdout, *r.Image)
	return 0
}

func (a *app) imageList(ctx context.Context, cfg *config, args []string) int {
	if _, err := a.parseCommand(a.flags("image ls"), args, 0, "image ls"); err != nil {
		return argumentExit(err)
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "image", "ls", "-json")
	if err != nil {
		return a.fail(err)
	}
	hibapi.PrintImages(a.stdout, r.Images)
	return 0
}

func (a *app) imageRemove(ctx context.Context, cfg *config, args []string) int {
	names, err := a.parseCommand(a.flags("image rm"), args, 1, "image rm NAME")
	if err != nil {
		return argumentExit(err)
	}
	if !a.checkNames(names...) {
		return 2
	}
	if _, err := a.remote(ctx, cfg, 0, "image", "rm", names[0], "-json"); err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "removed image %s\n", names[0])
	return 0
}
