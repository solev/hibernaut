package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *app) status(ctx context.Context, cfg *config, _ []string) int {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	if cfg.Azure != nil {
		state, err := a.powerState(ctx, cfg.Azure)
		if err != nil {
			return a.fail(err)
		}
		if state != "VM running" {
			state = strings.Join(strings.Fields(termsafe.String(state)), " ")
			fmt.Fprintf(a.stdout, "host    %s: asleep (%s). Using a machine wakes it.\n", termsafe.String(cfg.Host), state)
			return 0
		}
	}
	sshArgs, err := cfg.sudoHibdArgs("status", "--json")
	if err != nil {
		return a.fail(err)
	}
	r, err := a.call(ctx, cfg, shortTimeout, sshArgs, callOptions{sudo: true})
	if err != nil {
		return a.failWaiting(ctx, err, "")
	}
	if r.Status == nil {
		return a.fail(errors.New("hibd didn't report a status"))
	}
	hibapi.PrintStatus(a.stdout, cfg.Host, *r.Status, nil)
	if !r.Status.OK() {
		return 1
	}
	return 0
}
