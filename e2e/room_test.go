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

	var running []string
	for {
		ls := hib(t, ctx, "ls")
		line := roomLine(ls)
		if strings.HasPrefix(line, "No room for another machine") ||
			strings.HasPrefix(line, "Room for more machines: ") && !strings.Contains(line, " medium") {
			break
		}
		if !strings.HasPrefix(line, "Room for more machines: ") || len(running) == 6 {
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
		runHib(ctx, "rm", extra, "--yes")
	})
	wantNoRoom(t, ctx, "hib new", extra, running, func(ctx context.Context) (bool, string) {
		_, stderr, code := runHib(ctx, "new", extra)
		return code != 0, stderr
	})
	wantNoRoom(t, ctx, "ssh to a paused machine", paused, running, func(ctx context.Context) (bool, string) {
		_, stderr, err := sshResult(ctx, nil, paused, "true")
		return err != nil, stderr
	})

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

	hib(t, ctx, "pause", running[0])
	waitFor(t, ctx, "hib ls to show room for a medium machine", time.Minute, func() bool {
		line := roomLine(hib(t, ctx, "ls"))
		return strings.HasPrefix(line, "Room for more machines: ") && strings.Contains(line, " medium")
	})
	if out := sshMachine(t, ctx, paused, "echo woke"); strings.TrimSpace(out) != "woke" {
		t.Errorf("ssh to %s once there's room printed %q, want woke", paused, out)
	}
}

func roomLine(ls string) string {
	_, line, _ := strings.Cut(ls, "\n\n")
	return strings.TrimSpace(line)
}

func wantNoRoom(t *testing.T, ctx context.Context, what, name string, running []string, attempt func(context.Context) (bool, string)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	start := time.Now()
	failed, stderr := attempt(ctx)
	if err := ctx.Err(); err != nil {
		t.Fatalf("waiting for %s to be refused: %v", what, err)
	}
	t.Logf("%s was refused in %v", what, time.Since(start).Round(time.Millisecond))
	if !failed || !strings.Contains(stderr, `no room on the host for "`+name+`"`) || !strings.Contains(stderr, "hib pause NAME") {
		t.Errorf("%s: failed %v, stderr %q; want the message for a full host", what, failed, stderr)
	}
	for _, r := range running {
		if !strings.Contains(stderr, r) {
			t.Errorf("%s: the message doesn't name running machine %s: %q", what, r, stderr)
		}
	}
}
