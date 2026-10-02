//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

// TestHostFull fills the host's memory with running machines. One more machine, and waking a
// paused one, are then refused at once with a message that says what to do, and the running
// machines are left alone. Pausing one of them makes room again.
func TestHostFull(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	paused := newHibMachine(t, ctx)
	hib(t, ctx, "pause", paused)

	// Start machines until hib ls says no more fit. Timing ls gives the trip to the host.
	var running []string
	var lsTook time.Duration
	for {
		start := time.Now()
		ls := hib(t, ctx, "ls")
		lsTook = time.Since(start)
		if strings.Contains(ls, "\nNo room for another dev machine") {
			break
		}
		if !strings.Contains(ls, "\nRoom for ") || len(running) == 6 {
			t.Fatalf("after %d more machines, hib ls says:\n%s", len(running), ls)
		}
		running = append(running, newHibMachine(t, ctx))
	}
	if len(running) == 0 {
		t.Fatal("the host was full before the test started")
	}
	t.Logf("%d running machines filled the host", len(running))

	extra := fmt.Sprintf("e2e-%08x", rand.Uint32())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		runHib(ctx, "rm", extra)
	})
	start := time.Now()
	_, stderr, code := runHib(ctx, "new", extra)
	wantNoRoom(t, "hib new", code != 0, stderr, extra, running, time.Since(start), lsTook)

	start = time.Now()
	_, stderr, err := sshResult(ctx, nil, paused, "true")
	wantNoRoom(t, "ssh to a paused machine", err != nil, stderr, paused, running, time.Since(start), lsTook)

	for _, name := range running {
		if m, err := vm.engine.Get(ctx, machineID(t, ctx, name)); err != nil || m.State != engine.Running {
			t.Errorf("%s is %s after the refusals, want running (%v)", name, m.State, err)
		}
	}
	if m, err := vm.engine.Get(ctx, machineID(t, ctx, paused)); err != nil || m.State != engine.Paused {
		t.Errorf("%s is %s after the refusal, want paused (%v)", paused, m.State, err)
	}
	if ms, err := vm.engine.List(ctx, engine.ListOptions{Metadata: map[string]string{"hib.name": extra}}); err != nil || len(ms) != 0 {
		t.Errorf("the refused hib new made %d machines (%v)", len(ms), err)
	}

	// Pausing a running machine frees its memory, and then the paused one wakes.
	hib(t, ctx, "pause", running[0])
	waitFor(t, ctx, "hib ls to show room for a machine", time.Minute, func() bool {
		return strings.Contains(hib(t, ctx, "ls"), "\nRoom for ")
	})
	if out := sshMachine(t, ctx, paused, "echo woke"); strings.TrimSpace(out) != "woke" {
		t.Errorf("ssh to %s once there's room printed %q, want woke", paused, out)
	}
}

// wantNoRoom checks that a start was refused with the message for a full host, which names the
// running machines and says what to do, about as fast as hib ls answers.
func wantNoRoom(t *testing.T, what string, failed bool, stderr, name string, running []string, took, lsTook time.Duration) {
	t.Helper()
	t.Logf("%s was refused in %v (hib ls takes %v)", what, took.Round(time.Millisecond), lsTook.Round(time.Millisecond))
	if !failed || !strings.Contains(stderr, `no room on the host for "`+name+`"`) || !strings.Contains(stderr, "hib pause NAME") {
		t.Errorf("%s: failed %v, stderr %q; want the message for a full host", what, failed, stderr)
	}
	for _, r := range running {
		if !strings.Contains(stderr, r) {
			t.Errorf("%s: the message doesn't name running machine %s: %q", what, r, stderr)
		}
	}
	if took > lsTook+time.Second {
		t.Errorf("%s took %v to be refused; hib ls takes %v", what, took.Round(time.Millisecond), lsTook.Round(time.Millisecond))
	}
}
