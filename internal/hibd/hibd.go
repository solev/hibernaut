// Package hibd is the host daemon's command line: the commands hib runs over SSH, and that
// systemd and idle-deallocate run (spec D1). Commands that hib parses print JSON with --json.
package hibd

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/busy"
	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/linkctl"
	"github.com/solev/hibernaut/internal/linkguard"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/pauseall"
	proxypkg "github.com/solev/hibernaut/internal/proxy"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/slots"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/version"
)

const defaultConfig = "/etc/hibernaut/hibd.env"

const usage = `usage: hibd <command> [flags]

commands:
  new NAME          make a machine (-image, default ` + images.Default + `; -size small, medium or large,
                    default medium; -idle, default 15m)
  ls                list machines (-state running|paused, -q for IDs only)
  rm NAME           delete a machine for good
  pause NAME        pause a machine
  fork MACHINE      copy a machine, as root (-count, default 1, most 10)
  bridge NAME       connect stdin and stdout to a machine's SSH server
  secret add NAME   store a bound secret from stdin (-env VAR -bind SITE[,SITE...])
  secret ls         list bound secrets
  secret rm NAME    remove a bound secret
  log               show secret swap audit entries (-n, default 50)
  port add NAME PORT [PORT...]
                    publish a machine's ports to the other machines, as NAME.hib:PORT
  port rm NAME PORT [PORT...]
                    stop publishing them, and close the links using them
  port ls           list published ports and how many links each has open
  connect github    store a GitHub token as secret github, after GitHub accepts it, and give
                    machines git's identity, as root. -source gh|token reads the token from
                    stdin. -source device gets one with GitHub's device flow, and stops when
                    stdin ends
  connect codex     log in to Codex on a ChatGPT plan with a device code, keep the login, and
                    give machines a login file of placeholders, as root
  disconnect codex|github
                    remove the host's login, as root. Machines keep placeholders
  codex refresh     refresh the Codex login once half its token's lifetime has passed, as root
                    (-force refreshes now, even when a refresh is stuck)
  status            check E2B, every secret and the Codex login, as root
  ca init           create the Hibernaut CA once, as root
  proxy             run the egress proxy, as root (-unhook removes its iptables rule)
  image build NAME  build an image in every size (-force rebuilds every step)
                    -verbose keeps full logs. -json -events sends build step events
  image save MACHINE NAME
                    save a machine as an image, memory included, as root
  image ls          list built-in and saved images
  image rm NAME     remove a saved image not used by any machine
  ready             wait until E2B answers (-timeout)
  pause-all         pause every running machine; wait until none is running
  leakscan          scan a paused machine or image for real secrets, as root. Running machines
                    are paused first and stay paused (-image NAME, -canary, -json)
  cleanup           delete unused build directories and trim old snapshot bodies, as root
                    (-n only says what it would delete or trim, and why it keeps the others)
  daemon            keep busy machines running (-every), clean up when a machine is
                    removed and hourly, and keep the Codex login fresh; systemd runs it as
                    root
  version           print the version

new, ls, rm, pause, fork, port, image build|save|ls|rm, connect, disconnect, codex refresh,
status and version
print JSON with -json.

hibd reads its settings from ` + defaultConfig + ` ($HIBD_CONFIG overrides the path).
Environment variables of the same names override the file:
  E2B_API_URL           default http://127.0.0.1:3000
  E2B_SANDBOX_URL       default http://127.0.0.1:3002
  E2B_API_KEY           required
  E2B_HEALTH_URLS       what ready waits for, space-separated
  HIBD_STATE_DIR        default /var/lib/hibernaut
  HIBD_VAULT            default $HIBD_STATE_DIR/vault.json
  HIBD_VAULT_KEY        default /etc/hibernaut/vault.key
  HIBD_E2B_STORAGE      E2B snapshot storage, default /var/lib/e2b/local-template-storage
  HIBD_TRIM             snapshot trimming: off, e2e or all, default e2e
  HIBD_AUDIT_LOG        default /var/log/hibernaut/audit.jsonl
  HIBD_AUTHORIZED_KEYS  keys that may log in to new machines, default ~/.ssh/authorized_keys
  HIBD_CA_CERT          CA certificate for the proxy and image builds, default /etc/hibernaut/ca.crt
  HIBD_CA_KEY           CA private key, default /etc/hibernaut/ca.key
  HIBD_PROXY_PORT       interception port, default 5443
  HIBD_QUIESCING        new, bridge, fork and image build|save refuse while it exists. daemon leaves
                        machines and the Codex login alone; default /run/hibernaut/quiescing,
                        which quiesce makes before pausing everything
  HIBD_AGENTS           programs that keep a machine busy while they run, space-separated;
                        default codex copilot claude
  HIBD_CGROUPS          where E2B keeps its machines' cgroups, default /sys/fs/cgroup/e2b
  HIBD_LINK_PORT        port for links between machines, default 0, which is off
  HIBD_LINK_LOG         link log, default /var/log/hibernaut/links.jsonl
`

const (
	createTimeout = 5 * time.Minute
	dialTimeout   = 3 * time.Minute
	buildTimeout  = time.Hour

	minIdle = time.Minute
)

// App runs hibd commands.
type App struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// NewEngine returns the engine to use. If it's nil, App uses E2B, as configured.
	NewEngine func() (engine.Engine, error)
	// ReadRoom reads the room on the host. If it's nil, App reads this host's.
	ReadRoom func() (room.Host, error)
	// Clean deletes unused directories and trims old snapshot bodies, like cleanup.Host.Clean.
	// If it's nil, App cleans up this host's.
	Clean func(ctx context.Context, e engine.Engine, now time.Time, dryRun bool, logf func(format string, args ...any)) error
	// OpenTrim supplies the snapshot filesystem. Nil uses cleanup.OpenTrimStorage.
	OpenTrim func(string) (cleanup.TrimStorage, error)
	// Timeout supplies cleanup's phase contexts. Nil uses context.WithTimeout.
	Timeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	// WaitCleanup waits before retrying a held cleanup lock. Nil waits with a context-aware timer.
	WaitCleanup func(context.Context) error
	// Query runs SQL in E2B's database. If it's nil, App uses cleanup.Postgres.
	Query func(ctx context.Context, sql string) ([][]string, error)
	// EUID returns the effective user ID. If it's nil, App uses os.Geteuid.
	EUID func() int
	// NewCanary returns a canary secret for leakscan -canary. If it's nil, App uses random bytes.
	NewCanary func() (string, error)
	// GitHubAPI is the base URL of GitHub's API, for tests only.
	// If it's "", App uses github.APIBase.
	GitHubAPI string
	// GitHubClient is the HTTP client for GitHub's API, for tests only. If it's nil, App uses
	// github.NewClient().
	GitHubClient *http.Client
	// GitHubDeviceFlow runs hibd connect github -source device, for tests only. If it's nil, App
	// uses a DeviceFlow with github.ClientID and the rest of its defaults.
	GitHubDeviceFlow *github.DeviceFlow
	// CodexBin is the codex that hibd connect codex runs, for tests only. If it's "", App uses
	// /usr/local/bin/codex.
	CodexBin string
	// CodexTokenURL is OpenAI's token endpoint, and CodexClient the HTTP client for it, for tests
	// only. Empty means the codex.Store's own.
	CodexTokenURL string
	CodexClient   *http.Client
	// ProxyHook sends machines' HTTPS traffic to hibd proxy, for tests only. If it's nil, App
	// uses the iptables rule for the proxy's port.
	ProxyHook proxypkg.Rule
	// ProxyListen opens hibd proxy's port, for tests only. If it's nil, App uses net.Listen.
	ProxyListen func(network, address string) (net.Listener, error)
	// Now supplies hibd's clock. If it's nil, App uses time.Now.
	Now func() time.Time
	// DaemonTicks supplies the daemon's ticks. If it's nil, App uses a ticker at -every.
	DaemonTicks <-chan time.Time

	codexEvery time.Duration
	// linkParts replaces the links pieces hibd's own commands use, for tests only.
	linkParts linkParts
}

