//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

// TestQuiescing checks the drain marker: while the host is going to sleep, hib new is refused
// with an explanation and leaves nothing behind, but hib ls still works.
func TestQuiescing(t *testing.T) {
	ctx := testContext(t, 3*time.Minute)
	if vm.engine == nil {
		if err := vm.openTunnel(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mustSSH(t, ctx, "sudo mkdir -p /run/hibernaut && sudo touch /run/hibernaut/quiescing")
	t.Cleanup(func() {
		// A fresh context: the test's ctx is cancelled by the time cleanups run, and a lingering
		// marker would make every later hib new fail.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := vm.ssh(ctx, "sudo rm -f /run/hibernaut/quiescing"); err != nil {
			t.Errorf("removing the quiescing marker: %v", err)
		}
	})

	name := "e2e-drain-probe"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		runHib(ctx, "rm", name, "--yes")
	})

	_, stderr, code := runHib(ctx, "new", name)
	if code == 0 {
		t.Error("hib new succeeded while the host was draining, want it refused")
	}
	if !strings.Contains(stderr, "going to sleep") {
		t.Errorf("hib new didn't say the host is draining:\n%s", stderr)
	}

	ms, err := vm.engine.List(ctx, engine.ListOptions{Metadata: map[string]string{"hib.name": name}})
	if err != nil {
		t.Fatalf("listing machines: %v", err)
	}
	if len(ms) > 0 {
		t.Errorf("a refused hib new left %d machine(s) behind", len(ms))
	}

	if _, _, code := runHib(ctx, "ls"); code != 0 {
		t.Errorf("hib ls exited %d while the host was draining, want it to work", code)
	}
}
