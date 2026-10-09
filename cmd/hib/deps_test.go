package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestHibdLinksNoTerminalUI(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../hibd").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatal("go list found no packages")
	}
	for _, dep := range deps {
		if strings.HasPrefix(dep, "charm.land/") || strings.HasPrefix(dep, "github.com/charmbracelet/") ||
			dep == "github.com/solev/hibernaut/internal/bot" {
			t.Errorf("hibd links %s", dep)
		}
	}
}
