package hibapi

import (
	"slices"
	"strings"
	"testing"
)

func TestTaskCommandRows(t *testing.T) {
	want := []string{"task start", "task run", "task ls", "task show", "task logs",
		"task wait", "task cancel", "task rescue", "task rm", "task cleanup",
		"recipe put", "recipe ls", "recipe show", "recipe rm", "recipe default"}
	var paths []string
	for _, c := range TaskCommands() {
		paths = append(paths, c.Path)
		if c.Help == "" || !c.JSON {
			t.Errorf("%s lacks help or JSON support", c.Path)
		}
		if c.Laptop != (c.Path != "task run" && c.Path != "task cleanup") {
			t.Errorf("%s has wrong laptop availability", c.Path)
		}
		if c.Laptop && c.Summary == "" {
			t.Errorf("%s lacks a laptop summary", c.Path)
		}
		if c.E2B != (c.Path == "task ls" || c.Path == "task show") {
			t.Errorf("%s has wrong E2B use", c.Path)
		}
		if c.RequestID != slices.Contains([]string{"task start", "task run",
			"task cancel", "task rescue", "task rm"}, c.Path) {
			t.Errorf("%s has wrong request ID support", c.Path)
		}
		if strings.Count(c.Path, " ") != 1 {
			t.Errorf("%q is not a group and leaf", c.Path)
		}
	}
	if !slices.Equal(paths, want) {
		t.Errorf("command paths = %q, want %q", paths, want)
	}
	rows := TaskCommands()
	rows[0].Path = "changed"
	if got := TaskCommands()[0].Path; got != "task start" {
		t.Errorf("a caller changed the command table to %q", got)
	}
}
