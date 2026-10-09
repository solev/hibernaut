package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/solev/hibernaut/internal/hibapi"
)

var errNoPorts = errors.New("this host's hibd is too old for hib port. Upgrade it")

func (a *app) portAdd(ctx context.Context, cfg *config, args []string) int {
	return a.portChange(ctx, cfg, args, true)
}

func (a *app) portRM(ctx context.Context, cfg *config, args []string) int {
	return a.portChange(ctx, cfg, args, false)
}

// portChange publishes or unpublishes ports of one machine. The checks here are the ones a
// usage mistake fails: everything about the machine and the state is hibd's to refuse.
func (a *app) portChange(ctx context.Context, cfg *config, args []string, publishing bool) int {
	switch len(args) {
	case 0:
		return a.mistake("missing NAME")
	case 1:
		if !a.checkNames(args[0]) {
			return 2
		}
		return a.mistake("missing PORT")
	}
	name := args[0]
	if !a.checkNames(name) {
		return 2
	}
	var ports []int
	for _, arg := range args[1:] {
		n, err := strconv.ParseUint(arg, 10, 16)
		if err != nil || n == 0 {
			return a.mistake(arg + " isn't a port. Ports are 1 to 65535")
		}
		if !slices.Contains(ports, int(n)) {
			ports = append(ports, int(n))
		}
	}
	if err := a.needPorts(ctx, cfg); err != nil {
		return a.failWaiting(ctx, err, "")
	}
	sub, doing := "rm", "unpublishing"
	if publishing {
		sub, doing = "add", "publishing"
	}
	remote := []string{"port", sub, name}
	for _, p := range ports {
		remote = append(remote, strconv.Itoa(p))
	}
	r, err := a.remote(ctx, cfg, shortTimeout, append(remote, "--json")...)
	if err != nil {
		var refused *hibapi.RemoteError
		if a.json && errors.As(err, &refused) && r.Ports != nil {
			return a.reply(hibapi.Response{Ports: r.Ports}, err)
		}
		return a.failWaiting(ctx, err, fmt.Sprintf("The host may still be %s %s: check hib port ls", doing, hostPorts(name, ports)))
	}
	if a.json {
		return a.reply(hibapi.Response{Ports: r.Ports}, nil)
	}
	verb := "is"
	if len(ports) > 1 {
		verb = "are"
	}
	if publishing {
		fmt.Fprintf(a.stdout, "%s %s published to your machines as %s\n", hostPorts(name, ports), verb, hostPorts(name+".hib", ports))
	} else {
		fmt.Fprintf(a.stdout, "%s %s no longer reachable from your machines\n", hostPorts(name, ports), verb)
	}
	return 0
}

func (a *app) portLS(ctx context.Context, cfg *config, _ []string) int {
	if err := a.needPorts(ctx, cfg); err != nil {
		return a.failWaiting(ctx, err, "")
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "port", "ls", "--json")
	if err != nil {
		return a.failWaiting(ctx, err, "")
	}
	all := r.Ports
	if all == nil {
		all = []hibapi.Ports{}
	}
	if a.json {
		return a.reply(hibapi.Response{Ports: all}, nil)
	}
	printPorts(a.stdout, all)
	if len(all) == 0 {
		fmt.Fprintln(a.stderr, "no published ports. hib port add NAME PORT publishes one")
	}
	return 0
}

// needPorts fails unless the host's hibd has the port command.
func (a *app) needPorts(ctx context.Context, cfg *config) error {
	features, err := a.features(ctx, cfg)
	if err != nil {
		return err
	}
	if !slices.Contains(features, hibapi.FeaturePorts) {
		return errNoPorts
	}
	return nil
}

// printPorts writes the published ports as a table. OPEN is how many links use a machine's
// ports now, or "-" when hibd proxy didn't count one of them.
func printPorts(w io.Writer, all []hibapi.Ports) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHOST\tIP\tPORTS\tOPEN")
	for _, p := range all {
		nums := make([]string, len(p.Published))
		total, counted := 0, true
		for i, pp := range p.Published {
			nums[i] = strconv.Itoa(pp.Port)
			if pp.Open == nil {
				counted = false
			} else {
				total += *pp.Open
			}
		}
		open := "-"
		if counted {
			open = strconv.Itoa(total)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", safe(p.Machine), safe(p.Machine+".hib"), safe(p.Addr), strings.Join(nums, ","), open)
	}
	tw.Flush()
}

// hostPorts writes each port as host:port, in one list.
func hostPorts(host string, ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = safe(host) + ":" + strconv.Itoa(p)
	}
	return joinList(parts, "and")
}
