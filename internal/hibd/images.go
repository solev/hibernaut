package hibd

import (
	"context"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/leakscan"
	"github.com/solev/hibernaut/internal/termsafe"
)

const imageUsage = `usage: hibd image build NAME [-force] [-json]
       hibd image save MACHINE NAME [-json]
       hibd image ls [-json]
       hibd image rm NAME [-json]`

func (a *App) imageSave(ctx context.Context, args []string) int {
	fl := a.flags("image save")
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 2, "image save MACHINE NAME [-json]")
	if !ok {
		return 2
	}
	if err := a.needRoot("image save"); err != nil {
		return a.fail(*asJSON, "image save", err)
	}
	m, err := a.manager()
	if err != nil {
		return a.fail(*asJSON, "image save", err)
	}
	img, err := m.SaveImage(ctx, names[0], names[1])
	if err != nil {
		return a.fail(*asJSON, "image save", err)
	}
	if *asJSON {
		return a.reply(hibapi.Response{Image: &img})
	}
	hibapi.PrintSavedImage(a.Stdout, img)
	return 0
}

func (a *App) imageList(ctx context.Context, args []string) int {
	fl := a.flags("image ls")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "image ls [-json]"); !ok {
		return 2
	}
	m, err := a.manager()
	if err != nil {
		return a.fail(*asJSON, "image ls", err)
	}
	imgs, err := m.Images(ctx)
	if err != nil {
		return a.fail(*asJSON, "image ls", err)
	}
	if *asJSON {
		return a.reply(hibapi.Response{Images: imgs})
	}
	hibapi.PrintImages(a.Stdout, imgs)
	return 0
}

func (a *App) imageRemove(ctx context.Context, args []string) int {
	fl := a.flags("image rm")
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "image rm NAME [-json]")
	if !ok {
		return 2
	}
	m, err := a.manager()
	if err != nil {
		return a.fail(*asJSON, "image rm", err)
	}
	if err := m.RemoveImage(ctx, names[0]); err != nil {
		return a.fail(*asJSON, "image rm", err)
	}
	if *asJSON {
		return a.reply(hibapi.Response{})
	}
	fmt.Fprintf(a.Stdout, "removed image %s\n", termsafe.String(names[0]))
	return 0
}

func (a *App) durableSnapshot(ctx context.Context, cfg config, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	query := a.query()
	build, err := leakscan.SnapshotBuild(ctx, query, id)
	if err != nil {
		return err
	}
	return leakscan.WaitBuild(ctx, query, cfg.e2bStorage, build, leakscan.WaitOptions{Timeout: 2 * time.Minute})
}
