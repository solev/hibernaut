// Command hib is Hibernaut's laptop CLI. It runs hibd on the host over SSH, and it's the
// ProxyCommand that ssh NAME.hib goes through (spec §5, SSH path).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/version"
)

const usage = `usage: hib [-config DIR] <command> [flags]

commands:
  init              set this laptop up (-host USER@HOST; see hib init -h)
  new NAME          make a machine (-image, default ` + images.Default + `; -idle, default 15m); then: ssh NAME.hib
  ls                list machines, and when each pauses unless it's busy
  rm NAME           delete a machine for good
  pause NAME        pause a machine; connecting wakes it
  connect github    give machines your GitHub login (--from-gh, or --token on stdin)
  status            show the host's, E2B's and each secret's health; never wakes the host
  secret add NAME   store a bound secret (-env VAR -bind SITE[,SITE...])
  secret ls         list bound secrets
  secret rm NAME    remove a bound secret
  log               show secret swap audit entries (-n, default 50)
  exec NAME -- CMD  run CMD on a machine, as ssh NAME.hib -- CMD does
  image build NAME  build an image on the host (-force rebuilds every step)
  bridge NAME.hib   connect to a machine's SSH server; ssh runs this for you
  version           print the version

-config defaults to ~/.config/hibernaut.
`

const (
	shortTimeout = 2 * time.Minute
	newTimeout   = 6 * time.Minute
	buildTimeout = 65 * time.Minute
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	a := &app{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		getenv:     os.Getenv,
		ssh:        runCommand("ssh"),
		gh:         runCommand("gh"),
		execSSH:    execSSH,
		az:         runAz,
		executable: os.Executable,
		wakePoll:   5 * time.Second,
		now:        time.Now,
	}
	os.Exit(a.run(ctx, os.Args[1:]))
}

// runFunc runs a program with args and returns its exit code. err is for when it couldn't run
// or was stopped.
type runFunc func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, err error)

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	ssh            runFunc
	gh             runFunc
	// execSSH replaces hib with ssh.
	execSSH    func(args []string) error
	az         func(ctx context.Context, args ...string) ([]byte, error)
	executable func() (string, error)
	wakePoll   time.Duration
	now        func() time.Time
}

