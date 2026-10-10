package hibd

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/linkctl"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
)

// newController reaches hibd proxy's control socket. Tests replace it.
var newController = func() (links.Controller, error) { return linkctl.Dial("") }

// listenCheckTimeout bounds the advisory check of what a newly published port is bound to.
const listenCheckTimeout = 5 * time.Second

// port is hibd's side of hib port add, hib port rm and hib port ls. The laptop never reads
// links.json itself: the file belongs to the host, and only hibd knows which links are open.
func (a *App) port(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, "usage: hibd port add NAME PORT [PORT...] [-json] | port rm NAME PORT [PORT...] [-json] | port ls [-json]")
		return 2
	}
	switch args[0] {
	case "add":
		return a.portChange(ctx, args[1:], true)
	case "rm":
		return a.portChange(ctx, args[1:], false)
	case "ls":
		return a.portLS(ctx, args[1:])
	}
	fmt.Fprintf(a.Stderr, "hibd port: unknown command %q\n", args[0])
	return 2
}

// portChange publishes or unpublishes a batch of one machine's ports. Everything after the
// name is resolved is keyed by the sandbox ID, because a name can be removed and made again
// while the command runs.
func (a *App) portChange(ctx context.Context, args []string, publishing bool) int {
	cmd := "port rm"
	if publishing {
		cmd = "port add"
	}
	fl := a.flags(cmd)
	asJSON := fl.Bool("json", false, "print JSON")
	rest, ok := a.parseAny(fl, args)
	if !ok {
		return 2
	}
	if len(rest) < 2 {
		fmt.Fprintf(a.Stderr, "usage: hibd %s NAME PORT [PORT...] [-json]\n", cmd)
		return 2
	}
	name := rest[0]
	ports, err := hibapi.ParsePorts(rest[1:])
	if err != nil {
		fmt.Fprintf(a.Stderr, "hibd %s: %s\n", cmd, termsafe.String(err.Error()))
		return 2
	}
	cfg, err := linksConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	e, err := a.engine()
	if err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	em, err := machines.Lookup(ctx, e, name)
	if err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	for _, p := range ports {
		if links.IsReserved(p) {
			return a.fail(*asJSON, cmd, reservedPort(p))
		}
	}
	var (
		after        links.Machine
		noop         []int
		have, adding int
	)
	err = links.NewStore(cfg.stateDir).Update(ctx, func(d *links.Doc) error {
		var err error
		if m := d.Machines[em.ID]; m != nil {
			have = len(m.Ports)
		}
		if publishing {
			adding = countNew(d.Machines[em.ID], ports)
			noop, err = d.Publish(em.ID, ports)
			if err == nil {
				_, err = d.Allocate(em.ID)
			}
		} else {
			noop, err = d.Unpublish(em.ID, ports)
		}
		if m := d.Machines[em.ID]; m != nil {
			after = links.Machine{Addr: m.Addr, Ports: slices.Clone(m.Ports)}
		}
		return err
	})
	var revoked error
	if !publishing && (err == nil || errors.Is(err, links.ErrNotFound)) {
		revoked = revoke(ctx, em.ID, ports)
	}
	if err != nil {
		return a.fail(*asJSON, cmd, portRefusal(err, name, noop, have, adding))
	}
	a.notePorts(name, noop, publishing)
	if publishing {
		a.warnLocalOnly(ctx, e, em, ports)
	}
	a.pushSetup(ctx, cfg, "published ports saved")
	reply := hibapi.Response{Ports: []hibapi.Ports{apiPorts(name, after, nil, "")}}
	if revoked != nil {
		msg := fmt.Sprintf("%s %s unpublished, but hibd proxy didn't confirm it closed open links: check hib port ls",
			hostPorts(name, ports), plural(len(ports), "is", "are"))
		if *asJSON {
			reply.Error = msg
			return a.reply(reply)
		}
		fmt.Fprintf(a.Stderr, "hibd %s: %s\n", cmd, msg)
		return 1
	}
	if *asJSON {
		return a.reply(reply)
	}
	if publishing {
		fmt.Fprintf(a.Stdout, "%s %s published to your machines as %s\n",
			hostPorts(name, ports), plural(len(ports), "is", "are"), hostPorts(name+".hib", ports))
	} else {
		fmt.Fprintf(a.Stdout, "%s %s no longer reachable from your machines\n", hostPorts(name, ports), plural(len(ports), "is", "are"))
	}
	return 0
}

