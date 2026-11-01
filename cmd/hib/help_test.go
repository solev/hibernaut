package main

import (
	"flag"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/version"
)

func allCommands(c *command) []*command {
	list := []*command{c}
	for _, sub := range c.subs {
		list = append(list, allCommands(sub)...)
	}
	return list
}

func declaredFlags(c *command) []*flag.Flag {
	if c.flags == nil {
		return nil
	}
	fl := flag.NewFlagSet(c.path(), flag.ContinueOnError)
	c.flags(fl)
	var flags []*flag.Flag
	fl.VisitAll(func(f *flag.Flag) { flags = append(flags, f) })
	return flags
}

// homeless is a client before hib init, with no $HOME, that names no settings directory.
func homeless(t *testing.T, h *host) (*client, func(args ...string) int) {
	c := newClient(t, h)
	c.getenv = func(string) string { return "" }
	return c, func(args ...string) int {
		c.out.Reset()
		c.errOut.Reset()
		return c.run(t.Context(), args)
	}
}

var helpFlag = regexp.MustCompile(`(^|[\s\[(|])(-{1,2})([a-z][a-z0-9-]*)`)

func TestEveryCommandHasHelpBeforeInit(t *testing.T) {
	h := newHost(t)
	c, hib := homeless(t, h)
	for _, cmd := range allCommands(commandTable())[1:] {
		path := strings.Fields(cmd.path())
		asks := [][]string{append([]string{"help"}, path...)}
		if !cmd.raw {
			for _, help := range []string{"-h", "-help", "--help"} {
				asks = append(asks, append(append([]string{}, path...), help))
			}
		}
		var first string
		for _, args := range asks {
			if code := hib(args...); code != 0 || c.errOut.Len() > 0 {
				t.Errorf("hib %s = %d, stderr %q; want 0 and nothing", strings.Join(args, " "), code, c.errOut)
				continue
			}
			help := c.out.String()
			if first == "" {
				first = help
			} else if help != first {
				t.Errorf("hib %s printed other help:\n%s\nthan:\n%s", strings.Join(args, " "), help, first)
			}
		}
		if !strings.HasPrefix(first, "usage: hib "+cmd.path()) || !strings.Contains(first, "\nexamples:\n  ") {
			t.Errorf("help for %s lacks a synopsis or an example:\n%s", cmd.path(), first)
		}
		for _, match := range helpFlag.FindAllStringSubmatch(first, -1) {
			dashes := "--"
			if len(match[3]) == 1 {
				dashes = "-"
			}
			if match[2] != dashes {
				t.Errorf("help for %s spells %s%s; want %s%s", cmd.path(), match[2], match[3], dashes, match[3])
			}
		}
		for _, f := range declaredFlags(cmd) {
			spelling := "--" + f.Name
			if len(f.Name) == 1 {
				spelling = "-" + f.Name
			}
			if !strings.Contains(first, "\n  "+spelling+" ") && !strings.HasSuffix(first, "\n  "+spelling+"\n") {
				t.Errorf("help for %s lacks %s:\n%s", cmd.path(), spelling, first)
			}
			if f.Name != "json" && !strings.Contains(joinWords(cmd.args, cmd.opts), spelling) {
				t.Errorf("synopsis of %s lacks %s: %s", cmd.path(), spelling, cmd.opts)
			}
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("help reached the host: %q", calls)
	}
}

func TestHelpShowsFlagsWithTheirDefaults(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	for cmd, want := range map[string][]string{
		"new":         {"usage: hib new NAME [--image IMAGE] [--size SIZE] [--idle 15m]\n", "  --image IMAGE ", "(default dev)", "  --idle DURATION "},
		"log":         {"usage: hib log [-n N] [--links]\n", "  -n N ", "(default 50)", "  --links ", "  --json "},
		"fork":        {"(default 1)", "pauses for a few seconds", "a secret you typed in yourself goes into the image too"},
		"image build": {"usage: hib image build NAME [--force] [--verbose]\n", "  --force ", "  --verbose "},
		"rm":          {"usage: hib rm NAME [--yes]\n", "  --yes "},
		"exec":        {"usage: hib exec NAME -- CMD [ARG...]\n"},
		"ssh":         {"usage: hib ssh NAME [-- CMD]\n"},
	} {
		if code := c.hib(append([]string{"help"}, strings.Fields(cmd)...)...); code != 0 {
			t.Fatalf("hib help %s = %d", cmd, code)
		}
		for _, part := range want {
			if !strings.Contains(c.out.String(), part) {
				t.Errorf("hib help %s lacks %q:\n%s", cmd, part, c.out)
			}
		}
	}
	if code := c.hib("help", "status"); code != 0 || strings.Contains(c.out.String(), "(default") {
		t.Errorf("hib help status shows a default for a flag without one:\n%s", c.out)
	}
}

func TestGeneralHelpListsCommandsAndExitCodes(t *testing.T) {
	h := newHost(t)
	c, hib := homeless(t, h)
	if code := hib("help"); code != 0 || c.errOut.Len() > 0 {
		t.Fatalf("hib help = %d, stderr %q", code, c.errOut)
	}
	help := c.out.String()
	_, commands, _ := strings.Cut(help, "\ncommands:\n")
	if first := strings.Fields(commands)[0]; first != "init" {
		t.Errorf("hib help lists %s first, want init", first)
	}
	last := -1
	for _, path := range []string{"image rm", "task start", "task rm", "recipe put", "recipe default", "completion"} {
		pos := strings.Index(help, "\n  "+path+" ")
		if pos <= last {
			t.Errorf("hib help lists %s out of order; want image, task, recipe, completion", path)
		}
		last = pos
	}
	for _, part := range []string{
		"usage: hib [--config DIR] COMMAND [ARG...]\n",
		"\n  new NAME ", "\n  image build NAME ", "\n  image save MACHINE NAME ", "\n  ssh NAME [-- CMD] ",
		"\n  completion bash|zsh|fish ", "\n  disconnect codex|github ", "\n  connect github ", "\n  connect codex ",
		"\n  --config DIR ", "\n  --version ",
		"exit codes:\n", "\n  0    done\n", "\n  1    failed\n", "\n  2    used wrongly\n", "\n  130  stopped by Ctrl-C",
	} {
		if !strings.Contains(help, part) {
			t.Errorf("hib help lacks %q:\n%s", part, help)
		}
	}
	if strings.Contains(help, "__complete") || strings.Contains(help, "completion bash ") {
		t.Errorf("hib help lists a hidden command or each completion shell:\n%s", help)
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"-help"}, {"--config", "/nowhere", "help"}} {
		if code := hib(args...); code != 0 || c.out.String() != help || c.errOut.Len() > 0 {
			t.Errorf("hib %s = %d, stderr %q; want hib help's text", strings.Join(args, " "), code, c.errOut)
		}
	}
	if code := hib(); code != 2 || c.errOut.String() != help || c.out.Len() > 0 {
		t.Errorf("hib alone = %d, stdout %q; want 2 and hib help's text on stderr", code, c.out)
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("help reached the host: %q", calls)
	}
}

