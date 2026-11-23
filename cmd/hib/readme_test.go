package main

import (
	"os"
	"strings"
	"testing"
)

func TestREADMEHasTheCommandReference(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), "\n### Command reference\n")
	if !ok {
		t.Fatal("README.md has no ### Command reference section")
	}
	_, block, ok := strings.Cut(section, "\n```text\n")
	block, _, closed := strings.Cut(block, "\n```\n")
	if !ok || !closed {
		t.Fatal("the command reference has no ```text block")
	}
	c, hib := homeless(t, newHost(t))
	if code := hib("help"); code != 0 {
		t.Fatalf("hib help = %d: %s", code, c.errOut)
	}
	if want := strings.TrimSuffix(c.out.String(), "\n"); block != want {
		t.Errorf("README.md's command reference differs from hib help. Paste this in:\n%s", want)
	}
}

func TestREADMEExplainsTasksAndRecipes(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	heading := "\n### Tasks and recipes (`hib task`, `hib recipe`)\n"
	_, section, ok := strings.Cut(text, heading)
	section, _, _ = strings.Cut(section, "\n### ")
	if !ok {
		t.Fatal("README has no Tasks and recipes section")
	}
	if strings.Index(text, heading) < strings.Index(text, "\nUseful extras:") ||
		strings.Contains(section, "create-vm.sh") || strings.Contains(section, "Useful extras:") {
		t.Error("README's Tasks section must follow Useful extras and leave the Azure blocks with AI tools")
	}
	paragraphs := strings.Split(section, "\n\n")
	for _, c := range allCommands(commandTable()) {
		if !strings.HasPrefix(c.path(), "task ") && !strings.HasPrefix(c.path(), "recipe ") {
			continue
		}
		for _, paragraph := range paragraphs {
			paragraph = strings.ReplaceAll(paragraph, "\n", " ")
			if !strings.Contains(paragraph, "`hib "+c.path()+"`") {
				continue
			}
			if strings.Contains(paragraph, "declares commands") || strings.Contains(paragraph, "units") {
				t.Errorf("README's hib %s paragraph uses implementation jargon", c.path())
			}
		}
	}
}