func (a *App) portLS(ctx context.Context, args []string) int {
	fl := a.flags("port ls")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "port ls [-json]"); !ok {
		return 2
	}
	cfg, err := linksConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "port ls", err)
	}
	d, err := links.NewStore(cfg.stateDir).Read()
	if err != nil {
		return a.fail(*asJSON, "port ls", portRefusal(err, "", nil, 0, 0))
	}
	e, err := a.engine()
	if err != nil {
		return a.fail(*asJSON, "port ls", err)
	}
	ems, err := e.List(ctx, engine.ListOptions{})
	if err != nil {
		return a.fail(*asJSON, "port ls", err)
	}
	names := map[string]string{}
	for _, em := range ems {
		names[em.ID] = machines.Name(em)
	}
	type published struct {
		id, name string
		m        links.Machine
	}
	var found []published
	for id, m := range d.Machines {
		if m == nil || len(m.Ports) == 0 || !m.Addr.IsValid() || names[id] == "" {
			continue
		}
		found = append(found, published{id: id, name: names[id], m: *m})
	}
	slices.SortFunc(found, func(x, y published) int { return strings.Compare(x.name, y.name) })
	var stats *links.Stats
	if len(found) > 0 {
		s, err := openLinks(ctx)
		if err != nil {
			a.warnf("hibd proxy didn't say how many links are open: %v", err)
		} else {
			stats = &s
		}
	}
	all := make([]hibapi.Ports, 0, len(found))
	for _, f := range found {
		all = append(all, apiPorts(f.name, f.m, stats, f.id))
	}
	if *asJSON {
		return a.reply(hibapi.Response{Ports: all})
	}
	printPorts(a.Stdout, all)
	if len(all) == 0 {
		fmt.Fprintln(a.Stderr, "no published ports. hib port add NAME PORT publishes one")
	}
	return 0
}

// linksConfig loads the config of a command that only makes sense with links on.
func linksConfig(getenv func(string) string) (config, error) {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return config{}, err
	}
	if !cfg.linksConfiguredOn() {
		return config{}, fmt.Errorf("%w on this host", links.ErrLinksOff)
	}
	return cfg, nil
}

func countNew(m *links.Machine, ports []int) int {
	n := 0
	for _, p := range ports {
		if m == nil || !slices.Contains(m.Ports, p) {
			n++
		}
	}
	return n
}

func reservedPort(p int) error {
	switch p {
	case 8081:
		return errors.New("port 8081 is Hibernaut's SSH bridge and can't be published")
	case 49983:
		return errors.New("port 49983 is E2B's agent and can't be published")
	}
	return fmt.Errorf("port %d is reserved and can't be published", p)
}

// portRefusal turns the store's errors into what a person reads.
func portRefusal(err error, name string, noop []int, have, adding int) error {
	var newer interface{ NewerVersion() int }
	switch {
	case errors.As(err, &newer):
		return errors.New("hibd can't read its published ports: they were written by a newer hibd. Upgrade it")
	case errors.Is(err, links.ErrStateUnreadable):
		return fmt.Errorf("hibd can't read its published ports: %v. No machine can reach another until it's fixed", err)
	case errors.Is(err, links.ErrAlreadyPublished):
		return fmt.Errorf("%s %s already published", hostPorts(name, noop), plural(len(noop), "is", "are"))
	case errors.Is(err, links.ErrNotFound):
		return fmt.Errorf("%s %s published", hostPorts(name, noop), plural(len(noop), "wasn't", "weren't"))
	case errors.Is(err, links.ErrTooManyPorts) && have >= links.MaxPorts:
		return fmt.Errorf("%s already publishes %d ports, the most there is room for", name, have)
	case errors.Is(err, links.ErrTooManyPorts):
		return fmt.Errorf("that would make %d ports on %s, and %d is the most there is room for", have+adding, name, links.MaxPorts)
	case errors.Is(err, links.ErrTooManyMachines):
		return fmt.Errorf("%d machines already publish ports, the most there is room for", links.MaxMachines)
	case errors.Is(err, links.ErrNoAddresses):
		return errors.New("no virtual addresses left. addresses are never reused, so this needs a new host")
	}
	return err
}