func (a *app) run(ctx context.Context, args []string) int {
	fl := a.flags("hib")
	dir := fl.String("config", "", "settings directory (default ~/.config/hibernaut)")
	fl.Usage = func() { fmt.Fprint(a.stderr, usage) }
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fl.NArg() == 0 {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	cmd, args := fl.Arg(0), fl.Args()[1:]
	switch cmd {
	case "version":
		fmt.Fprintln(a.stdout, "hib", version.Version)
		return 0
	case "help":
		fmt.Fprint(a.stdout, usage)
		return 0
	case "init", "new", "ls", "rm", "pause", "exec", "image", "bridge", "secret", "log", "connect", "status":
	default:
		fmt.Fprintf(a.stderr, "hib: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	cfgDir, err := a.configDir(*dir)
	if err != nil {
		return a.fail(err)
	}
	if cmd == "init" {
		return a.init(ctx, cfgDir, args)
	}
	cfg, err := loadConfig(cfgDir)
	if err != nil {
		return a.fail(err)
	}
	switch cmd {
	case "new":
		return a.newMachine(ctx, cfg, args)
	case "ls":
		return a.ls(ctx, cfg, args)
	case "rm":
		return a.byName(ctx, cfg, "rm", "removed", args)
	case "pause":
		return a.byName(ctx, cfg, "pause", "paused", args)
	case "secret":
		return a.secret(ctx, cfg, args)
	case "connect":
		return a.connect(ctx, cfg, args)
	case "status":
		return a.status(ctx, cfg, args)
	case "log":
		return a.log(ctx, cfg, args)
	case "exec":
		return a.exec(cfg, args)
	case "image":
		return a.image(ctx, cfg, args)
	}
	return a.bridgeCmd(ctx, cfg, args)
}

func (a *app) newMachine(ctx context.Context, cfg *config, args []string) int {
	fl := a.flags("new")
	image := fl.String("image", images.Default, "the image to make the machine from")
	idle := fl.Duration("idle", 0, "pause the machine once it has been idle this long, e.g. 1h (default 15m; 0 means never)")
	names, ok := a.parse(fl, args, 1, "new NAME [-image IMAGE] [-idle 15m]")
	if !ok || !a.checkNames(names[0], *image) {
		return 2
	}
	words := []string{"new", names[0], "--image", *image}
	// hibd has the default and checks the idle time, so only pass one that was asked for.
	fl.Visit(func(f *flag.Flag) {
		if f.Name == "idle" {
			words = append(words, "--idle", idle.String())
		}
	})
	r, err := a.remote(ctx, cfg, newTimeout, append(words, "--json")...)
	if err != nil {
		return a.fail(err)
	}
	if r.Machine == nil {
		return a.fail(errors.New("hibd didn't say which machine it made"))
	}
	fmt.Fprintf(a.stdout, "made %s; connect with: ssh %s.hib\n", names[0], names[0])
	return 0
}

func (a *app) ls(ctx context.Context, cfg *config, args []string) int {
	if _, ok := a.parse(a.flags("ls"), args, 0, "ls"); !ok {
		return 2
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "ls", "--json")
	if err != nil {
		return a.fail(err)
	}
	slices.SortFunc(r.Machines, func(x, y hibapi.Machine) int {
		return strings.Compare(x.Name+"\x00"+x.ID, y.Name+"\x00"+y.ID)
	})
	now := a.now()
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tIMAGE\tIDLE\tPAUSES IN\tSTARTED\tID")
	for _, m := range r.Machines {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", safe(m.Name), safe(m.State), safe(m.Image),
			hibapi.FormatIdle(m.IdleSeconds), pausesIn(m, now), m.StartedAt.Local().Format("2006-01-02 15:04"), safe(m.ID))
	}
	tw.Flush()
	if r.Room != nil {
		fmt.Fprintf(a.stdout, "\n%s\n", roomLine(*r.Room))
	}
	return 0
}

// roomLine says how many more machines fit on the host, and how much memory and disk is free.
func roomLine(r hibapi.Room) string {
	free := fmt.Sprintf("%s of %s memory free, %s disk free.", hibapi.FormatMB(r.MemoryFreeMB), hibapi.FormatMB(r.MemoryMB),
		hibapi.FormatMB(r.DiskFreeMB))
	image := safe(r.Image)
	switch {
	case r.Fits <= 0:
		return fmt.Sprintf("No room for another %s machine: %s hib pause NAME frees memory, hib rm NAME frees disk.", image, free)
	case r.Fits == 1:
		return fmt.Sprintf("Room for 1 more %s machine: %s", image, free)
	}
	return fmt.Sprintf("Room for %d more %s machines: %s", r.Fits, image, free)
}

// pausesIn says how long until machine m pauses itself, as of now, unless it's busy: "12m",
// "1h05m", "45s", "now", or "never". It's "-" for a machine that's paused or that hibd didn't
// make.
func pausesIn(m hibapi.Machine, now time.Time) string {
	switch {
	case m.State != "running" || m.IdleSeconds == nil:
		return "-"
	case *m.IdleSeconds == 0:
		return "never"
	}
	left := m.EndAt.Sub(now)
	switch {
	case left <= 0:
		return "now"
	case left < time.Minute:
		return fmt.Sprintf("%ds", int(left/time.Second))
	case left < time.Hour:
		return fmt.Sprintf("%dm", int(left/time.Minute))
	}
	return fmt.Sprintf("%dh%02dm", int(left/time.Hour), int(left%time.Hour/time.Minute))
}

// safe makes text from hibd safe to print, and shows empty text as "-".
func safe(s string) string {
	if s == "" {
		return "-"
	}
	return termsafe.String(s)
}

// byName runs a hibd command that takes a machine name and replies with nothing.
func (a *app) byName(ctx context.Context, cfg *config, cmd, done string, args []string) int {
	names, ok := a.parse(a.flags(cmd), args, 1, cmd+" NAME")
	if !ok || !a.checkNames(names[0]) {
		return 2
	}
	if _, err := a.remote(ctx, cfg, shortTimeout, cmd, names[0], "--json"); err != nil {
		return a.fail(err)
	}
	if cmd == "rm" {
		if err := cfg.unpin(names[0]); err != nil {
			return a.fail(err)
		}
	}
	fmt.Fprintf(a.stdout, "%s %s\n", done, names[0])
	return 0
}

// exec runs ssh NAME.hib -- CMD in hib's place, so CMD gets the terminal, signals and exit code.
func (a *app) exec(cfg *config, args []string) int {
	if len(args) < 3 || args[1] != "--" {
		fmt.Fprintln(a.stderr, "usage: hib exec NAME -- COMMAND [ARG...]")
		return 2
	}
	if !a.checkNames(args[0]) {
		return 2
	}
	var sshArgs []string
	if cfg.SSHConfig != "" {
		sshArgs = append(sshArgs, "-F", cfg.SSHConfig)
	}
	sshArgs = append(sshArgs, "--", args[0]+".hib")
	if err := a.execSSH(append(sshArgs, args[2:]...)); err != nil {
		return a.fail(err)
	}
	return 0
}

func execSSH(args []string) error {
	path, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{"ssh"}, args...), os.Environ())
}

