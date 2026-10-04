package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
)

func (a *app) fork(ctx context.Context, cfg *config, args []string) int {
	fl := a.flags("fork")
	count := fl.Int("count", 1, fmt.Sprintf("copies to make, from 1 to %d", machines.MaxCopies))
	fl.Usage = func() {
		fmt.Fprintf(a.stderr, "usage: hib fork MACHINE [-count N]\n\n%s\n", snapshotHelp)
		fl.PrintDefaults()
	}
	names, err := a.parseCommand(fl, args, 1, "fork MACHINE [-count N]")
	if err != nil {
		return argumentExit(err)
	}
	if !a.checkNames(names...) {
		return 2
	}
	if *count < 1 || *count > machines.MaxCopies {
		fmt.Fprintf(a.stderr, "hib fork: bad copy count %d: use 1 to %d\n", *count, machines.MaxCopies)
		return 2
	}
	r, err := a.remoteSudo(ctx, cfg, 0, nil, "fork", names[0], "-count", strconv.Itoa(*count), "-json")
	var remote *hibapi.RemoteError
	if err != nil && !errors.As(err, &remote) {
		return a.fail(err)
	}
	hibapi.PrintFork(a.stdout, names[0], r.Machines)
	if err != nil {
		return a.fail(err)
	}
	if len(r.Machines) == 0 {
		return a.fail(errors.New("hibd didn't say which copies it made"))
	}
	return 0
}
