//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

// TestPauseAll runs the deployed hibd on the VM, as idle-deallocate, quiesce and the pause-all
// guard do. It pauses every running machine on the VM, not only the test's.
func TestPauseAll(t *testing.T) {
	ctx := testContext(t, 5*time.Minute)
	running := newMachine(t, ctx)
	paused := newMachine(t, ctx)
	if err := vm.engine.Pause(ctx, paused.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	sh(t, ctx, running.ID, "echo kept > marker")

	// idle-deallocate's check for running machines.
	const listRunning = "hibd ls --state running -q"
	if out := mustSSH(t, ctx, listRunning); !slices.Equal(strings.Fields(out), []string{running.ID}) {
		t.Errorf("%s = %q, want only %s", listRunning, out, running.ID)
	}

	out, err := vm.ssh(ctx, "hibd pause-all --timeout 2m 2>&1")
	t.Logf("hibd pause-all:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{running.ID, paused.ID} {
		m, err := vm.engine.Get(ctx, id)
		if err != nil || m.State != engine.Paused {
			t.Errorf("after pause-all: Get(%s) = %+v, %v; want paused", id, m, err)
		}
	}
	if out := mustSSH(t, ctx, listRunning); out != "" {
		t.Errorf("%s after pause-all = %q, want nothing", listRunning, out)
	}

	if _, err := vm.engine.Resume(ctx, running.ID, 10*time.Minute); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := sh(t, ctx, running.ID, "cat marker"); got != "kept\n" {
		t.Errorf("marker after pause-all and resume = %q, want %q", got, "kept\n")
	}
}
