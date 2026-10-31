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

func TestREADMEExplainsUnavailableTasksAndRecipes(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"task", "recipe"} {
		_, paragraph, ok := strings.Cut(string(data), "\n`hib "+group+"` ")
		paragraph, _, _ = strings.Cut(paragraph, "\n\n")
		if !ok || !strings.Contains(paragraph, "this build") ||
			!strings.Contains(paragraph, "exits 1") || !strings.Contains(paragraph, "without contacting the host") {
			t.Errorf("README's hib %s paragraph must say this build exits 1 without contacting the host", group)
		}
		if strings.Contains(paragraph, "declares commands") || strings.Contains(paragraph, "units") {
			t.Errorf("README's hib %s paragraph uses implementation jargon", group)
		}
	}
}