// linkParts are the links pieces that pause, rm, pause-all, the leak scan, ls, bridge and
// cleanup use with links on. A nil field uses the production one.
type linkParts struct {
	store    links.Store
	ctl      links.Controller
	dropper  links.Dropper
	resolver links.Resolver
}

// Run runs the command in args and returns its exit code: 0, 1 if it failed, or 2 if args
// are wrong.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Stderr, usage)
		return 2
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "new":
		return a.new(ctx, args)
	case "ls":
		return a.ls(ctx, args)
	case "rm", "pause":
		return a.pauseOrRemove(ctx, cmd, args)
	case "fork":
		return a.fork(ctx, args)
	case "bridge":
		return a.bridge(ctx, args)
	case "secret":
		return a.secret(ctx, args)
	case "connect":
		return a.connect(ctx, args)
	case "disconnect":
		return a.disconnect(ctx, args)
	case "codex":
		return a.runCodex(ctx, args)
	case "status":
		return a.status(ctx, args)
	case "port":
		return a.port(ctx, args)
	case "log":
		return a.log(args)
	case "ca":
		return a.ca(args)
	case "proxy":
		return a.proxy(ctx, args)
	case "image":
		return a.image(ctx, args)
	case "ready":
		return a.ready(ctx, args)
	case "pause-all":
		return a.pauseAll(ctx, args)
	case "leakscan":
		return a.leakscan(ctx, args)
	case "cleanup":
		return a.cleanup(ctx, args)
	case "daemon":
		return a.daemon(ctx, args)
	case "version", "-v", "--version":
		return a.version(args)
	case "help", "-h", "--help":
		fmt.Fprint(a.Stdout, usage)
		return 0
	}
	fmt.Fprintf(a.Stderr, "hibd: unknown command %q\n\n%s", cmd, usage)
	return 2
}

func (a *App) new(ctx context.Context, args []string) int {
	fl := a.flags("new")
	image := fl.String("image", images.Default, "the image to make the machine from")
	sizeName := fl.String("size", "", "the machine's size: small, medium or large (default medium, or a saved image's size)")
	idle := fl.Duration("idle", machines.DefaultIdle, "pause the machine once it has been idle this long; 0 means never")
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "new NAME")
	if !ok {
		return 2
	}
	var size images.Size
	if isSet(fl, "size") {
		s, err := images.SizeNamed(*sizeName)
		if err != nil {
			return a.fail(*asJSON, "new", err)
		}
		size = s
	}
	if *idle != 0 && (*idle < minIdle || *idle > machines.MaxRun) {
		return a.fail(*asJSON, "new", fmt.Errorf("bad idle time %v: want 0 (never pause), or %s to %s",
			*idle, shortDuration(minIdle), shortDuration(machines.MaxRun)))
	}
	if err := a.awake(); err != nil {
		return a.fail(*asJSON, "new", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "new", err)
	}
	setup, err := loadSetup(cfg, a.codexStore(cfg, a.warnf))
	if err != nil {
		return a.fail(*asJSON, "new", err)
	}
	m, err := a.managerWithSetup(setup)
	if err != nil {
		return a.fail(*asJSON, "new", err)
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	mach, err := m.CreateSize(ctx, names[0], *image, size, *idle)
	if err != nil {
		return a.fail(*asJSON, "new", err)
	}
	if *asJSON {
		return a.reply(hibapi.Response{Machine: &mach})
	}
	fmt.Fprintf(a.Stdout, "made %s (%s)\n", mach.Name, mach.ID)
	return 0
}

func (a *App) ls(ctx context.Context, args []string) int {
	fl := a.flags("ls")
	state := fl.String("state", "", "only machines in this state: running or paused")
	quiet := fl.Bool("q", false, "print only IDs")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "ls"); !ok {
		return 2
	}
	switch engine.State(*state) {
	case "", engine.Running, engine.Paused:
	default:
		fmt.Fprintf(a.Stderr, "hibd ls: -state must be running or paused, not %q\n", *state)
		return 2
	}
	m, err := a.manager()
	if err != nil {
		return a.fail(*asJSON, "ls", err)
	}
	ms, err := m.List(ctx, engine.State(*state))
	if err != nil {
		return a.fail(*asJSON, "ls", err)
	}
	withPorts := false
	if !*quiet {
		withPorts = a.addPorts(ms)
	}
	switch {
	case *asJSON:
		r := hibapi.Response{Machines: ms}

		if rm, err := m.Room(ctx, images.Default); err == nil {
			r.Room = &rm
		}
		return a.reply(r)
	case *quiet:
		for _, m := range ms {
			fmt.Fprintln(a.Stdout, m.ID)
		}
		return 0
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	// With links off the table is M11's, so the PORTS column only shows with links on.
	ports := func(string) string { return "" }
	if withPorts {
		ports = func(s string) string { return s + "\t" }
	}
	fmt.Fprintln(tw, "NAME\tID\tSTATE\tIMAGE\tSIZE\tIDLE\t"+ports("PORTS")+"STARTED\tENDS")
	for _, m := range ms {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s%s\t%s\n", orDash(m.Name), m.ID, m.State, orDash(m.Image), orDash(m.Size),
			hibapi.FormatIdle(m.IdleSeconds), ports(formatPorts(m.Ports)), m.StartedAt.UTC().Format(time.RFC3339), m.EndAt.UTC().Format(time.RFC3339))
	}
	tw.Flush()
	return 0
}

// addPorts fills in each machine's published ports with links on, and reports whether they are
// on. A machine that publishes nothing gets an empty list rather than none. Not being able to
// read them is a warning, not a failure, so ls still lists the machines.
func (a *App) addPorts(ms []hibapi.Machine) bool {
	cfg, err := loadConfig(a.Getenv)
	if err != nil || !cfg.linksConfiguredOn() {
		return false
	}
	doc, err := a.linkPartsFor(cfg).store.Read()
	if err != nil {
		a.warnf("hibd can't read its published ports: %v", err)
		return true
	}
	for i := range ms {
		ms[i].Ports = []int{}
		if entry := doc.Machines[ms[i].ID]; entry != nil {
			ms[i].Ports = append(ms[i].Ports, entry.Ports...)
		}
	}
	return true
}

