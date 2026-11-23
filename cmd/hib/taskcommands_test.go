package main

import (
	"flag"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

func TestTaskCommandsStayInSync(t *testing.T) {
	for group, commands := range map[string]map[string]*command{"task": taskCommands(), "recipe": recipeCommands()} {
		for _, row := range hibapi.TaskCommands() {
			name, ok := strings.CutPrefix(row.Path, group+" ")
			if !ok || !row.Laptop {
				continue
			}
			if commands[name] == nil {
				t.Errorf("laptop row %s has no hib command", row.Path)
			}
			delete(commands, name)
		}
		for name := range commands {
			t.Errorf("hib %s %s has no laptop row", group, name)
		}
	}
	if t.Failed() {
		t.Fatal("command table drift")
	}
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

func TestTaskAndRecipeGroupHelpDescribesTheGroup(t *testing.T) {
	c, hib := homeless(t, newHost(t))
	for _, tc := range []struct{ group, about string }{
		{"task", "Runs commands in their own machines under hibd's supervision."},
		{"recipe", "Keeps named commands and their image, size and timeout defaults for hib task start."},
	} {
		if code := hib("help", tc.group); code != 0 || !strings.Contains(c.out.String(), tc.about) {
			t.Fatalf("help %s = exit %d, stdout %q, stderr %q; want %q",
				tc.group, code, c.out, c.errOut, tc.about)
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
			if !hasJSON {
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
		{"task", "logs", "t-abcdefgh", "--json"},
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
