package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

const nextAfterInit = "run hib status to see what's next"

func (a *app) status(ctx context.Context, cfg *config, _ []string) int {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	var host *hibapi.Host
	if cfg.Azure != nil {
		state, err := a.powerState(ctx, cfg.Azure)
		if err != nil {
			return a.failWaiting(ctx, err, "")
		}
		if state != "VM running" {
			return a.asleep(cfg, state)
		}
		host = &hibapi.Host{State: hibapi.HostRunning}
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
	if a.json {
		a.reply(hibapi.Response{Status: r.Status, Host: host}, nil)
	} else {
		hibapi.PrintStatus(a.stdout, cfg.Host, *r.Status, nil)
		if next := nextSteps(*r.Status); len(next) > 0 {
			fmt.Fprintf(a.stdout, "\nNext: %s\n", strings.Join(next, ", "))
		}
	}
	if !r.Status.OK() {
		return 1
	}
	return 0
}

func (a *app) asleep(cfg *config, state string) int {
	if a.json {
		return a.reply(hibapi.Response{Host: &hibapi.Host{State: hibapi.HostAsleep, Detail: state}}, nil)
	}
	state = strings.Join(strings.Fields(termsafe.String(state)), " ")
	fmt.Fprintf(a.stdout, "host    %s: asleep (%s). Using a machine wakes it.\n", termsafe.String(cfg.Host), state)
	return 0
}

func nextSteps(s hibapi.Status) []string {
	var next []string
	if s.GitHub == nil {
		next = append(next, "hib connect github")
	}
	if s.Codex == nil {
		next = append(next, "hib connect codex (optional)")
	}
	if s.Machines != nil && *s.Machines == 0 {
		next = append(next, "hib new NAME")
	}
	return next
}
