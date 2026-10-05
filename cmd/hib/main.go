// Command hib is Hibernaut's laptop CLI. It runs hibd on the host over SSH, and it's the
// ProxyCommand that ssh NAME.hib goes through (spec §5, SSH path).
package main

import (
	"bufio"
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
	"time"

	"golang.org/x/term"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

const (
	shortTimeout = 2 * time.Minute
	newTimeout   = 6 * time.Minute
	buildTimeout = 65 * time.Minute
)

func main() {
	ctx, cancel := context.WithCancelCause(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go stopOnSignal(signals, cancel)
	stderrTerminal := term.IsTerminal(int(os.Stderr.Fd()))
	a := &app{
		stdin:          os.Stdin,
		stdout:         os.Stdout,
		stderr:         os.Stderr,
		getenv:         os.Getenv,
		ssh:            runCommand("ssh"),
		gh:             runCommand("gh"),
		execSSH:        execSSH,
		az:             runAz,
		executable:     os.Executable,
		wakePoll:       5 * time.Second,
		azLimit:        shortTimeout,
		liveAfter:      liveAfter,
		now:            time.Now,
		colour:         colourOn(term.IsTerminal(int(os.Stdout.Fd())), os.Getenv),
		interactive:    term.IsTerminal(int(os.Stdin.Fd())) && stderrTerminal,
		stderrTerminal: stderrTerminal,
	}
	if stderrTerminal {
		a.browser = browserOpener(runtime.GOOS, a.getenv, runCommand)
	}
	os.Exit(a.run(ctx, os.Args[1:]))
}

// stopped is the cause hib's context is cancelled with, since a cancelled context can't tell
// which signal arrived.
type stopped struct {
	signal syscall.Signal
}

func (s stopped) Error() string {
	return "stopped by " + s.signal.String()
}

func stopOnSignal(signals <-chan os.Signal, cancel context.CancelCauseFunc) {
	if sig, ok := (<-signals).(syscall.Signal); ok {
		cancel(stopped{sig})
	}
}

func colourOn(terminal bool, getenv func(string) string) bool {
	return terminal && getenv("TERM") != "dumb" && getenv("NO_COLOR") == ""
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
	azLimit        time.Duration
	liveAfter      time.Duration
	stderrTerminal bool
	live           *live
	now            func() time.Time
	colour         bool
	interactive    bool
	command        *command
	configFlag     string
	json           bool
}

func (a *app) run(ctx context.Context, args []string) int {
	a.json = false
	code := a.dispatch(ctx, commandTable(), args)
	var s stopped
	if code != 0 && errors.As(context.Cause(ctx), &s) {
		return 128 + int(s.signal)
	}
	return code
}

func (a *app) dispatch(ctx context.Context, c *command, args []string) int {
	a.command = c
	if c.guard != nil {
		if err := c.guard(args); err != nil {
			return a.mistake(err.Error())
		}
	}
	switch {
	case c.parent == nil:
		fl := quietFlags(c.name)
		run := c.flags(fl)
		if err := fl.Parse(args); err != nil {
			return a.parseFailed(err)
		}
		return run(ctx, a, fl.Args())
	case c.subs != nil:
		return a.runGroup(ctx, c, args)
	}
	return a.runLeaf(ctx, c, args)
}

func (a *app) runGroup(ctx context.Context, c *command, args []string) int {
	i := slices.IndexFunc(args, func(arg string) bool { return !strings.HasPrefix(arg, "-") })
	if i < 0 {
		if slices.ContainsFunc(args, isHelpFlag) {
			printHelp(a.stdout, c)
			return 0
		}
		printHelp(a.stderr, c)
		return 2
	}
	return a.runSub(ctx, c, args[i], slices.Concat(args[:i], args[i+1:]))
}

func (a *app) runSub(ctx context.Context, c *command, name string, args []string) int {
	sub := c.sub(name)
	if sub == nil {
		return a.mistake(unknownCommand(name, c))
	}
	return a.dispatch(ctx, sub, args)
}

func (a *app) runLeaf(ctx context.Context, c *command, args []string) int {
	var remote []string
	if i := slices.Index(args, "--"); c.takesCommand && i >= 0 {
		args, remote = args[:i], args[i:]
	}
	fl := quietFlags(c.path())
	run := c.flags(fl)
	if c.raw {
		return run(ctx, a, args)
	}
	positional, err := parseAnywhere(fl, args)
	if err != nil {
		return a.parseFailed(err)
	}
	switch {
	case c.nargs == anyArgs:
	case len(positional) > c.nargs:
		return a.mistake("unexpected argument " + positional[c.nargs])
	case len(positional) < c.nargs:
		return a.mistake("missing " + strings.Fields(c.args)[len(positional)])
	}
	return run(ctx, a, append(positional, remote...))
}

func (a *app) parseFailed(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		printHelp(a.stdout, a.command)
		return 0
	}
	return a.mistake(flagMessage(err))
}

// parseAnywhere reads flags after positional arguments too, where Go's flag package stops.
func parseAnywhere(fl *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fl.Parse(args); err != nil {
			return nil, err
		}
		if fl.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fl.Arg(0))
		args = fl.Args()[1:]
	}
}