func TestFlagMetavariablesAreConsistent(t *testing.T) {
	c, hib := homeless(t, newHost(t))
	seen := map[string]string{}
	flagLine := regexp.MustCompile(`^  --?([a-z][a-z0-9-]*)(?: ([A-Z][A-Z0-9_@]*))? {2,}(.*)$`)
	midWordCapital := regexp.MustCompile(`[a-z][A-Z]`)
	for _, cmd := range allCommands(commandTable()) {
		if code := hib(append([]string{"help"}, strings.Fields(cmd.path())...)...); code != 0 {
			t.Fatalf("hib help %s = exit %d", cmd.path(), code)
		}
		_, section, _ := strings.Cut(c.out.String(), "\nflags:\n")
		section, _, _ = strings.Cut(section, "\nexamples:\n")
		count := 0
		for _, line := range strings.Split(section, "\n") {
			match := flagLine.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			count++
			name, metavar, doc := match[1], match[2], match[3]
			if want, ok := seen[name]; ok && metavar != want {
				t.Errorf("flag %s metavariable = %s in %s, want %s", name, metavar, cmd.path(), want)
			} else {
				seen[name] = metavar
			}
			if midWordCapital.MatchString(doc) {
				t.Errorf("hib help %s has a capital in mid-word: %s", cmd.path(), line)
			}
			if name == "image" || name == "size" || name == "timeout" {
				for _, word := range strings.Fields(doc) {
					if word == metavar {
						t.Errorf("hib help %s repeats its %s metavariable in the description: %s", cmd.path(), name, line)
					}
				}
			}
		}
		if want := len(declaredFlags(cmd)); count != want {
			t.Errorf("hib help %s has %d readable flag lines, want %d", cmd.path(), count, want)
		}
	}
}

func TestGroupsWithoutACommandShowTheirHelp(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	for _, group := range []string{"image", "secret", "connect", "disconnect", "completion"} {
		if code := c.hib(group); code != 2 || !strings.HasPrefix(c.errOut.String(), "usage: hib "+group+" ") || c.out.Len() > 0 {
			t.Errorf("hib %s = %d, stderr %q; want 2 and its help", group, code, c.errOut)
		}
		help := c.errOut.String()
		if code := c.hib(group, "--help"); code != 0 || c.out.String() != help {
			t.Errorf("hib %s --help = %d, stdout %q; want %q", group, code, c.out, help)
		}
	}
}

func TestVersionWithoutSettings(t *testing.T) {
	h := newHost(t)
	c, hib := homeless(t, h)
	for _, args := range [][]string{{"version"}, {"--version"}, {"-version"}} {
		if code := hib(args...); code != 0 || c.out.String() != "hib "+version.Version+"\n" || c.errOut.Len() > 0 {
			t.Errorf("hib %s = %d, %q, stderr %q", args[0], code, c.out, c.errOut)
		}
	}
}

func TestCommandsLoadSettingsAfterTheirFlags(t *testing.T) {
	h := newHost(t)
	c, hib := homeless(t, h)
	if code := hib("ls"); code != 1 || !strings.Contains(c.errOut.String(), "$HOME isn't set: use --config DIR") {
		t.Errorf("ls without $HOME = %d, stderr %q", code, c.errOut)
	}
	if code := hib("ls", "--bogus"); code != 2 || !strings.HasPrefix(c.errOut.String(), "hib ls: unknown flag --bogus\n") {
		t.Errorf("ls --bogus without $HOME = %d, stderr %q; want the flag mistake", code, c.errOut)
	}
}

func TestExamplesAreNoMistakes(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	c.interactive = true
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{Error: "refused in this test"}), true
	}
	for _, cmd := range allCommands(commandTable()) {
		if path := cmd.path(); strings.HasPrefix(path, "connect") || strings.HasPrefix(path, "disconnect") {
			continue
		}
		for _, example := range cmd.examples {
			words, ok := strings.CutPrefix(example, "hib ")
			if !ok {
				continue
			}
			words, _, _ = strings.Cut(words, " <")
			words, _, _ = strings.Cut(words, " |")
			if code := c.hib(strings.Fields(words)...); code == 2 || strings.Contains(c.errOut.String(), "\nusage: ") {
				t.Errorf("example %q is a mistake: exit %d, stderr %q", example, code, c.errOut)
			}
		}
	}
}
