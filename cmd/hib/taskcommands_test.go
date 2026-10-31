package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

func TestTaskCommandsStayInSync(t *testing.T) {
	root := commandTable()
	want := map[string]bool{}
	for _, row := range hibapi.TaskCommands() {
		if row.Laptop {
			want[row.Path] = true
		}
	}
	for _, name := range []string{"task", "recipe"} {
		group := root.sub(name)
		if group == nil {
			t.Errorf("hib has no %s group", name)
			continue
		}
		for _, c := range group.subs {
			if !want[c.path()] {
				t.Errorf("hib %s has no laptop row", c.path())
			}
			delete(want, c.path())
		}
	}
	for path := range want {
		t.Errorf("laptop row %s has no hib command", path)
	}
}

func TestTaskHelpIncludesTheDomainDocs(t *testing.T) {
	c, hib := homeless(t, newHost(t))
	var lines []string
	for _, p := range task.Phases() {
		lines = append(lines, fmt.Sprintf("  %s\n", p))
	}
	for _, o := range task.Outcomes() {
		lines = append(lines, fmt.Sprintf("  %s\n", o))
	}
	for _, name := range []string{"ls", "show"} {
		if code := hib("help", "task", name); code != 0 {
			t.Errorf("help task %s = exit %d, stderr %q", name, code, c.errOut)
			continue
		}
		for _, line := range lines {
			if !strings.Contains(c.out.String(), line) {
				t.Errorf("help task %s lacks %q", name, line)
			}
		}
		_, section, _ := strings.Cut(c.out.String(), "\nsteps:\n")
		section, _, _ = strings.Cut(section, "\nflags:\n")
		stepLines := strings.Split(strings.TrimSuffix(section, "\n"), "\n")
		if len(stepLines) != len(task.Steps()) {
			t.Errorf("help task %s has %d step lines, want %d", name, len(stepLines), len(task.Steps()))
			continue
		}
		column := -1
		for i, s := range task.Steps() {
			line := stepLines[i]
			pos := strings.Index(line, s.Doc())
			if pos < 0 || strings.TrimSpace(line[:pos]) != fmt.Sprint(s) {
				t.Errorf("help task %s lacks step %s and its doc %q", name, s, s.Doc())
			} else if column >= 0 && pos != column {
				t.Errorf("help task %s step %s doc starts at column %d, want %d", name, s, pos+1, column+1)
			} else {
				column = pos
			}
		}
	}
}

func TestTaskAndRecipeRunnersAreVisibleStubs(t *testing.T) {
	fixtures := map[string][]string{
		"task start": nil, "task ls": nil, "task show": {"t-abcdefgh"},
		"task logs": {"t-abcdefgh"}, "task wait": {"t-abcdefgh"}, "task cancel": {"t-abcdefgh"},
		"task rescue": {"t-abcdefgh"}, "task rm": {"t-abcdefgh"},
		"recipe put": {"check", "--", "true"}, "recipe ls": nil,
		"recipe show": {"check"}, "recipe rm": {"check"}, "recipe default": nil,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := newHost(t)
	c := initClient(t, h)
	root := commandTable()
	for _, cmd := range allCommands(root) {
		if !taskCommandIsStub(cmd) {
			continue
		}
		path := cmd.path()
		fixture, ok := fixtures[path]
		if !ok {
			t.Fatalf("%s has no invocation fixture", path)
		}
		args := append(strings.Fields(path), fixture...)
		code := c.hibContext(ctx, args...)
		want := "hib " + path + " isn't in this build yet"
		if code != 1 || c.out.Len() != 0 || !strings.Contains(c.errOut.String(), want) {
			t.Errorf("%s = exit %d, stdout %q, stderr %q; want offline stub %q",
				path, code, c.out, c.errOut, want)
		}
		if calls := h.takeCalls(); len(calls) != 0 {
			t.Errorf("%s stub contacted the host: %q", path, calls)
		}
	}
}

func taskCommandIsStub(c *command) bool {
	if !strings.HasPrefix(c.path(), "task ") && !strings.HasPrefix(c.path(), "recipe ") {
		return false
	}
	var help bytes.Buffer
	printHelp(&help, c)
	return strings.Contains(help.String(), "This command isn't in this build yet.")
}

func TestTaskAndRecipeGroupHelpDescribesTheGroup(t *testing.T) {
	c, hib := homeless(t, newHost(t))
	for _, group := range []string{"task", "recipe"} {
		if code := hib("help", group); code != 0 {
			t.Fatalf("help %s = exit %d, stderr %q", group, code, c.errOut)
		}
		if strings.Contains(c.out.String(), "These commands aren't in this build yet.") {
			t.Errorf("help %s claims the whole group is unavailable", group)
		}
	}
}

func TestTaskAndRecipeArgumentsAndFlags(t *testing.T) {
	root := commandTable()
	for _, group := range []string{"task", "recipe"} {
		g := root.sub(group)
		if g == nil {
			t.Fatalf("no %s group", group)
		}
		for _, c := range g.subs {
			fl := flag.NewFlagSet(c.path(), flag.ContinueOnError)
			c.flags(fl)
			hasJSON := fl.Lookup("json") != nil
			if hasJSON != (c.path() != "task logs") {
				t.Errorf("%s JSON flag = %v", c.path(), hasJSON)
			}
			if len(c.examples) == 0 || c.about == "" {
				t.Errorf("%s lacks examples or about text", c.path())
			}
		}
	}
	c, hib := homeless(t, newHost(t))
	for _, args := range [][]string{
		{"task", "start", "--prompt", "hello", "--file", "a", "--file", "b",
			"--image", "dev", "--size", "small", "--timeout", "1h",
			"--recipe", "check", "--request-id", "id", "--json", "--", "echo", "--flag"},
		{"task", "start", "--prompt-file", "prompt.txt"},
		{"task", "show", "--request", "id", "--json"},
		{"task", "logs", "t-abcdefgh", "--run", "2", "-f"},
		{"task", "wait", "t-abcdefgh", "--run", "2", "--json"},
		{"task", "rm", "t-abcdefgh", "--yes", "--request-id", "id", "--json"},
		{"recipe", "put", "check", "--image", "dev", "--size", "large", "--timeout", "30m", "--json", "--", "true"},
		{"recipe", "default", "check", "--json"},
	} {
		if code := hib(args...); code == 2 {
			t.Errorf("hib %q = exit %d, stderr %q; want no flag usage error", args, code, c.errOut)
		}
	}
	for _, args := range [][]string{
		{"task", "logs", "t-abcdefgh", "--json"},
		{"task", "start", "--repo", "solev/hibernaut"},
		{"task", "start", "--branch", "main"},
		{"task", "start", "--publish", "none"},
		{"task", "run", "t-abcdefgh"},
		{"task", "cleanup"},
	} {
		if code := hib(args...); code != 2 {
			t.Errorf("hib %q = exit %d; want usage error", args, code)
		}
	}
	if !slices.Contains(root.sub("task").names(), "wait") {
		t.Error("hib task lacks wait")
	}
}
