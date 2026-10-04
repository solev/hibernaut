package hibd

import (
	"context"
	"fmt"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *App) fork(ctx context.Context, args []string) int {
	fl := a.flags("fork")
	count := fl.Int("count", 1, fmt.Sprintf("copies to make, from 1 to %d", machines.MaxCopies))
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "fork MACHINE [-count N] [-json]")
	if !ok {
		return 2
	}
	if *count < 1 || *count > machines.MaxCopies {
		fmt.Fprintf(a.Stderr, "hibd fork: bad copy count %d: use 1 to %d\n", *count, machines.MaxCopies)
		return 2
	}
	if err := a.needRoot("fork"); err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	setup, err := loadSetup(cfg, a.codexStore(cfg, a.warnf))
	if err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	m, err := a.managerWithSetup(setup)
	if err != nil {
		return a.fail(*asJSON, "fork", err)
	}
	made, err := m.Fork(ctx, names[0], *count)
	if *asJSON {
		r := hibapi.Response{Machines: made}
		if err != nil {
			r.Error = termsafe.String(err.Error())
		}
		return a.reply(r)
	}
	hibapi.PrintFork(a.Stdout, names[0], made)
	if err != nil {
		return a.fail(false, "fork", err)
	}
	return 0
}
