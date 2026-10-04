package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
)

func TestForkPrintsCopiesAndLsShowsTheirOrigin(t *testing.T) {
	h := imageHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "web"); code != 0 {
		t.Fatalf("new = %d; stderr %s", code, c.errOut)
	}
	run := c.ssh
	c.ssh = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Error("fork SSH must not time out before the Manager returns")
		}
		return run(ctx, args, stdin, stdout, stderr)
	}
	if code := c.hib("fork", "web", "-count", "2"); code != 0 {
		t.Fatalf("fork = %d; stderr %s", code, c.errOut)
	}
	c.ssh = run
	if c.out.String() != "made web-1, web-2 from web. connect with: ssh web-1.hib\n" {
		t.Fatalf("fork output = %q", c.out)
	}
	if calls := remoteCommands(h.takeCalls()); !slices.Contains(calls, "sudo -n hibd 'fork' 'web' '-count' '2' '-json'") {
		t.Fatalf("fork must use root: calls %q", calls)
	}
	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls = %d; stderr %s", code, c.errOut)
	}
	lines := tableLines(c.out.String())
	header := strings.Fields(lines[0])
	from := slices.Index(header, "FROM")
	if from < 0 {
		t.Fatalf("ls header has no FROM column: %q", header)
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		want := "-"
		if strings.HasPrefix(fields[0], "web-") {
			want = "web"
		}
		if fields[from] != want {
			t.Errorf("ls row %q, want FROM %s", fields, want)
		}
	}
	if code := c.hib("pause", "web"); code != 0 {
		t.Fatalf("pause = %d; stderr %s", code, c.errOut)
	}
	if code := c.hib("fork", "web"); code != 0 || c.out.String() != "made web-3 from web. connect with: ssh web-3.hib\n" {
		t.Fatalf("default fork = %d; stdout %q; stderr %s", code, c.out, c.errOut)
	}
	if origin, err := h.engine.Get(t.Context(), machineID(t, h, "web")); err != nil || origin.State != engine.Paused {
		t.Fatalf("origin = %+v, %v, want paused", origin, err)
	}
}

func TestForkPrintsMadeCopiesBeforeItsError(t *testing.T) {
	h := imageHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "web"); code != 0 {
		t.Fatalf("new = %d; stderr %s", code, c.errOut)
	}
	h.engine.FailCreateAfterStart(map[string]string{"hib.name": "web-2"})
	if code := c.hib("fork", "web", "-count", "3"); code != 1 {
		t.Fatalf("fork = %d, want 1; stderr %s", code, c.errOut)
	}
	if c.out.String() != "made web-1 from web. connect with: ssh web-1.hib\n" ||
		!strings.Contains(c.errOut.String(), "couldn't make web-2") {
		t.Fatalf("partial fork stdout %q; stderr %q", c.out, c.errOut)
	}
	ms, err := h.engine.List(t.Context(), engine.ListOptions{})
	if err != nil || len(ms) != 2 {
		t.Fatalf("machines = %+v, %v, want origin and first copy", ms, err)
	}
}
