package hibd

import (
	"context"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

// addHosts gives setup the /etc/hosts block with links on, so a machine that new or fork makes
// has the other machines' names from the start rather than from the daemon's next pass.
// loadSetup has no engine to map sandbox IDs to names, so the block is built here the way
// updateSetup builds it: from links.json and every machine, paused ones included, since a
// paused machine keeps its name. When it can't be built, who, the machine or machines about to
// be made, starts without the names, which is a warning rather than a failure, as it is for the
// daemon.
func (a *App) addHosts(ctx context.Context, cfg config, setup *machines.Setup, who string) {
	if !cfg.linksConfiguredOn() {
		return
	}
	hosts, err := a.currentHosts(ctx, cfg)
	if err != nil {
		a.warnf("%s will start without the other machines' names: %v", who, err)
		return
	}
	setup.Hosts = hosts
}

func (a *App) currentHosts(ctx context.Context, cfg config) ([]byte, error) {
	e, err := a.engine()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ms, err := e.List(ctx, engine.ListOptions{})
	if err != nil {
		return nil, err
	}
	return hostsBlock(a.linkPartsFor(cfg).store, ms)
}
