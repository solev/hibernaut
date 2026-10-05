package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
)

func (a *app) fork(ctx context.Context, cfg *config, name string, count int) int {
	if !a.checkNames(name) {
		return 2
	}
	if count < 1 || count > machines.MaxCopies {
		return a.mistake(fmt.Sprintf("bad copy count %d: use 1 to %d", count, machines.MaxCopies))
	}
	r, err := a.remoteSudo(ctx, cfg, 0, callOptions{}, "fork", name, "-count", strconv.Itoa(count), "-json")
	var remote *hibapi.RemoteError
	if err != nil && !errors.As(err, &remote) {
		return a.failWaiting(ctx, err, fmt.Sprintf("The host may still be making copies of %s: check hib ls", name))
	}
	if err == nil && len(r.Machines) == 0 {
		err = errors.New("hibd didn't say which copies it made")
	}
	if a.json {
		return a.reply(hibapi.Response{Machines: r.Machines}, err)
	}
	hibapi.PrintFork(a.stdout, name, r.Machines)
	if err != nil {
		return a.fail(err)
	}
	return 0
}