func (a *app) settings() (*config, error) {
	dir, err := a.configDir(a.configFlag)
	if err != nil {
		return nil, err
	}
	return loadConfig(dir)
}

func (a *app) newMachine(ctx context.Context, cfg *config, fl *flag.FlagSet, name, image, size string, idle time.Duration) int {
	if !a.checkNames(name, image) {
		return 2
	}
	words := []string{"new", name, "--image", image}
	var badSize error
	fl.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "size":
			_, badSize = images.SizeNamed(size)
			words = append(words, "--size", size)
		case "idle":
			words = append(words, "--idle", idle.String())
		}
	})
	if badSize != nil {
		return a.mistake(badSize.Error())
	}
	stopLive := a.showLive(ctx, "making "+name)
	r, err := a.remote(ctx, cfg, newTimeout, append(words, "--json")...)
	stopLive()
	if err != nil {
		return a.failWaiting(ctx, err, fmt.Sprintf("The host may still be making %s: check hib ls", name))
	}
	if r.Machine == nil {
		return a.fail(errors.New("hibd didn't say which machine it made: run hib ls to see your machines"))
	}
	if a.json {
		return a.reply(hibapi.Response{Machine: r.Machine}, nil)
	}
	fmt.Fprintf(a.stdout, "made %s; connect with: ssh %s.hib\n", name, name)
	return 0
}

func (a *app) ls(ctx context.Context, cfg *config, _ []string) int {
	r, err := a.remote(ctx, cfg, shortTimeout, "ls", "--json")
	if err != nil {
		return a.failWaiting(ctx, err, "")
	}
	slices.SortFunc(r.Machines, func(x, y hibapi.Machine) int {
		return strings.Compare(x.Name+"\x00"+x.ID, y.Name+"\x00"+y.ID)
	})
	rememberMachines(cfg, r.Machines)
	if a.json {
		return a.reply(hibapi.Response{Machines: r.Machines, Room: r.Room}, nil)
	}
	now := a.now()
	tw := hibapi.NewTable(a.stdout, a.style())
	fmt.Fprintln(tw, "NAME\tSTATE\tIMAGE\tSIZE\tIDLE\tPAUSES IN\tSTARTED\tID\tFROM")
	for _, m := range r.Machines {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", safe(m.Name), tw.State(safe(m.State)), safe(m.Image), safe(m.Size),
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
			return fmt.Sprintf("No room for another machine: %s hib pause NAME frees memory, hib rm NAME --yes frees disk.", free)
		}
		return fmt.Sprintf("Room for more machines: %s. %s", joinList(fits, "or"), free)
	}
	image := safe(r.Image)
	switch {
	case r.Fits <= 0:
		return fmt.Sprintf("No room for another %s machine: %s hib pause NAME frees memory, hib rm NAME --yes frees disk.", image, free)
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
	return termsafe.Line(s)
}

func (a *app) rm(ctx context.Context, cfg *config, name string, yes bool) int {
	if !a.checkNames(name) {
		return 2
	}
	if !yes {
		if !a.interactive {
			return a.mistake(fmt.Sprintf("deleting %s can't be undone, so pass --yes when there's no terminal to ask on", name))
		}
		if !a.confirm(ctx, fmt.Sprintf("Delete %s and its disk for good? [y/N] ", name)) {
			fmt.Fprintf(a.stderr, "kept %s\n", name)
			return 1
		}
	}
	return a.byName(ctx, cfg, "rm", "deleting", "removed", name)
}

