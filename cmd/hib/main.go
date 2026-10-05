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
	"runtime"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/version"
)

const usage = `usage: hib [-config DIR] <command> [flags]

commands:
  init              set this laptop up (-host USER@HOST; see hib init -h)
  new NAME          make a machine (-image, default ` + images.Default + `; --size small, medium or large,
                    default medium; -idle, default 15m); then: ssh NAME.hib
  ls                list machines, and when each pauses unless it's busy
  rm NAME           delete a machine for good
  pause NAME        pause a machine; connecting wakes it
  fork MACHINE      copy a machine (-count, default 1, most 10)
  connect github    open a browser to approve your GitHub login (--from-gh or --token instead)
  connect codex     give machines your Codex login on a ChatGPT plan, with a device code
  status            show the health of the host, E2B, each secret and the Codex login; never
                    wakes the host
  secret add NAME   store a bound secret (-env VAR -bind SITE[,SITE...])
  secret ls         list bound secrets
  secret rm NAME    remove a bound secret
  log               show secret swap audit entries (-n, default 50)
  exec NAME -- CMD  run CMD on a machine, as ssh NAME.hib -- CMD does
  image build NAME  build an image on the host, in every size (-force rebuilds every step)
  image save MACHINE NAME
                    save a machine as an image, memory included
  image ls          list built-in and saved images
  image rm NAME     remove a saved image not used by any machine
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
	if term.IsTerminal(int(os.Stderr.Fd())) {
		a.browser = browserOpener(runtime.GOOS, a.getenv, runCommand)
	}
	os.Exit(a.run(ctx, os.Args[1:]))
}

type runFunc func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, err error)

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	ssh            runFunc
	gh             runFunc
	browser        func(ctx context.Context, url string) error
	execSSH        func(args []string) error
	az             func(ctx context.Context, args ...string) ([]byte, error)
	executable     func() (string, error)
	wakePoll       time.Duration
	now            func() time.Time
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
	case "init", "new", "ls", "rm", "pause", "fork", "exec", "image", "bridge", "secret", "log", "connect", "status":
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
	if slices.Contains(args, "-h") || slices.Contains(args, "-help") || slices.Contains(args, "--help") {
		switch {
		case cmd == "fork":
			return a.fork(ctx, nil, args)
		case cmd == "image" && len(args) > 0 && args[0] == "save":
			return a.imageSave(ctx, nil, args[1:])
		}
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
	case "fork":
		return a.fork(ctx, cfg, args)
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
	size := fl.String("size", "", "the machine's size: small, medium or large (default medium, or a saved image's size)")
	idle := fl.Duration("idle", 0, "pause the machine once it has been idle this long, e.g. 1h (default 15m; 0 means never)")
	names, ok := a.parse(fl, args, 1, "new NAME [-image IMAGE] [--size SIZE] [-idle 15m]")
	if !ok || !a.checkNames(names[0], *image) {
		return 2
	}
	words := []string{"new", names[0], "--image", *image}

	var badSize error
	fl.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "size":
			_, badSize = images.SizeNamed(*size)
			words = append(words, "--size", *size)
		case "idle":
			words = append(words, "--idle", idle.String())
		}
	})
	if badSize != nil {
		fmt.Fprintln(a.stderr, "hib:", termsafe.String(badSize.Error()))
		return 2
	}
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
	fmt.Fprintln(tw, "NAME\tSTATE\tIMAGE\tSIZE\tIDLE\tPAUSES IN\tSTARTED\tID\tFROM")
	for _, m := range r.Machines {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", safe(m.Name), safe(m.State), safe(m.Image), safe(m.Size),
			hibapi.FormatIdle(m.IdleSeconds), pausesIn(m, now), m.StartedAt.Local().Format("2006-01-02 15:04"), safe(m.ID), safe(m.ForkOf))
	}
	tw.Flush()
	if r.Room != nil {
		fmt.Fprintf(a.stdout, "\n%s\n", roomLine(*r.Room))
	}
	return 0
}

func roomLine(r hibapi.Room) string {
	free := fmt.Sprintf("%s of %s memory free, %s disk free.", hibapi.FormatMB(r.MemoryFreeMB), hibapi.FormatMB(r.MemoryMB),
		hibapi.FormatMB(r.DiskFreeMB))
	if len(r.Sizes) > 0 {
		var fits []string
		for _, s := range r.Sizes {
			if s.Fits > 0 {
				fits = append(fits, fmt.Sprintf("%d %s", s.Fits, safe(s.Size)))
			}
		}
		if len(fits) == 0 {
			return fmt.Sprintf("No room for another machine: %s hib pause NAME frees memory, hib rm NAME frees disk.", free)
		}
		return fmt.Sprintf("Room for more machines: %s. %s", joinList(fits, "or"), free)
	}
	image := safe(r.Image)
	switch {
	case r.Fits <= 0:
		return fmt.Sprintf("No room for another %s machine: %s hib pause NAME frees memory, hib rm NAME frees disk.", image, free)
	case r.Fits == 1:
		return fmt.Sprintf("Room for 1 more %s machine: %s", image, free)
	}
	return fmt.Sprintf("Room for %d more %s machines: %s", r.Fits, image, free)
}

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

func safe(s string) string {
	if s == "" {
		return "-"
	}
	return termsafe.String(s)
}

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
	if len(args) == 0 {
		fmt.Fprintln(a.stderr, imageUsage)
		return 2
	}
	switch args[0] {
	case "save":
		return a.imageSave(ctx, cfg, args[1:])
	case "ls":
		return a.imageList(ctx, cfg, args[1:])
	case "rm":
		return a.imageRemove(ctx, cfg, args[1:])
	case "build":
	default:
		fmt.Fprintln(a.stderr, imageUsage)
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
	if len(r.Sizes) == 0 {
		fmt.Fprintf(a.stdout, "built image %s (template %s)\n", names[0], safe(r.Template))
		return 0
	}
	sizes := make([]string, len(r.Sizes))
	for i, s := range r.Sizes {
		sizes[i] = safe(s)
	}
	fmt.Fprintf(a.stdout, "built image %s in sizes %s\n", names[0], joinList(sizes, "and"))
	return 0
}

func joinList(items []string, conj string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
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

func (a *app) parse(fl *flag.FlagSet, args []string, n int, use string) ([]string, bool) {
	rest, err := a.parseCommand(fl, args, n, use)
	return rest, err == nil
}

func (a *app) parseCommand(fl *flag.FlagSet, args []string, n int, use string) ([]string, error) {
	var rest []string
	for {
		if err := fl.Parse(args); err != nil {
			return nil, err
		}
		if fl.NArg() == 0 {
			break
		}
		rest = append(rest, fl.Arg(0))
		args = fl.Args()[1:]
	}
	if len(rest) != n {
		fmt.Fprintf(a.stderr, "usage: hib %s\n", use)
		return nil, errors.New("wrong argument count")
	}
	return rest, nil
}

func argumentExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "hib: %s\n", termsafe.String(err.Error()))
	return 1
}
