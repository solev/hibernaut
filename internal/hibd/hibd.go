// Package hibd is the host daemon's command line: the commands hib runs over SSH, and that
// systemd and idle-deallocate run (spec D1). Commands that hib parses print JSON with --json.
package hibd

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
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
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/pauseall"
	proxypkg "github.com/solev/hibernaut/internal/proxy"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/version"
)

const defaultConfig = "/etc/hibernaut/hibd.env"

const usage = `usage: hibd <command> [flags]

commands:
  new NAME          make a machine (-image, default ` + images.Default + `; -idle, default 15m)
  ls                list machines (-state running|paused, -q for IDs only)
  rm NAME           delete a machine for good
  pause NAME        pause a machine
  fork MACHINE      copy a machine, as root (-count, default 1, most 10)
  bridge NAME       connect stdin and stdout to a machine's SSH server
  secret add NAME   store a bound secret from stdin (-env VAR -bind SITE[,SITE...])
  secret ls         list bound secrets
  secret rm NAME    remove a bound secret
  log               show secret swap audit entries (-n, default 50)
  connect github    store a GitHub token as secret github, after GitHub accepts it, and give
                    machines git's identity, as root. -source gh|token reads the token from
                    stdin. -source device gets one with GitHub's device flow, and stops when
                    stdin ends
  connect codex     log in to Codex on a ChatGPT plan with a device code, keep the login, and
                    give machines a login file of placeholders, as root
  codex refresh     refresh the Codex login once half its token's lifetime has passed, as root
                    (-force refreshes now, even when a refresh is stuck)
  status            check E2B, every secret and the Codex login, as root
  ca init           create the Hibernaut CA once, as root
  proxy             run the egress proxy, as root (-unhook removes its iptables rule)
  image build NAME  build an image (-force rebuilds every step)
  image save MACHINE NAME
                    save a machine as an image, memory included, as root
  image ls          list built-in and saved images
  image rm NAME     remove a saved image not used by any machine
  ready             wait until E2B answers (-timeout)
  pause-all         pause every running machine; wait until none is running
  leakscan          scan a paused machine or image for real secrets, as root. Running machines
                    are paused first and stay paused (-image NAME, -canary, -json)
  cleanup           delete the build directories nothing needs any more, as root (-n only
                    says which, and why it keeps the others)
  daemon            keep busy machines running (-every), clean up when a machine is
                    removed and hourly, and keep the Codex login fresh; systemd runs it as
                    root
  version           print the version

new, ls, rm, pause, fork, image build|save|ls|rm, connect github, connect codex, codex refresh,
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
	// Clean deletes the build directories nothing needs any more, like cleanup.Host.Clean. If
	// it's nil, App cleans up this host's.
	Clean func(ctx context.Context, e engine.Engine, now time.Time, dryRun bool, logf func(format string, args ...any)) error
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
	// Now supplies the daemon's clock. If it's nil, App uses time.Now.
	Now func() time.Time
	// DaemonTicks supplies the daemon's ticks. If it's nil, App uses a ticker at -every.
	DaemonTicks <-chan time.Time

	codexEvery time.Duration
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
	case "rm":
		return a.byName(ctx, "rm", args, (*machines.Manager).Remove)
	case "pause":
		return a.byName(ctx, "pause", args, (*machines.Manager).Pause)
	case "fork":
		return a.fork(ctx, args)
	case "bridge":
		return a.bridge(ctx, args)
	case "secret":
		return a.secret(ctx, args)
	case "connect":
		return a.connect(ctx, args)
	case "codex":
		return a.runCodex(ctx, args)
	case "status":
		return a.status(ctx, args)
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
	idle := fl.Duration("idle", machines.DefaultIdle, "pause the machine once it has been idle this long; 0 means never")
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "new NAME")
	if !ok {
		return 2
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
	mach, err := m.Create(ctx, names[0], *image, *idle)
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
	fmt.Fprintln(tw, "NAME\tID\tSTATE\tIMAGE\tIDLE\tSTARTED\tENDS")
	for _, m := range ms {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", orDash(m.Name), m.ID, m.State, orDash(m.Image),
			hibapi.FormatIdle(m.IdleSeconds), m.StartedAt.UTC().Format(time.RFC3339), m.EndAt.UTC().Format(time.RFC3339))
	}
	tw.Flush()
	return 0
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
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args[1:], 1, "image build NAME")
	if !ok {
		return 2
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "image build", err)
	}
	caPEM, err := os.ReadFile(cfg.caCert)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("no Hibernaut CA at %s: run `sudo hibd ca init` (vm/setup-host.sh does this)", cfg.caCert)
		}
		return a.fail(*asJSON, "image build", err)
	}
	spec, err := images.Spec(names[0], caPEM)
	if err != nil {
		return a.fail(*asJSON, "image build", err)
	}
	spec.Force = *force
	if err := a.awake(); err != nil {
		return a.fail(*asJSON, "image build", err)
	}
	e, err := a.engine()
	if err != nil {
		return a.fail(*asJSON, "image build", err)
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	start := time.Now()

	buildLog := termsafe.NewWriter(a.Stderr)
	id, err := e.BuildTemplate(ctx, spec, buildLog)
	buildLog.Flush()
	if err != nil {
		return a.fail(*asJSON, "image build", err)
	}
	fmt.Fprintf(a.Stderr, "built %s in %v\n", names[0], time.Since(start).Round(time.Second))
	if *asJSON {
		return a.reply(hibapi.Response{Template: id})
	}
	fmt.Fprintln(a.Stdout, id)
	return 0
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
	start := time.Now()
	paused, err := pauseall.Run(ctx, e, pauseall.Options{Logf: logger.Printf})
	if err != nil {
		logger.Printf("%v", err)
		return 1
	}
	logger.Printf("done: paused %d machine(s) in %v; none running", len(paused), time.Since(start).Round(time.Millisecond))
	return 0
}

func (a *App) cleanup(ctx context.Context, args []string) int {
	fl := a.flags("cleanup")
	dryRun := fl.Bool("n", false, "only say what would be deleted, and why the rest is kept")
	if _, ok := a.parse(fl, args, 0, "cleanup [-n]"); !ok {
		return 2
	}
	e, err := a.engine()
	if err != nil {
		return a.fail(false, "cleanup", err)
	}
	if err := a.clean(ctx, e, time.Now(), *dryRun, log.New(a.Stdout, "", 0).Printf); err != nil {
		return a.fail(false, "cleanup", err)
	}
	return 0
}

func (a *App) clean(ctx context.Context, e engine.Engine, now time.Time, dryRun bool, logf func(format string, args ...any)) error {
	if a.Clean != nil {
		return a.Clean(ctx, e, now, dryRun, logf)
	}
	h := cleanup.Host{Storage: cleanup.StorageDir, Cache: cleanup.CacheDir, Query: cleanup.Postgres, Engine: e}
	return h.Clean(ctx, now, dryRun, logf)
}

const sweepTimeout = 30 * time.Second

type sweeper struct {
	engine  engine.Engine
	clean   func(ctx context.Context, now time.Time) error
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
	err = s.clean(lctx, now)
	retry := time.Hour
	if errors.Is(err, cleanup.ErrBusy) || errors.Is(err, machines.ErrBusy) {
		retry = time.Minute
	}
	s.next = now.Add(retry)
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
	w, err := busy.New(busy.Config{Engine: e, CPU: busy.CgroupCPU(cfg.cgroups), Agents: cfg.agents, Logf: logger.Printf})
	if err != nil {
		return a.fail(false, "daemon", err)
	}
	s := &sweeper{engine: e, logf: func(format string, args ...any) {
		logger.Printf("cleanup: %s", termsafe.String(fmt.Sprintf(format, args...)))
	}}
	m := machines.New(machines.Config{Engine: e, StateDir: cfg.stateDir})
	s.clean = func(ctx context.Context, now time.Time) error {
		if err := m.SweepForks(ctx); err != nil {
			return err
		}
		return a.clean(ctx, e, now, false, s.logf)
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
		return a.reply(hibapi.Response{Version: version.Version})
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
	msg := termsafe.String(err.Error())
	if asJSON {
		a.reply(hibapi.Response{Error: msg})
	} else {
		fmt.Fprintf(a.Stderr, "hibd %s: %s\n", cmd, msg)
	}
	return 1
}

func (a *App) reply(r hibapi.Response) int {
	r.V = hibapi.Version
	if err := json.NewEncoder(a.Stdout).Encode(r); err != nil {
		fmt.Fprintln(a.Stderr, "hibd:", err)
		return 1
	}
	if r.Error != "" {
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
	auditLogPath   string
	authorizedKeys string
	quiescing      string
	agents         []string
	cgroups        string
	caCert         string
	caKey          string
	proxyPort      string
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
		auditLogPath:   get("HIBD_AUDIT_LOG", "/var/log/hibernaut/audit.jsonl"),
		authorizedKeys: get("HIBD_AUTHORIZED_KEYS", keys),
		quiescing:      get("HIBD_QUIESCING", "/run/hibernaut/quiescing"),
		agents:         strings.Fields(get("HIBD_AGENTS", "")),
		cgroups:        get("HIBD_CGROUPS", "/sys/fs/cgroup/e2b"),
		caCert:         get("HIBD_CA_CERT", "/etc/hibernaut/ca.crt"),
		caKey:          get("HIBD_CA_KEY", "/etc/hibernaut/ca.key"),
		proxyPort:      get("HIBD_PROXY_PORT", "5443"),
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
