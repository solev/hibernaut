package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/atomicfile"
	"github.com/solev/hibernaut/internal/hibapi"
)

const (
	namesFile     = "names.json"
	maxNames      = 1000
	maxNamesBytes = 1 << 20
)

type names struct {
	Machines []string `json:"machines"`
	Images   []string `json:"images"`
}

func goodNames(list []string) []string {
	var good []string
	for _, n := range list {
		if hibapi.CheckName(n) == nil {
			good = append(good, n)
		}
	}
	slices.Sort(good)
	good = slices.Compact(good)
	return good[:min(len(good), maxNames)]
}

func readNames(dir string) names {
	f, err := os.Open(filepath.Join(dir, namesFile))
	if err != nil {
		return names{}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxNamesBytes+1))
	var n names
	if err != nil || len(data) > maxNamesBytes || json.Unmarshal(data, &n) != nil {
		return names{}
	}
	return names{Machines: goodNames(n.Machines), Images: goodNames(n.Images)}
}

func rememberNames(dir string, update func(*names)) {
	n := readNames(dir)
	update(&n)
	n = names{Machines: goodNames(n.Machines), Images: goodNames(n.Images)}
	if data, err := json.Marshal(n); err == nil {
		_ = atomicfile.WriteFile(filepath.Join(dir, namesFile), data, 0o600)
	}
}

func rememberMachines(cfg *config, list []hibapi.Machine) {
	rememberNames(cfg.dir, func(n *names) {
		n.Machines = nil
		for _, m := range list {
			n.Machines = append(n.Machines, m.Name)
		}
	})
}

func rememberImages(cfg *config, list []hibapi.Image) {
	rememberNames(cfg.dir, func(n *names) {
		n.Images = nil
		for _, img := range list {
			n.Images = append(n.Images, img.Name)
		}
	})
}

func complete(_ context.Context, a *app, words []string) int {
	for _, c := range a.completions(words) {
		fmt.Fprintln(a.stdout, c)
	}
	return 0
}

func (a *app) completions(words []string) []string {
	if len(words) == 0 {
		words = []string{""}
	}
	before, word := words[:len(words)-1], words[len(words)-1]
	w := walker{c: a.command.root(), configDir: a.configFlag}
	for i, arg := range before {
		if !w.next(arg, i > 0 && before[i-1] == "=") {
			return nil
		}
	}
	var candidates []string
	prefix := ""
	switch {
	case w.pending != nil && word == "=":
		word = ""
		fallthrough
	case w.pending != nil:
		candidates = a.flagValues(w, w.pending.Name)
	case strings.HasPrefix(word, "-") && strings.Contains(word, "="):
		name, value, _ := strings.Cut(word, "=")
		prefix, word = name+"=", value
		candidates = a.flagValues(w, strings.TrimLeft(name, "-"))
	case strings.HasPrefix(word, "-"):
		dashes := "--"
		if !strings.HasPrefix(word, "--") && len(word) > 1 {
			dashes = "-"
		}
		w.c.flagSet().VisitAll(func(f *flag.Flag) {
			candidates = append(candidates, dashes+f.Name)
		})
	default:
		candidates = a.positional(w)
	}
	var out []string
	for _, c := range candidates {
		if strings.HasPrefix(c, word) {
			out = append(out, prefix+c)
		}
	}
	return out
}

type walker struct {
	c          *command
	pending    *flag.Flag
	positional []string
	afterDash  bool
	configDir  string
}

func (w *walker) next(arg string, afterEquals bool) bool {
	switch {
	case w.afterDash:
		return false
	case w.pending != nil && arg == "=" && !afterEquals:
		return true
	case w.pending != nil:
		w.setFlag(w.pending.Name, arg)
		w.pending = nil
		return true
	case arg == "--" && w.c.takesCommand:
		w.afterDash = true
		return true
	case strings.HasPrefix(arg, "-") && arg != "-":
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := w.c.flagSet().Lookup(name)
		switch {
		case f == nil:
		case inline:
			w.setFlag(name, value)
		case !isBoolFlag(f):
			w.pending = f
		}
		return true
	case w.c.subs != nil:
		w.c = w.c.sub(arg)
		return w.c != nil
	}
	w.positional = append(w.positional, arg)
	return true
}

func (w *walker) setFlag(name, value string) {
	if w.c.parent == nil && name == "config" {
		w.configDir = value
	}
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func (c *command) flagSet() *flag.FlagSet {
	fl := quietFlags(c.path())
	if c.flags != nil {
		c.flags(fl)
	}
	return fl
}

func (a *app) flagValues(w walker, name string) []string {
	switch {
	case name == "size" && w.c.path() == "new":
		return images.SizeNames()
	case name == "image" && w.c.path() == "new":
		return goodNames(append(images.Names(), a.cachedNames(w).Images...))
	}
	return nil
}

func (a *app) positional(w walker) []string {
	if w.c.subs != nil {
		return w.c.names()
	}
	arg := len(w.positional)
	switch w.c.path() {
	case "rm", "pause", "ssh", "exec", "fork", "port add", "port rm":
		if arg == 0 {
			return a.cachedNames(w).Machines
		}
	case "image save":
		if arg == 0 {
			return a.cachedNames(w).Machines
		}
	case "image rm":
		if arg == 0 {
			return a.cachedNames(w).Images
		}
	case "image build":
		if arg == 0 {
			return images.Names()
		}
	case "help":
		c := w.c.root()
		for _, name := range w.positional {
			if c = c.sub(name); c == nil {
				return nil
			}
		}
		return c.names()
	}
	return nil
}

func (a *app) cachedNames(w walker) names {
	dir := w.configDir
	if rest, ok := strings.CutPrefix(dir, "~/"); ok && a.getenv("HOME") != "" {
		dir = filepath.Join(a.getenv("HOME"), rest)
	}
	dir, err := a.configDir(dir)
	if err != nil {
		return names{}
	}
	return readNames(dir)
}