func formatPorts(ports []int) string {
	if len(ports) == 0 {
		return "-"
	}
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

func shortDuration(d time.Duration) string {
	sec := int64(d / time.Second)
	return hibapi.FormatIdle(&sec)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *App) byName(ctx context.Context, cmd string, args []string, do func(*machines.Manager, context.Context, string) error) int {
	fl := a.flags(cmd)
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, cmd+" NAME")
	if !ok {
		return 2
	}
	m, err := a.manager()
	if err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	if err := do(m, ctx, names[0]); err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	if *asJSON {
		return a.reply(hibapi.Response{})
	}
	return 0
}

// pauseOrRemove is hibd pause and hibd rm. With links off they are M11's byName. With links on
// they go through pauseFenced, pause marks the machine as paused by hand, and rm drops its entry
// from the link state once it's gone. Its address stays spent, because addresses are never
// handed out twice.
func (a *App) pauseOrRemove(ctx context.Context, cmd string, args []string) int {
	do := (*machines.Manager).Pause
	if cmd == "rm" {
		do = (*machines.Manager).Remove
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil || !cfg.linksConfiguredOn() {
		return a.byName(ctx, cmd, args, do)
	}
	fl := a.flags(cmd)
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, cmd+" NAME")
	if !ok {
		return 2
	}
	name := names[0]
	m, err := a.manager()
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
	store := a.linkPartsFor(cfg).store
	err = a.pauseFenced(ctx, cfg, em.ID, name, cmd, cmd+" "+name, func(ctx context.Context) error {
		if err := do(m, ctx, name); err != nil || cmd != "pause" {
			return err
		}
		err := store.Update(ctx, func(d *links.Doc) error {
			d.Entry(em.ID).PausedByHand = true
			return nil
		})
		if err != nil {
			return fmt.Errorf("paused %s, but couldn't mark it as paused by hand, so a link may wake it: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	if cmd == "rm" {
		unchanged := errors.New("no entry")
		err := store.Update(ctx, func(d *links.Doc) error {
			if _, ok := d.Machines[em.ID]; !ok {
				return unchanged
			}
			delete(d.Machines, em.ID)
			return nil
		})
		if err != nil && !errors.Is(err, unchanged) {
			a.warnf("removed %s, but couldn't drop its entry from the link state: %v. hibd cleanup drops it later", name, err)
		}
	}
	if *asJSON {
		return a.reply(hibapi.Response{})
	}
	return 0
}

// linksConfiguredOn reports whether HIBD_LINK_PORT turns links on. Only a port of 0 is off.
// Every hibd command asks this one question, so none of them can disagree about it.
func (c config) linksConfiguredOn() bool {
	return c.linkPort != 0
}

// parseLinkPort reads HIBD_LINK_PORT. A value that isn't a port number is an error rather than
// a guess: a pause that read links as off would raise no fence while the listener served.
func parseLinkPort(v string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || port < 0 || port > 65535 {
		return 0, fmt.Errorf("HIBD_LINK_PORT is %q, but it must be a port number, or 0 to leave links off", v)
	}
	return port, nil
}

func (a *App) linkPartsFor(cfg config) linkParts {
	p := a.linkParts
	if p.store == nil {
		p.store = links.NewStore(cfg.stateDir)
	}
	return p
}

// pauseMachine pauses m the way every pause Hibernaut starts has to: with e.Pause and nothing
// else with links off, as M11 did, and through pauseFenced with links on.
func (a *App) pauseMachine(ctx context.Context, cfg config, e engine.Engine, m engine.Machine, why, rerun string) error {
	if !cfg.linksConfiguredOn() {
		return e.Pause(ctx, m.ID)
	}
	name := cmp.Or(machines.Name(m), m.ID)
	return a.pauseFenced(ctx, cfg, m.ID, name, why, rerun, func(ctx context.Context) error {
		return e.Pause(ctx, m.ID)
	})
}

// pauseFenced runs op, which pauses or removes machine id, so that no link outlives it. It
// raises id's fence, so nothing admits a link to or from id, asks hibd proxy to close id's
// links and waits for the answer, and only then runs op. When the proxy doesn't answer within
// links.CtlTimeout, it deletes id's gates in the kernel instead, and runs op only once they are
// gone. Otherwise it fails closed: op doesn't run. The fence comes down once op returns, whether
// op succeeded or not. op's error comes back as it was, so callers can still tell
// engine.ErrNotRunning apart.
//
// With links off it runs op and nothing else. name is what messages call the machine, why is
// links.Fence.Why, and rerun is the hibd command to run as root when deleting the gates needs it.
func (a *App) pauseFenced(ctx context.Context, cfg config, id, name, why, rerun string, op func(context.Context) error) error {
	if !cfg.linksConfiguredOn() {
		return op(ctx)
	}
	p := a.linkPartsFor(cfg)
	release, err := p.store.Fence(ctx, id, why)
	if err != nil {
		if release != nil {
			release()
		}
		return fmt.Errorf("left %s as it was: couldn't mark it as being paused in the link state: %w", name, err)
	}
	defer release()
	if err := p.closeLinks(ctx, cfg, id, name, rerun); err != nil {
		return err
	}
	return op(ctx)
}

// closeLinks asks hibd proxy to close every link with id at either end. If the proxy doesn't
// answer, it deletes id's gates in the kernel, which needs root: the kernel then carries nothing
// more for any socket the proxy may still hold. A machine that isn't running has no gates.
func (p linkParts) closeLinks(ctx context.Context, cfg config, id, name, rerun string) error {
	ctl := p.ctl
	var closeErr error
	if ctl == nil {
		c, err := linkctl.Dial("")
		if err != nil {
			closeErr = err
		} else {
			ctl = c
		}
	}
	if ctl != nil {
		cctx, cancel := context.WithTimeout(ctx, links.CtlTimeout)
		_, closeErr = ctl.Close(cctx, id)
		cancel()
	}
	if closeErr == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("left %s as it was: %w", name, err)
	}
	unconfirmed := fmt.Sprintf("left %s as it was: hibd proxy didn't confirm its links are closed (%v)", name, closeErr)
	resolver := p.resolver
	if resolver == nil {
		runs, err := linkadmit.NewRuns(cfg.cgroups)
		if err != nil {
			return fmt.Errorf("%s, and finding its gates failed: %w", unconfirmed, err)
		}
		resolver = slots.Resolver{Finder: slots.Finder{Cgroups: cfg.cgroups}, Runs: runs}
	}
	addr, _, err := resolver.Addr(id)
	switch {
	case errors.Is(err, links.ErrNotRunning):
		return nil
	case err != nil:
		return fmt.Errorf("%s, and finding its gates failed: %w", unconfirmed, err)
	}
	dropper := p.dropper
	if dropper == nil {
		dropper = linkguard.New()
	}
	if err := dropper.Drop(addr); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("%s, and deleting its gates in the kernel needs root (%w). Run it as root: sudo hibd %s", unconfirmed, err, rerun)
		}
		return fmt.Errorf("%s, and deleting its gates in the kernel failed: %w", unconfirmed, err)
	}
	return nil
}

