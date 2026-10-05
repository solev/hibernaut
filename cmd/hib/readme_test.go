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
