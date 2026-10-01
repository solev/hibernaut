// Package hibd is the host daemon's command line: the commands hib runs over SSH, and that
// systemd and idle-deallocate run (spec D1). Commands that hib parses print JSON with --json.
package hibd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/pauseall"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/version"
)

const defaultConfig = "/etc/hibernaut/hibd.env"

const usage = `usage: hibd <command> [flags]

commands:
  new NAME          make a machine (-image, default ` + images.Default + `)
  ls                list machines (-state running|paused, -q for IDs only)
  rm NAME           delete a machine for good
  pause NAME        pause a machine
  bridge NAME       connect stdin and stdout to a machine's SSH server
  image build NAME  build an image (-force rebuilds every step)
  ready             wait until E2B answers (-timeout)
  pause-all         pause every running machine; wait until none is running
  version           print the version

new, ls, rm, pause, image build and version print JSON with -json.

hibd reads its settings from ` + defaultConfig + ` ($HIBD_CONFIG overrides the path).
Environment variables of the same names override the file:
  E2B_API_URL           default http://127.0.0.1:3000
  E2B_SANDBOX_URL       default http://127.0.0.1:3002
  E2B_API_KEY           required
  E2B_HEALTH_URLS       what ready waits for, space-separated
  HIBD_STATE_DIR        default /var/lib/hibernaut
  HIBD_AUTHORIZED_KEYS  keys that may log in to new machines, default ~/.ssh/authorized_keys
  HIBD_QUIESCING        new, bridge and image build refuse while it exists; default
                        /run/hibernaut/quiescing, which quiesce makes before pausing everything
`

const (
	createTimeout = 5 * time.Minute
	dialTimeout   = 3 * time.Minute
	buildTimeout  = time.Hour
)

// App runs hibd commands.
type App struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// NewEngine returns the engine to use. If it's nil, App uses E2B, as configured.
	NewEngine func() (engine.Engine, error)
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
	case "bridge":
		return a.bridge(ctx, args)
	case "image":
		return a.image(ctx, args)
	case "ready":
		return a.ready(ctx, args)
	case "pause-all":
		return a.pauseAll(ctx, args)
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
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "new NAME")
	if !ok {
		return 2
	}
	if err := a.awake(); err != nil {
		return a.fail(*asJSON, "new", err)
	}
	m, err := a.manager()
	if err != nil {
		return a.fail(*asJSON, "new", err)
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	mach, err := m.Create(ctx, names[0], *image)
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
		return a.reply(hibapi.Response{Machines: ms})
	case *quiet:
		for _, m := range ms {
			fmt.Fprintln(a.Stdout, m.ID)
		}
		return 0
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSTATE\tIMAGE\tSTARTED\tENDS")
	for _, m := range ms {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", orDash(m.Name), m.ID, m.State, orDash(m.Image),
			m.StartedAt.UTC().Format(time.RFC3339), m.EndAt.UTC().Format(time.RFC3339))
	}
	tw.Flush()
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// byName runs a command that takes a machine name and prints nothing.
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

// bridge connects stdin and stdout to a machine's SSH server, after a header line that tells
// hib the server's host key. It's what hib's ProxyCommand runs on the host.
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
	// Either side ending ends the bridge: SSH has nothing to say after a one-sided close.
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
	if len(args) == 0 || args[0] != "build" {
		fmt.Fprintln(a.Stderr, "usage: hibd image build NAME [-force] [-json]")
		return 2
	}
	fl := a.flags("image build")
	force := fl.Bool("force", false, "rebuild every step, not only those that changed")
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args[1:], 1, "image build NAME")
	if !ok {
		return 2
	}
	spec, err := images.Spec(names[0])
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
	// The log has the output of the build's commands.
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

// ready waits until each of E2B's services answers its health check.
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

// parse parses flags wherever they are among args, and wants exactly n other arguments.
func (a *App) parse(fl *flag.FlagSet, args []string, n int, use string) ([]string, bool) {
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
	if len(rest) != n {
		fmt.Fprintf(a.Stderr, "usage: hibd %s\n", use)
		return nil, false
	}
	return rest, true
}

// fail reports err, as JSON on stdout if asJSON, and returns exit code 1. Errors can quote
// what a machine said, so they're made safe for a terminal.
func (a *App) fail(asJSON bool, cmd string, err error) int {
	msg := termsafe.String(err.Error())
	if asJSON {
		a.reply(hibapi.Response{Error: msg})
	} else {
		fmt.Fprintf(a.Stderr, "hibd %s: %s\n", cmd, msg)
	}
	return 1
}

// reply prints r as JSON. It returns exit code 0, or 1 if r is an error.
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

// errQuiescing means the host is getting ready to sleep (vm/lib/quiesce). Machines mustn't start
// then: pause-all may have run already, and stopping E2B would lose them.
var errQuiescing = errors.New("the host is going to sleep: try again in a few minutes, and hib will wake it")

// awake returns errQuiescing while the host gets ready to sleep.
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
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return nil, err
	}
	e, err := a.engine()
	if err != nil {
		return nil, err
	}
	return machines.New(machines.Config{Engine: e, StateDir: cfg.stateDir, AuthorizedKeys: cfg.authorizedKeys}), nil
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
	authorizedKeys string
	quiescing      string
}

// loadConfig reads the env file, then lets environment variables override it.
func loadConfig(getenv func(string) string) (config, error) {
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
		authorizedKeys: get("HIBD_AUTHORIZED_KEYS", keys),
		quiescing:      get("HIBD_QUIESCING", "/run/hibernaut/quiescing"),
	}
	if cfg.e2b.APIKey == "" {
		return config{}, fmt.Errorf("E2B_API_KEY is not set (in the environment or %s)", path)
	}
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