func (a *App) bridge(ctx context.Context, args []string) int {
	fl := a.flags("bridge")
	names, ok := a.parse(fl, args, 1, "bridge NAME")
	if !ok {
		return 2
	}
	if err := a.awake(); err != nil {
		return a.fail(false, "bridge", err)
	}
	m, err := a.manager()
	if err != nil {
		return a.fail(false, "bridge", err)
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	conn, err := m.Dial(dctx, names[0])
	if err == nil {
		a.startedByHand(dctx, names[0])
	}
	cancel()
	if err != nil {
		return a.fail(false, "bridge", err)
	}
	defer conn.Close()
	if err := hibapi.WriteBridgeHeader(a.Stdout, conn.HostKey); err != nil {
		return a.fail(false, "bridge", err)
	}

	done := make(chan struct{}, 2)
	go func() { io.Copy(conn, a.Stdin); done <- struct{}{} }()
	go func() { io.Copy(a.Stdout, conn); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return 0
}

// startedByHand clears name's paused-by-hand mark with links on, once a person has started it
// through the bridge, which hib ssh, hib exec and everything else a person runs go through. It
// is the only thing that clears the mark: save and fork resume a machine too, and must not.
// Failing to clear it is a warning, since the person still gets their session.
func (a *App) startedByHand(ctx context.Context, name string) {
	cfg, err := loadConfig(a.Getenv)
	if err != nil || !cfg.linksConfiguredOn() {
		return
	}
	e, err := a.engine()
	if err != nil {
		a.warnf("couldn't clear %s's paused-by-hand mark, so links still can't reach it: %v", name, err)
		return
	}
	em, err := machines.Lookup(ctx, e, name)
	if err != nil {
		a.warnf("couldn't clear %s's paused-by-hand mark, so links still can't reach it: %v", name, err)
		return
	}
	store := a.linkPartsFor(cfg).store
	doc, err := store.Read()
	if err == nil {
		if entry := doc.Machines[em.ID]; entry == nil || !entry.PausedByHand {
			return
		}
		err = store.Update(ctx, func(d *links.Doc) error {
			if entry := d.Machines[em.ID]; entry != nil {
				entry.PausedByHand = false
			}
			return nil
		})
	}
	if err != nil {
		a.warnf("couldn't clear %s's paused-by-hand mark, so links still can't reach it: %v", name, err)
	}
}

func (a *App) image(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, imageUsage)
		return 2
	}
	switch args[0] {
	case "save":
		return a.imageSave(ctx, args[1:])
	case "ls":
		return a.imageList(ctx, args[1:])
	case "rm":
		return a.imageRemove(ctx, args[1:])
	case "build":
	default:
		fmt.Fprintln(a.Stderr, imageUsage)
		return 2
	}
	fl := a.flags("image build")
	force := fl.Bool("force", false, "rebuild every step, not only those that changed")
	verbose := fl.Bool("verbose", false, "print the full build log instead of step lines")
	asJSON := fl.Bool("json", false, "print JSON")
	events := fl.Bool("events", false, "send a JSON response for each step start and end")
	names, ok := a.parse(fl, args[1:], 1, "image build NAME")
	if !ok {
		return 2
	}
	if *events && !*asJSON {
		fmt.Fprintln(a.Stderr, "hibd image build: -events requires -json")
		return 2
	}
	fail := func(err error) int {
		if *events {
			return a.buildReply(hibapi.Response{Error: termsafe.String(withNextStep("image build", err).Error())})
		}
		return a.fail(*asJSON, "image build", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return fail(err)
	}
	caPEM, err := os.ReadFile(cfg.caCert)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("no Hibernaut CA at %s: run `sudo hibd ca init` (vm/setup-host.sh does this)", cfg.caCert)
		}
		return fail(err)
	}
	sizes := images.BuildSizes()
	specs := make([]engine.TemplateSpec, len(sizes))
	for i, size := range sizes {
		spec, err := images.Spec(names[0], size, caPEM)
		if err != nil {
			return fail(err)
		}
		spec.Force = *force
		specs[i] = spec
	}
	if err := a.awake(); err != nil {
		return fail(err)
	}
	e, err := a.engine()
	if err != nil {
		return fail(err)
	}
	m, err := a.manager()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()

	var template string
	var built []string
	for i, size := range sizes {
		id, err := a.buildSize(ctx, e, m, names[0], size, specs[i], *verbose, *events)
		if err != nil {
			return fail(buildFailed(err, sizes, i))
		}
		if size == images.DefaultSize() {
			template = id
		}
		built = append(built, size.Name())
	}
	if *asJSON {
		if *events {
			return a.buildReply(hibapi.Response{Template: template, Sizes: built})
		}
		return a.reply(hibapi.Response{Template: template, Sizes: built})
	}
	fmt.Fprintln(a.Stdout, template)
	return 0
}