const maxAnswer = 1024

func (a *app) confirm(ctx context.Context, question string) bool {
	fmt.Fprint(a.stderr, question)
	answer := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(a.stdin, maxAnswer)).ReadString('\n')
		answer <- line
	}()
	select {
	case <-ctx.Done():
	case line := <-answer:
		if line, ok := strings.CutSuffix(line, "\n"); ok {
			word := strings.ToLower(strings.TrimSpace(line))
			return word == "y" || word == "yes"
		}
	}
	fmt.Fprintln(a.stderr)
	return false
}

func (a *app) pause(ctx context.Context, cfg *config, args []string) int {
	return a.byName(ctx, cfg, "pause", "pausing", "paused", args[0])
}

func (a *app) byName(ctx context.Context, cfg *config, cmd, doing, done, name string) int {
	if !a.checkNames(name) {
		return 2
	}
	if _, err := a.remote(ctx, cfg, shortTimeout, cmd, name, "--json"); err != nil {
		return a.failWaiting(ctx, err, fmt.Sprintf("The host may still be %s %s: check hib ls", doing, name))
	}
	if cmd == "rm" {
		if err := cfg.unpin(name); err != nil {
			return a.fail(fmt.Errorf("deleted %s, but couldn't forget its host key: %w. Remove that file yourself", name, err))
		}
	}
	fmt.Fprintf(a.stdout, "%s %s\n", done, name)
	return 0
}

func (a *app) exec(ctx context.Context, cfg *config, args []string) int {
	if len(args) < 3 {
		return a.mistake("missing CMD")
	}
	return a.sshTo(ctx, cfg, args)
}

func (a *app) sshTo(_ context.Context, cfg *config, args []string) int {
	if !a.checkNames(args[0]) {
		return 2
	}
	var sshArgs []string
	if cfg.SSHConfig != "" {
		sshArgs = append(sshArgs, "-F", cfg.SSHConfig)
	}
	sshArgs = append(sshArgs, "--", args[0]+".hib")
	if len(args) > 2 {
		sshArgs = append(sshArgs, args[2:]...)
	}
	if err := a.execSSH(sshArgs); err != nil {
		return a.fail(err)
	}
	return 0
}

func execSSH(args []string) error {
	path, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("%w: install OpenSSH 9.6 or later", err)
	}
	return syscall.Exec(path, append([]string{"ssh"}, args...), os.Environ())
}

func (a *app) imageBuild(ctx context.Context, cfg *config, name string, force bool) int {
	if !a.checkNames(name) {
		return 2
	}
	remote := []string{"image", "build", name, "--json"}
	if force {
		remote = append(remote, "--force")
	}
	r, err := a.remote(ctx, cfg, buildTimeout, remote...)
	if err != nil {
		return a.failWaiting(ctx, err, fmt.Sprintf("The host may still finish building %s: check hib image ls in a few minutes", name))
	}
	if len(r.Sizes) == 0 {
		fmt.Fprintf(a.stdout, "built image %s (template %s)\n", name, safe(r.Template))
		return 0
	}
	sizes := make([]string, len(r.Sizes))
	for i, s := range r.Sizes {
		sizes[i] = safe(s)
	}
	fmt.Fprintf(a.stdout, "built image %s in sizes %s\n", name, joinList(sizes, "and"))
	return 0
}

func joinList(items []string, conj string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
}

func (a *app) bridgeCmd(ctx context.Context, cfg *config, args []string) int {
	name, ok := strings.CutSuffix(args[0], ".hib")
	if !ok || hibapi.CheckName(name) != nil {
		return a.mistake(fmt.Sprintf("want NAME.hib, with NAME of a-z, 0-9 and -, not %q", args[0]))
	}
	return a.bridge(ctx, cfg, name)
}

func (a *app) checkNames(names ...string) bool {
	for _, n := range names {
		if err := hibapi.CheckName(n); err != nil {
			a.mistake(err.Error())
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
			return "", errors.New("$HOME isn't set: use --config DIR")
		}
	}
	return filepath.Abs(dir)
}

func (a *app) fail(err error) int {
	if a.json {
		return a.reply(hibapi.Response{}, err)
	}
	fmt.Fprintf(a.stderr, "hib: %s\n", termsafe.String(err.Error()))
	return 1
}
