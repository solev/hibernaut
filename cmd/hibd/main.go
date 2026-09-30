// Command hibd is Hibernaut's host daemon. In M0 it's the subcommands that systemd and
// idle-deallocate run; hib will call it over SSH too (spec D1).
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
	"github.com/solev/hibernaut/internal/pauseall"
	"github.com/solev/hibernaut/internal/version"
)

const defaultConfig = "/etc/hibernaut/hibd.env"

const usage = `usage: hibd <command> [flags]

commands:
  pause-all   pause every running machine; wait until none is running
  ls          list machines
  version     print the version

hibd reads E2B's address and API key from ` + defaultConfig + ` ($HIBD_CONFIG
overrides the path). Environment variables of the same names override the file:
  E2B_API_URL      default http://127.0.0.1:3000
  E2B_SANDBOX_URL  default http://127.0.0.1:3002
  E2B_API_KEY      required
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	a.newEngine = a.e2bEngine
	os.Exit(a.run(ctx, os.Args[1:]))
}

type app struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	newEngine      func() (engine.Engine, error)
}

func (a *app) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "pause-all":
		return a.pauseAll(ctx, args)
	case "ls":
		return a.ls(ctx, args)
	case "version", "-v", "--version":
		fmt.Fprintln(a.stdout, "hibd", version.Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(a.stdout, usage)
		return 0
	}
	fmt.Fprintf(a.stderr, "hibd: unknown command %q\n\n%s", cmd, usage)
	return 2
}

func (a *app) pauseAll(ctx context.Context, args []string) int {
	fl := flag.NewFlagSet("pause-all", flag.ContinueOnError)
	fl.SetOutput(a.stderr)
	timeout := fl.Duration("timeout", 4*time.Minute, "give up after this long")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	e, err := a.newEngine()
	if err != nil {
		fmt.Fprintln(a.stderr, "hibd:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	logger := log.New(a.stderr, "pause-all: ", 0)
	start := time.Now()
	paused, err := pauseall.Run(ctx, e, pauseall.Options{Logf: logger.Printf})
	if err != nil {
		logger.Printf("%v", err)
		return 1
	}
	logger.Printf("done: paused %d machine(s) in %v; none running", len(paused), time.Since(start).Round(time.Millisecond))
	return 0
}

func (a *app) ls(ctx context.Context, args []string) int {
	fl := flag.NewFlagSet("ls", flag.ContinueOnError)
	fl.SetOutput(a.stderr)
	state := fl.String("state", "", "only machines in this state: running or paused")
	quiet := fl.Bool("q", false, "print only IDs")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	var opts engine.ListOptions
	switch engine.State(*state) {
	case "":
	case engine.Running, engine.Paused:
		opts.States = []engine.State{engine.State(*state)}
	default:
		fmt.Fprintf(a.stderr, "hibd ls: -state must be running or paused, not %q\n", *state)
		return 2
	}
	e, err := a.newEngine()
	if err != nil {
		fmt.Fprintln(a.stderr, "hibd:", err)
		return 1
	}
	ms, err := e.List(ctx, opts)
	if err != nil {
		fmt.Fprintln(a.stderr, "hibd ls:", err)
		return 1
	}
	if *quiet {
		for _, m := range ms {
			fmt.Fprintln(a.stdout, m.ID)
		}
		return 0
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tTEMPLATE\tSTARTED\tENDS")
	for _, m := range ms {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.ID, m.State, m.Template,
			m.StartedAt.UTC().Format(time.RFC3339), m.EndAt.UTC().Format(time.RFC3339))
	}
	tw.Flush()
	return 0
}

func (a *app) e2bEngine() (engine.Engine, error) {
	cfg, err := loadConfig(a.getenv)
	if err != nil {
		return nil, err
	}
	return e2b.New(cfg)
}

// loadConfig reads the env file, then lets environment variables override it.
func loadConfig(getenv func(string) string) (e2b.Config, error) {
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
			return e2b.Config{}, fmt.Errorf("%s: %w", path, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return e2b.Config{}, err
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
	cfg := e2b.Config{
		APIURL:     get("E2B_API_URL", "http://127.0.0.1:3000"),
		SandboxURL: get("E2B_SANDBOX_URL", "http://127.0.0.1:3002"),
		APIKey:     get("E2B_API_KEY", ""),
	}
	if cfg.APIKey == "" {
		return e2b.Config{}, fmt.Errorf("E2B_API_KEY is not set (in the environment or %s)", path)
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