func (a *App) buildSize(ctx context.Context, e engine.Engine, m *machines.Manager, image string, size images.Size, spec engine.TemplateSpec, verbose, events bool) (string, error) {
	if err := m.CheckBuildRoom(ctx, image, size); err != nil {
		return "", err
	}
	start := a.now()
	buildLog := buildLog{image: image, size: size.Name(), out: a.Stderr, verbose: verbose}
	if events {
		buildLog.events = a.Stdout
	}
	id, err := e.BuildTemplate(ctx, spec, buildLog.entry)
	buildLog.finish(buildLog.last, err != nil)
	err = errors.Join(err, buildLog.err)
	if err != nil {
		return "", fmt.Errorf("building %s (%s): %w", image, size.Name(), err)
	}
	fmt.Fprintf(a.Stderr, "built %s (%s) in %v\n", termsafe.Line(image), termsafe.Line(size.Name()), a.now().Sub(start).Round(time.Second))
	return id, nil
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func buildFailed(err error, sizes []images.Size, failed int) error {
	names := func(sizes []images.Size) string {
		var out []string
		for _, s := range sizes {
			out = append(out, s.Name())
		}
		return cmp.Or(strings.Join(out, ", "), "none")
	}
	err = fmt.Errorf("%w. Built: %s. Failed: %s", err, names(sizes[:failed]), sizes[failed].Name())
	if rest := sizes[failed+1:]; len(rest) > 0 {
		err = fmt.Errorf("%w. Not tried: %s", err, names(rest))
	}
	return err
}

func isSet(fl *flag.FlagSet, name string) bool {
	set := false
	fl.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

func (a *App) ready(ctx context.Context, args []string) int {
	fl := a.flags("ready")
	timeout := fl.Duration("timeout", 5*time.Minute, "give up after this long")
	if _, ok := a.parse(fl, args, 0, "ready"); !ok {
		return 2
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(false, "ready", err)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	for _, url := range cfg.healthURLs {
		for {
			err := healthy(ctx, client, url)
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return a.fail(false, "ready", fmt.Errorf("E2B isn't ready after %v: %w", *timeout, err))
			case <-time.After(time.Second):
			}
		}
	}
	return 0
}

func healthy(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}

func (a *App) pauseAll(ctx context.Context, args []string) int {
	fl := a.flags("pause-all")
	timeout := fl.Duration("timeout", 4*time.Minute, "give up after this long")
	if _, ok := a.parse(fl, args, 0, "pause-all"); !ok {
		return 2
	}
	e, err := a.engine()
	if err != nil {
		fmt.Fprintln(a.Stderr, "hibd:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	logger := log.New(a.Stderr, "pause-all: ", 0)
	opts := pauseall.Options{Logf: logger.Printf}
	// engine has just loaded the same config, so it only fails here when a test supplies the
	// engine, and pause-all then runs as it did before links.
	cfg, err := loadConfig(a.Getenv)
	made := false
	if err == nil && cfg.linksConfiguredOn() {
		made, err = makeQuiescing(cfg.quiescing)
		if err != nil {
			logger.Printf("couldn't make %s, so links may wake machines meanwhile: %v", cfg.quiescing, err)
		}
		opts.Pause = func(ctx context.Context, id string) error {
			return a.pauseMachine(ctx, cfg, e, engine.Machine{ID: id}, "pause-all", "pause-all")
		}
	}
	start := time.Now()
	paused, err := pauseall.Run(ctx, e, opts)
	if err != nil {
		logger.Printf("%v", err)
		if made {
			if err := os.Remove(cfg.quiescing); err != nil && !errors.Is(err, fs.ErrNotExist) {
				logger.Printf("couldn't remove %s: %v", cfg.quiescing, err)
			}
		}
		return 1
	}
	logger.Printf("done: paused %d machine(s) in %v; none running", len(paused), time.Since(start).Round(time.Millisecond))
	return 0
}

// makeQuiescing makes the quiescing marker at path, so the link listener stops admitting and
// renewing links before pause-all pauses anything, and reports whether this call made it. It is
// left in place once pause-all succeeds, as quiesce leaves it, and removed only by whoever made
// it, if pausing fails.
func makeQuiescing(path string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

func (a *App) cleanup(ctx context.Context, args []string) int {
	fl := a.flags("cleanup")
	dryRun := fl.Bool("n", false, "only say what would be deleted or trimmed, and why the rest is kept")
	if _, ok := a.parse(fl, args, 0, "cleanup [-n]"); !ok {
		return 2
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(false, "cleanup", err)
	}
	e, err := a.engine()
	if err != nil {
		return a.fail(false, "cleanup", err)
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	logger := log.New(a.Stdout, "", 0)
	logf := func(format string, args ...any) {
		logger.Printf("%s", termsafe.String(fmt.Sprintf(format, args...)))
	}
	if err := a.clean(ctx, ctx, cfg, e, now, *dryRun, false, logf); err != nil {
		return a.fail(false, "cleanup", err)
	}
	return 0
}

func (a *App) clean(ctx, listingCtx context.Context, cfg config, e engine.Engine, now time.Time, dryRun, sweep bool, logf func(format string, args ...any)) error {
	unlock, err := a.lockCleanup(ctx, cfg.stateDir, !sweep, logf)
	if err != nil {
		return err
	}
	// What might be left over is read before E2B is listed, so nothing made after the listing
	// can look forgotten.
	left := a.readLinkLeftovers(cfg, now)
	var listed map[string]bool
	err = a.cleanLocked(ctx, listingCtx, cfg, e, now, dryRun, sweep, logf, func(l cleanup.Listing) {
		listed = make(map[string]bool, len(l.Machines))
		for _, m := range l.Machines {
			listed[m.ID] = true
		}
	})
	// Pruning takes links.lock, which must never nest under the cleanup lock.
	unlock()
	if listed == nil {
		return err
	}
	pctx := ctx
	if sweep {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, sweepTimeout)
		defer cancel()
	}
	if perr := a.pruneLinkLeftovers(pctx, cfg, e, left, listed, dryRun, logf); perr != nil {
		return errors.Join(err, perr)
	}
	return err
}

// cleanLocked is the cleanup itself, run under the cleanup lock. It hands listed the machines
// E2B lists once it has listed them all. Clean replaces all of it, so nothing is pruned then.
func (a *App) cleanLocked(ctx, listingCtx context.Context, cfg config, e engine.Engine, now time.Time, dryRun, sweep bool, logf func(format string, args ...any), listed func(cleanup.Listing)) error {
	if a.Clean != nil {
		return a.Clean(listingCtx, e, now, dryRun, logf)
	}
	query := a.Query
	if query == nil {
		query = cleanup.Postgres
	}
	m := machines.New(machines.Config{Engine: e, StateDir: cfg.stateDir})
	h := cleanup.Host{
		Storage: cfg.e2bStorage, Cache: cleanup.CacheDir, Query: query, Engine: e,
		TrimScope: cfg.trimScope, OpenTrim: a.OpenTrim, Timeout: a.Timeout,
		ReadGuard: func(ctx context.Context) (func(), error) {
			return a.lockCleanupRead(ctx, m, !sweep, logf)
		},
		CheckSaves: func(ctx context.Context, l cleanup.Listing, dryRun bool, logf func(string, ...any)) error {
			listed(l)
			return m.CheckSaves(ctx, l, dryRun, logf)
		},
	}
	if sweep {
		h.TrimTimeout = trimTimeout
	}
	return h.CleanWithListing(ctx, listingCtx, now, dryRun, logf)
}

// linkLeftovers is what cleanup may prune once E2B has been listed: the names in the activity
// clock's directory and, with links on, the machines links.json has entries for, and the
// fences there that have been up for links.FenceStale or longer.
type linkLeftovers struct {
	activity []string
	entries  []string
	stale    []staleFence
	err      error
}

type staleFence struct {
	id string
	m  links.Machine
}

func (a *App) readLinkLeftovers(cfg config, now time.Time) linkLeftovers {
	var left linkLeftovers
	var errs []error
	if names, err := readActivityNames(cfg.stateDir); err != nil {
		errs = append(errs, fmt.Errorf("reading the activity clock: %w", err))
	} else {
		left.activity = names
	}
	if cfg.linksConfiguredOn() {
		doc, err := a.linkPartsFor(cfg).store.Read()
		if err != nil {
			errs = append(errs, fmt.Errorf("reading the link state: %w", err))
		}
		for id, m := range doc.Machines {
			left.entries = append(left.entries, id)
			if m.Stale(now) {
				left.stale = append(left.stale, staleFence{id: id, m: *m})
			}
		}
		slices.Sort(left.entries)
		slices.SortFunc(left.stale, func(x, y staleFence) int { return strings.Compare(x.id, y.id) })
	}
	left.err = errors.Join(errs...)
	return left
}

func readActivityNames(stateDir string) ([]string, error) {
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(filepath.FromSlash(links.ActivityDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// pruneLinkLeftovers removes the activity files and, with links on, the link entries of machines
// E2B has forgotten, and the temporary files a crashed activity update left. Removing one an
// update is still using only loses that update, which errs toward less activity. With links on
// it also settles the stale fences. It asks E2B about each machine once.
func (a *App) pruneLinkLeftovers(ctx context.Context, cfg config, e engine.Engine, left linkLeftovers, listed map[string]bool, dryRun bool, logf func(format string, args ...any)) error {
	type got struct {
		m   engine.Machine
		err error
	}
	asked := map[string]got{}
	get := func(id string) (engine.Machine, error) {
		g, ok := asked[id]
		if !ok {
			g.m, g.err = e.Get(ctx, id)
			asked[id] = g
		}
		return g.m, g.err
	}
	forgotten, err := confirmForgotten(get, left, listed)
	errs := []error{left.err, err}
	for _, f := range left.stale {
		errs = append(errs, a.reconcileFence(ctx, cfg, f, get, dryRun, logf))
	}
	if len(left.activity) > 0 {
		errs = append(errs, pruneActivity(cfg.stateDir, left.activity, forgotten, dryRun, logf))
	}
	var gone []string
	for _, id := range left.entries {
		if forgotten[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		errs = append(errs, a.dropLinkEntries(ctx, cfg, gone, dryRun, logf))
	}
	return errors.Join(errs...)
}

// confirmForgotten asks E2B, through get, for each machine in left that the listing missed, and
// reports the ones it has forgotten. Missing from one listing isn't enough: the listing reads
// running and paused machines page by page, and a machine E2B pauses meanwhile can fall between
// them. A machine E2B still has, in any state, is kept, and so is one it can't be asked about,
// with an error.
func confirmForgotten(get func(id string) (engine.Machine, error), left linkLeftovers, listed map[string]bool) (map[string]bool, error) {
	entries, clocks := map[string]bool{}, map[string]bool{}
	var candidates []string
	for _, id := range left.entries {
		if !listed[id] {
			entries[id] = true
			candidates = append(candidates, id)
		}
	}
	for _, name := range left.activity {
		if !strings.HasPrefix(name, ".") && !listed[name] {
			clocks[name] = true
			candidates = append(candidates, name)
		}
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)
	forgotten := map[string]bool{}
	var errs []error
	for _, id := range candidates {
		_, err := get(id)
		switch {
		case err == nil:
		case errors.Is(err, engine.ErrNotFound):
			forgotten[id] = true
		default:
			what := "the activity clock"
			switch {
			case entries[id] && clocks[id]:
				what = "the link entry and activity clock"
			case entries[id]:
				what = "the link entry"
			}
			errs = append(errs, fmt.Errorf("kept %s of %s: couldn't confirm E2B has forgotten it: %w", what, id, err))
		}
	}
	return forgotten, errors.Join(errs...)
}

// reconcileFence settles a fence that has been up for links.FenceStale or longer. Age alone
// never clears one: a save may run for ten minutes. A fence whose owner is still running is only
// logged. One whose owner is gone is cleared once E2B reports the machine's state, whatever it
// is, by taking the fence over and releasing it, so the store clears it as it clears its own. A
// machine E2B has forgotten is left to the prune, which drops its whole entry.
func (a *App) reconcileFence(ctx context.Context, cfg config, f staleFence, get func(id string) (engine.Machine, error), dryRun bool, logf func(format string, args ...any)) error {
	fence := *f.m.Fence
	since, why := fence.Since.UTC().Format(time.RFC3339), termsafe.Line(fence.Why)
	state, err := f.m.Fenced(func(owner string) (bool, error) { return statefile.Held(cfg.stateDir, owner) })
	switch {
	case err != nil:
		return fmt.Errorf("kept the fence on %s: couldn't check whether its owner is still running: %w", f.id, err)
	case state == links.FencedLive:
		logf("the fence on %s has been up since %s (%s), and the operation that raised it is still running", f.id, since, why)
		return nil
	case state != links.FencedOrphaned:
		return nil
	}
	m, err := get(f.id)
	switch {
	case errors.Is(err, engine.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("kept the fence on %s: couldn't read its state from E2B: %w", f.id, err)
	}
	detail := fmt.Sprintf("left since %s by %s, whose operation is gone: E2B reports it %s", since, why, m.State)
	if dryRun {
		logf("would clear the fence on %s, %s", f.id, detail)
		return nil
	}
	store := a.linkPartsFor(cfg).store
	fctx, cancel := context.WithTimeout(ctx, fenceClearTimeout)
	release, err := store.Fence(fctx, f.id, "clearing a fence its owner left")
	timedOut := errors.Is(fctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	if err != nil {
		if release != nil {
			release()
		}
		if timedOut {
			logf("left the fence on %s: an operation is fencing it now", f.id)
			return nil
		}
		return fmt.Errorf("kept the fence on %s: couldn't take it over to clear it: %w", f.id, err)
	}
	release()
	doc, err := store.Read()
	if err != nil {
		return fmt.Errorf("cleared the fence on %s, but couldn't read the link state back: %w", f.id, err)
	}
	if after := doc.Machines[f.id]; after != nil && after.Fence != nil {
		if after.Fence.Since.Equal(fence.Since) {
			return fmt.Errorf("the fence on %s is still up after clearing it", f.id)
		}
		return nil
	}
	logf("cleared the fence on %s, %s", f.id, detail)
	return nil
}

// dropLinkEntries drops the link entries of the machines in gone that still have one. Their
// addresses stay spent.
func (a *App) dropLinkEntries(ctx context.Context, cfg config, gone []string, dryRun bool, logf func(format string, args ...any)) error {
	if dryRun {
		for _, id := range gone {
			logf("would drop the link entry of %s, which E2B has forgotten", id)
		}
		return nil
	}
	var dropped []string
	unchanged := errors.New("no entries to drop")
	err := a.linkPartsFor(cfg).store.Update(ctx, func(d *links.Doc) error {
		dropped = dropped[:0]
		for _, id := range gone {
			if _, ok := d.Machines[id]; ok {
				delete(d.Machines, id)
				dropped = append(dropped, id)
			}
		}
		if len(dropped) == 0 {
			return unchanged
		}
		return nil
	})
	switch {
	case errors.Is(err, unchanged):
		return nil
	case err != nil:
		return fmt.Errorf("dropping the link entries of machines E2B has forgotten: %w", err)
	}
	for _, id := range dropped {
		logf("dropped the link entry of %s, which E2B has forgotten", id)
	}
	return nil
}

// pruneActivity removes the activity files in names whose machines E2B has forgotten, and the
// temporary files whose names start with a dot. It goes through the state directory as a root,
// so a symlink planted in it can't point the removals anywhere else.
func pruneActivity(stateDir string, names []string, forgotten map[string]bool, dryRun bool, logf func(format string, args ...any)) error {
	state, err := os.OpenRoot(stateDir)
	if err != nil {
		return fmt.Errorf("pruning the activity clock: %w", err)
	}
	defer state.Close()
	dir, err := state.OpenRoot(filepath.FromSlash(links.ActivityDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pruning the activity clock: %w", err)
	}
	defer dir.Close()
	var errs []error
	for _, name := range names {
		temporary := strings.HasPrefix(name, ".")
		if !temporary && !forgotten[name] {
			continue
		}
		what := fmt.Sprintf("the activity clock of %s, which E2B has forgotten", name)
		if temporary {
			what = fmt.Sprintf("%s, left by an activity update that didn't finish", name)
		}
		if dryRun {
			logf("would remove %s", what)
			continue
		}
		if err := dir.Remove(name); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			errs = append(errs, fmt.Errorf("pruning the activity clock: %w", err))
			continue
		}
		logf("removed %s", what)
	}
	return errors.Join(errs...)
}

const (
	sweepTimeout = 30 * time.Second
	trimTimeout  = 2 * time.Minute
	// fenceClearTimeout bounds cleanup's wait for a machine's fence lock, so a live operation
	// that took it meanwhile, such as a ten-minute save, makes cleanup give up, not wait.
	fenceClearTimeout = 5 * time.Second
)

var errCleanupLocked = errors.New("another cleanup is running")

func (a *App) lockCleanupRead(ctx context.Context, m *machines.Manager, wait bool, logf func(string, ...any)) (func(), error) {
	waiting := false
	for {
		release, err := m.TryCleanupRead(ctx)
		if !errors.Is(err, machines.ErrBusy) || !wait {
			return release, err
		}
		if !waiting {
			logf("waiting for an image save or fork to finish")
			waiting = true
		}
		if err := a.waitCleanup(ctx); err != nil {
			return nil, err
		}
	}
}

func (a *App) waitCleanup(ctx context.Context) error {
	if a.WaitCleanup != nil {
		return a.WaitCleanup(ctx)
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *App) lockCleanup(ctx context.Context, dir string, wait bool, logf func(string, ...any)) (func(), error) {
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, ok, err := tryLock(dir, "cleanup.lock", "cleanup")
		if err != nil || ok {
			return unlock, err
		}
		if !wait {
			return nil, errCleanupLocked
		}
		if !waiting {
			logf("waiting for another cleanup")
			waiting = true
		}
		if err := a.waitCleanup(ctx); err != nil {
			return nil, err
		}
	}
}

type sweeper struct {
	engine  engine.Engine
	clean   func(ctx, listingCtx context.Context, now time.Time) error
	clock   func() time.Time
	logf    func(format string, args ...any)
	seen    map[string]bool
	next    time.Time
	problem string
}

func (s *sweeper) look(ctx context.Context, now time.Time) {
	lctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	listed, err := s.engine.List(lctx, engine.ListOptions{})
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, m := range listed {
		seen[m.ID] = true
	}
	for id := range s.seen {
		if !seen[id] {
			s.next = now
		}
	}
	s.seen = seen
	if now.Before(s.next) {
		return
	}
	err = s.clean(ctx, lctx, now)
	retry := time.Hour
	if errors.Is(err, cleanup.ErrBusy) || errors.Is(err, machines.ErrBusy) || errors.Is(err, machines.ErrSaveUnsettled) || errors.Is(err, errCleanupLocked) || errors.Is(err, cleanup.ErrTrimTimeout) {
		retry = time.Minute
	}
	completed := now
	if s.clock != nil {
		if at := s.clock(); at.After(completed) {
			completed = at
		}
	}
	s.next = completed.Add(retry)
	if ctx.Err() != nil {
		return
	}
	problem := ""
	if err != nil {
		problem = fmt.Sprintf("%v; trying again in %s", err, shortDuration(retry))
	}
	if problem != "" && problem != s.problem {
		s.logf("%s", problem)
	}
	s.problem = problem
}

func (a *App) daemon(ctx context.Context, args []string) int {
	fl := a.flags("daemon")
	every := fl.Duration("every", 15*time.Second, "how often to look at the machines")
	if _, ok := a.parse(fl, args, 0, "daemon [-every 15s]"); !ok {
		return 2
	}
	if *every <= 0 {
		fmt.Fprintf(a.Stderr, "hibd daemon: -every must be positive, not %v\n", *every)
		return 2
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(false, "daemon", err)
	}
	e, err := a.engine()
	if err != nil {
		return a.fail(false, "daemon", err)
	}
	logger := log.New(a.Stderr, "", 0)
	w, err := busy.New(busy.Config{
		Engine: e, CPU: busy.CgroupCPU(cfg.cgroups), Agents: cfg.agents, Logf: logger.Printf,
		Activity: links.NewClock(cfg.stateDir).Touch, StateDir: cfg.stateDir,
	})
	if err != nil {
		return a.fail(false, "daemon", err)
	}
	clock := a.Now
	if clock == nil {
		clock = time.Now
	}
	s := &sweeper{engine: e, clock: clock, logf: func(format string, args ...any) {
		logger.Printf("cleanup: %s", termsafe.String(fmt.Sprintf(format, args...)))
	}}
	m := machines.New(machines.Config{Engine: e, StateDir: cfg.stateDir})
	s.clean = func(ctx, listingCtx context.Context, now time.Time) error {
		if err := m.SweepForks(listingCtx); err != nil {
			return err
		}
		return a.clean(ctx, listingCtx, cfg, e, now, false, true, s.logf)
	}
	store := a.codexStore(cfg, func(format string, args ...any) { logger.Printf("codex: "+format, args...) })
	delivery := newSetupDelivery(e, cfg, store, func(format string, args ...any) { logger.Printf("setup: "+format, args...) })
	removeLeftoverCodexLogins(cfg.stateDir, logger.Printf)
	logger.Printf("hibd %s: keeping busy machines running, looking every %v", version.Version, *every)
	switch changed, err := rebindGitHub(cfg); {
	case err != nil:
		logger.Printf("github: binding secret github to the Copilot CLI's sites failed: %v. Run hib connect github again", err)
	case changed:
		logger.Printf("github: bound secret github to %s, so the Copilot CLI works too", strings.Join(github.Sites(), ", "))
	}
	ticks := a.DaemonTicks
	if ticks == nil {
		t := time.NewTicker(*every)
		defer t.Stop()
		ticks = t.C
	}
	at := time.Now()
	if a.Now != nil {
		at = a.Now()
	}
	sweeping := make(chan struct{}, 1)
	var wg sync.WaitGroup
	refresher := &codexRefresher{store: a.codexStore(cfg, nil), logf: func(format string, args ...any) { logger.Printf("codex: "+format, args...) }}
	wg.Go(func() { refresher.run(ctx, cmp.Or(a.codexEvery, time.Minute), a.awake) })
	problem := ""
	for {
		now := ""
		switch err := a.awake(); {
		case errors.Is(err, errQuiescing):
			now = "the host is going to sleep: leaving machines alone"
		case err != nil:
			now = err.Error()
		default:
			select {
			case sweeping <- struct{}{}:
				sweepAt := at
				wg.Go(func() {
					s.look(ctx, sweepAt)
					<-sweeping
				})
			default:
			}
			if err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				now = err.Error()
			}
			delivery.look(ctx)
		}
		if now != problem {
			if now == "" {
				logger.Printf("looking at machines again")
			} else {
				logger.Printf("%s", now)
			}
			problem = now
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			logger.Printf("stopping")
			return 0
		case at = <-ticks:
		}
	}
}

func (a *App) version(args []string) int {
	fl := a.flags("version")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "version"); !ok {
		return 2
	}
	if *asJSON {
		return a.reply(hibapi.Response{Version: version.Version, Features: []string{hibapi.FeatureBuildEvents, hibapi.FeatureDisconnect, hibapi.FeaturePorts}})
	}
	fmt.Fprintln(a.Stdout, "hibd", version.Version)
	return 0
}

func (a *App) flags(cmd string) *flag.FlagSet {
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(a.Stderr)
	return fl
}

func (a *App) parse(fl *flag.FlagSet, args []string, n int, use string) ([]string, bool) {
	rest, ok := a.parseAny(fl, args)
	if !ok {
		return nil, false
	}
	if len(rest) != n {
		fmt.Fprintf(a.Stderr, "usage: hibd %s\n", use)
		return nil, false
	}
	return rest, true
}

func (a *App) parseAny(fl *flag.FlagSet, args []string) ([]string, bool) {
	var rest []string
	for {
		if err := fl.Parse(args); err != nil {
			return nil, false
		}
		if fl.NArg() == 0 {
			break
		}
		rest = append(rest, fl.Arg(0))
		args = fl.Args()[1:]
	}
	return rest, true
}

func (a *App) fail(asJSON bool, cmd string, err error) int {
	err = withNextStep(cmd, err)
	msg := termsafe.String(err.Error())
	if asJSON {
		a.reply(hibapi.Response{Error: msg})
	} else {
		fmt.Fprintf(a.Stderr, "hibd %s: %s\n", cmd, msg)
	}
	return 1
}

func (a *App) reply(r hibapi.Response) int {
	data, err := marshalReply(r)
	return a.writeReply(data, err, r.Error != "")
}

func (a *App) writeReply(data []byte, err error, failed bool) int {
	if err == nil {
		_, err = a.Stdout.Write(data)
	}
	if err != nil {
		fmt.Fprintln(a.Stderr, "hibd:", termsafe.String(err.Error()))
		return 1
	}
	if failed {
		return 1
	}
	return 0
}

var errQuiescing = errors.New("the host is going to sleep: try again in a few minutes, and hib will wake it")

func (a *App) awake() error {
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return err
	}
	_, err = os.Stat(cfg.quiescing)
	switch {
	case err == nil:
		return errQuiescing
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return err
}

func (a *App) manager() (*machines.Manager, error) {
	return a.managerWithSetup(machines.Setup{})
}

func (a *App) managerWithSetup(setup machines.Setup) (*machines.Manager, error) {
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return nil, err
	}
	e, err := a.engine()
	if err != nil {
		return nil, err
	}
	return machines.New(machines.Config{
		Engine: e, StateDir: cfg.stateDir, AuthorizedKeys: cfg.authorizedKeys,
		ReadRoom: a.ReadRoom, Setup: setup, Awake: a.awake,
		Durable: func(ctx context.Context, id string) error {
			return a.durableSnapshot(ctx, cfg, id)
		},
		Fenced: a.snapshotFence(cfg),
	}), nil
}

func (a *App) engine() (engine.Engine, error) {
	if a.NewEngine != nil {
		return a.NewEngine()
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return nil, err
	}
	return e2b.New(cfg.e2b)
}

type config struct {
	e2b            e2b.Config
	healthURLs     []string
	stateDir       string
	githubProfile  string
	vaultPath      string
	vaultKeyPath   string
	e2bStorage     string
	trimScope      string
	auditLogPath   string
	authorizedKeys string
	quiescing      string
	agents         []string
	cgroups        string
	caCert         string
	caKey          string
	proxyPort      string
	// linkPort is HIBD_LINK_PORT. It defaults to 0, which means links between machines are
	// off. It stays 0 until the slice that wires the two listeners together turns it on.
	linkPort int
	// linkLog is HIBD_LINK_LOG, the link log's path.
	linkLog string
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg, err := loadSettings(getenv)
	if err != nil {
		return config{}, err
	}
	if cfg.e2b.APIKey == "" {
		path := getenv("HIBD_CONFIG")
		if path == "" {
			path = defaultConfig
		}
		return config{}, fmt.Errorf("E2B_API_KEY is not set (in the environment or %s)", path)
	}
	return cfg, nil
}

func loadSettings(getenv func(string) string) (config, error) {
	path := getenv("HIBD_CONFIG")
	if path == "" {
		path = defaultConfig
	}
	file := map[string]string{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		file, err = parseEnv(data)
		if err != nil {
			return config{}, fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return config{}, err
	}
	get := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		if v := file[key]; v != "" {
			return v
		}
		return def
	}
	keys := ""
	if home := getenv("HOME"); home != "" {
		keys = filepath.Join(home, ".ssh", "authorized_keys")
	}
	linkPort, err := parseLinkPort(get("HIBD_LINK_PORT", "0"))
	if err != nil {
		return config{}, err
	}
	cfg := config{
		e2b: e2b.Config{
			APIURL:     get("E2B_API_URL", "http://127.0.0.1:3000"),
			SandboxURL: get("E2B_SANDBOX_URL", "http://127.0.0.1:3002"),
			APIKey:     get("E2B_API_KEY", ""),
		},
		healthURLs:     strings.Fields(get("E2B_HEALTH_URLS", "http://127.0.0.1:3000/health http://127.0.0.1:5008/health http://127.0.0.1:3003/health")),
		stateDir:       get("HIBD_STATE_DIR", "/var/lib/hibernaut"),
		vaultPath:      get("HIBD_VAULT", ""),
		vaultKeyPath:   get("HIBD_VAULT_KEY", "/etc/hibernaut/vault.key"),
		e2bStorage:     get("HIBD_E2B_STORAGE", cleanup.StorageDir),
		trimScope:      get("HIBD_TRIM", "e2e"),
		auditLogPath:   get("HIBD_AUDIT_LOG", "/var/log/hibernaut/audit.jsonl"),
		authorizedKeys: get("HIBD_AUTHORIZED_KEYS", keys),
		quiescing:      get("HIBD_QUIESCING", "/run/hibernaut/quiescing"),
		agents:         strings.Fields(get("HIBD_AGENTS", "")),
		cgroups:        get("HIBD_CGROUPS", "/sys/fs/cgroup/e2b"),
		caCert:         get("HIBD_CA_CERT", "/etc/hibernaut/ca.crt"),
		caKey:          get("HIBD_CA_KEY", "/etc/hibernaut/ca.key"),
		proxyPort:      get("HIBD_PROXY_PORT", "5443"),
		linkPort:       linkPort,
		linkLog:        get("HIBD_LINK_LOG", links.LogPath),
	}
	if cfg.vaultPath == "" {
		cfg.vaultPath = filepath.Join(cfg.stateDir, "vault.json")
	}
	cfg.githubProfile = filepath.Join(cfg.stateDir, github.ProfileFile)
	return cfg, nil
}

// parseEnv parses KEY=VALUE lines as systemd's EnvironmentFile does, for the subset we write:
// blank lines, # comments, and values optionally wrapped in double quotes.
func parseEnv(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}
