package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

func taskCommands() map[string]*command {
	commands := taskReadCommands()
	commands["start"] = taskStartCommand()
	for name, c := range taskFollowCommands() {
		commands[name] = c
	}
	return commands
}

func taskGroupCommand(group string, commands map[string]*command, about string, examples []string) *command {
	c := &command{name: group, about: about, examples: examples, subs: []*command{}}
	for _, row := range hibapi.TaskCommands() {
		name, ok := strings.CutPrefix(row.Path, group+" ")
		if !ok || !row.Laptop {
			continue
		}
		leaf := commands[name]
		if leaf == nil {
			panic("laptop row " + row.Path + " has no hib command")
		}
		leaf.name, leaf.summary = name, row.Summary
		c.subs = append(c.subs, leaf)
		delete(commands, name)
	}
	for name := range commands {
		panic("hib " + group + " " + name + " has no laptop row")
	}
	return c
}

func taskDocs() string {
	var b strings.Builder
	b.WriteString("\n\nphases:\n")
	for _, p := range task.Phases() {
		fmt.Fprintf(&b, "  %s\n", p)
	}
	b.WriteString("\noutcomes:\n")
	for _, o := range task.Outcomes() {
		fmt.Fprintf(&b, "  %s\n", o)
	}
	b.WriteString("\nsteps:\n")
	for _, s := range task.Steps() {
		fmt.Fprintf(&b, "  %s  %s\n", s, s.Doc())
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func taskNotInBuild(_ context.Context, a *app, _ []string) int {
	fmt.Fprintln(a.stderr, a.command.title()+" isn't in this build yet")
	return 1
}