// notePorts tells a person which ports of a batch were already the way they asked for.
func (a *App) notePorts(name string, noop []int, publishing bool) {
	if len(noop) == 0 {
		return
	}
	if publishing {
		a.notef("%s %s already published", hostPorts(name, noop), plural(len(noop), "was", "were"))
	} else {
		a.notef("%s %s published", hostPorts(name, noop), plural(len(noop), "wasn't", "weren't"))
	}
}

// revoke asks hibd proxy to close the links already using ports, and to cancel the ones
// waiting on a wake or a dial. hib port rm only reports success once it has answered.
func revoke(ctx context.Context, id string, ports []int) error {
	ctl, err := newController()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, links.CtlTimeout)
	defer cancel()
	_, err = ctl.Revoke(ctx, id, ports)
	return err
}

func openLinks(ctx context.Context) (links.Stats, error) {
	ctl, err := newController()
	if err != nil {
		return links.Stats{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, links.CtlTimeout)
	defer cancel()
	return ctl.Stat(ctx)
}

// warnLocalOnly warns when every socket listening on one of ports in the machine is bound to
// loopback. envd relays those to the machine's own address, so a link reaches them, and their
// clients arrive looking like 127.0.0.1. The check is advisory: a paused machine, a failed
// check or a port nothing listens on yet leaves the warning out.
func (a *App) warnLocalOnly(ctx context.Context, e engine.Engine, em engine.Machine, ports []int) {
	if em.State != engine.Running {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, listenCheckTimeout)
	defer cancel()
	res, err := e.Exec(ctx, em.ID, engine.ExecRequest{Cmd: "cat", Args: []string{"/proc/net/tcp", "/proc/net/tcp6"}})
	if err != nil {
		return
	}
	name := machines.Name(em)
	for _, p := range ports {
		if at, ok := loopbackOnly(res.Stdout, p); ok {
			a.notef("%s listens on %s, so its clients will look local to it", name, at)
		}
	}
}

// loopbackOnly reads /proc/net/tcp and tcp6 and reports where port listens when every socket
// listening on it is bound to a loopback address.
func loopbackOnly(tables []byte, port int) (netip.AddrPort, bool) {
	var first netip.Addr
	for line := range strings.Lines(string(tables)) {
		f := strings.Fields(line)
		// Fields 1 and 3 are the local address and the state, and 0A is LISTEN.
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		hexAddr, hexPort, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(hexPort, 16, 16)
		if err != nil || int(p) != port {
			continue
		}
		addr, ok := procAddr(hexAddr)
		if !ok {
			continue
		}
		if !addr.IsLoopback() {
			return netip.AddrPort{}, false
		}
		if !first.IsValid() {
			first = addr
		}
	}
	if !first.IsValid() {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(first, uint16(port)), true
}

// procAddr decodes an address from /proc/net/tcp, which prints each 32-bit word in the
// machine's byte order. Machines are x86-64, so that is little-endian.
func procAddr(s string) (netip.Addr, bool) {
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return netip.Addr{}, false
	}
	for i := 0; i < len(b); i += 4 {
		slices.Reverse(b[i : i+4])
	}
	addr, _ := netip.AddrFromSlice(b)
	return addr.Unmap(), true
}

// apiPorts is one machine's published ports. stats counts the links using each, by sandbox ID,
// and is nil when hibd proxy wasn't asked or didn't answer, which leaves every Open null.
func apiPorts(name string, m links.Machine, stats *links.Stats, id string) hibapi.Ports {
	p := hibapi.Ports{Machine: name, Published: make([]hibapi.PublishedPort, 0, len(m.Ports))}
	if m.Addr.IsValid() {
		p.Addr = m.Addr.String()
	}
	for _, port := range m.Ports {
		pp := hibapi.PublishedPort{Port: port}
		if stats != nil {
			n := stats.Ports[links.Open{Target: id, Port: port}]
			pp.Open = &n
		}
		p.Published = append(p.Published, pp)
	}
	return p
}

// printPorts writes hib port ls's table. OPEN is "-" for a machine when any of its counts is
// unknown.
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", termsafe.Line(p.Machine), termsafe.Line(p.Machine+".hib"),
			termsafe.Line(p.Addr), strings.Join(nums, ","), open)
	}
	tw.Flush()
}

// hostPorts writes ports as host:port, joined with "and".
func hostPorts(host string, ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprintf("%s:%d", host, p)
	}
	if len(parts) <= 1 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