func (a *app) image(ctx context.Context, cfg *config, args []string) int {
	if len(args) == 0 || args[0] != "build" {
		fmt.Fprintln(a.stderr, "usage: hib image build NAME [-force]")
		return 2
	}
	fl := a.flags("image build")
	force := fl.Bool("force", false, "rebuild every step, not only those that changed")
	names, ok := a.parse(fl, args[1:], 1, "image build NAME [-force]")
	if !ok || !a.checkNames(names[0]) {
		return 2
	}
	remote := []string{"image", "build", names[0], "--json"}
	if *force {
		remote = append(remote, "--force")
	}
	r, err := a.remote(ctx, cfg, buildTimeout, remote...)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "built image %s (template %s)\n", names[0], safe(r.Template))
	return 0
}

func (a *app) bridgeCmd(ctx context.Context, cfg *config, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(a.stderr, "usage: hib bridge NAME.hib")
		return 2
	}
	name, ok := strings.CutSuffix(args[0], ".hib")
	if !ok || hibapi.CheckName(name) != nil {
		fmt.Fprintf(a.stderr, "hib bridge: want NAME.hib, with NAME of a-z, 0-9 and -, not %q\n", args[0])
		return 2
	}
	return a.bridge(ctx, cfg, name)
}

// checkNames reports whether names are all valid machine or image names, and explains if not.
func (a *app) checkNames(names ...string) bool {
	for _, n := range names {
		if err := hibapi.CheckName(n); err != nil {
			fmt.Fprintln(a.stderr, "hib:", err)
			return false
		}
	}
	return true
}

func (a *app) configDir(dir string) (string, error) {
	if dir == "" {
		if x := a.getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
			dir = filepath.Join(x, "hibernaut")
		} else if home := a.getenv("HOME"); home != "" {
			dir = filepath.Join(home, ".config", "hibernaut")
		} else {
			return "", errors.New("$HOME isn't set: use -config DIR")
		}
	}
	return filepath.Abs(dir)
}

func (a *app) flags(cmd string) *flag.FlagSet {
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(a.stderr)
	return fl
}

// parse parses flags wherever they are among args, and wants exactly n other arguments.
func (a *app) parse(fl *flag.FlagSet, args []string, n int, use string) ([]string, bool) {
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
		fmt.Fprintf(a.stderr, "usage: hib %s\n", use)
		return nil, false
	}
	return rest, true
}

// fail prints err, made safe for a terminal since it can quote hibd, and returns exit code 1.
func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "hib: %s\n", termsafe.String(err.Error()))
	return 1
}
