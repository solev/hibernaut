package main

import (
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/solev/hibernaut/internal/suggest"
	"github.com/solev/hibernaut/internal/termsafe"
)

const exitCodes = `exit codes:
  0    done
  1    failed
  2    used wrongly
  130  stopped by Ctrl-C (128 plus the signal's number)
`

func printHelp(w io.Writer, c *command) {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s\n\n%s\n", c.synopsis(), c.about)
	if c.subs != nil {
		b.WriteString("\ncommands:\n")
		tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		listCommands(tw, c)
		tw.Flush()
	}
	if c.flags != nil {
		fl := flag.NewFlagSet(c.path(), flag.ContinueOnError)
		c.flags(fl)
		if flags := describeFlags(fl); flags != "" {
			b.WriteString("\nflags:\n" + flags)
		}
	}
	b.WriteString("\nexamples:\n")
	for _, e := range c.examples {
		b.WriteString("  " + e + "\n")
	}
	if c.parent == nil {
		b.WriteString("\n" + exitCodes)
	}
	if c.subs != nil {
		fmt.Fprintf(&b, "\nrun %s for a command's flags and examples.\n", joinWords("hib help", c.path(), "COMMAND"))
	}
	io.WriteString(w, b.String())
}

func listCommands(w io.Writer, c *command) {
	for _, sub := range c.subs {
		switch {
		case sub.hidden:
		case sub.subs != nil && !sub.oneLine:
			listCommands(w, sub)
		default:
			fmt.Fprintf(w, "  %s\t%s\n", joinWords(sub.path(), sub.args, sub.choices()), sub.summary)
		}
	}
}

func describeFlags(fl *flag.FlagSet) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fl.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		if name != "" {
			name = " " + strings.ToUpper(name)
		}
		switch f.DefValue {
		case "", "false", "0", "0s":
		default:
			usage += fmt.Sprintf(" (default %s)", f.DefValue)
		}
		fmt.Fprintf(tw, "  %s%s%s\t%s\n", flagPrefix(f.Name), f.Name, name, usage)
	})
	tw.Flush()
	return b.String()
}

func flagPrefix(name string) string {
	if len(name) == 1 {
		return "-"
	}
	return "--"
}

func (a *app) mistake(msg string) int {
	c := a.command
	fmt.Fprintf(a.stderr, "%s: %s\nusage: %s\nrun %s\n", c.title(), termsafe.Line(msg), c.synopsis(),
		joinWords("hib help", c.path()))
	return 2
}

func unknownCommand(name string, group *command) string {
	msg := "unknown command " + name
	if near, ok := suggest.Closest(name, group.names()); ok {
		msg += ". Did you mean " + near + "?"
	}
	return msg
}

const quoted = `("(?:[^"\\]|\\.)*")`

var flagErrors = []struct {
	re        *regexp.Regexp
	with      string
	flagGroup int
}{
	{regexp.MustCompile(`^flag provided but not defined: -(.*)$`), "unknown flag --$1", 1},
	{regexp.MustCompile(`^flag needs an argument: -(.*)$`), "--$1 needs a value", 1},
	{regexp.MustCompile(`^invalid (?:boolean )?value ` + quoted + ` for (?:flag )?-(.*?): (.*)$`), "invalid value $1 for --$2: $3", 2},
	{regexp.MustCompile(`^bad flag syntax: (.*)$`), "bad flag $1", 0},
}

func flagMessage(err error) string {
	msg := err.Error()
	for _, e := range flagErrors {
		if match := e.re.FindStringSubmatch(msg); match != nil {
			with := e.with
			if e.flagGroup != 0 {
				placeholder := fmt.Sprintf("$%d", e.flagGroup)
				with = strings.ReplaceAll(with, "--"+placeholder, flagPrefix(match[e.flagGroup])+placeholder)
			}
			return e.re.ReplaceAllString(msg, with)
		}
	}
	return msg
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--h" || arg == "--help"
}

func quietFlags(name string) *flag.FlagSet {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	return fl
}
