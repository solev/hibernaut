//go:build e2e

package e2e

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
)

// TestSizes checks that hib new --size small makes a machine with 1 vCPU and about 1 GiB that
// stays small across a pause and a wake, that a machine made without --size is still medium, and
// that every size's template has its size's CPUs and RAM, without starting a large machine.
func TestSizes(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	small := newHibMachine(t, ctx, "--size", "small")
	requireSize(t, ctx, small, "small", 1, 0.75, 1.0)

	hib(t, ctx, "pause", small)
	if row := nameRow(hib(t, ctx, "ls"), small); !strings.Contains(row, "paused") {
		t.Fatalf("hib ls row for %s = %q, want it paused", small, row)
	}
	if out := hib(t, ctx, "exec", small, "--", "echo", "woke"); strings.TrimSpace(out) != "woke" {
		t.Errorf("hib exec on a paused machine printed %q, want woke", out)
	}
	requireSize(t, ctx, small, "small", 1, 0.75, 1.0)

	medium := newHibMachine(t, ctx)
	requireSize(t, ctx, medium, "medium", 2, 1.75, 2.0)

	for _, s := range images.Sizes() {
		alias := images.SizeTemplate("dev", s)
		info, err := vm.engine.Template(ctx, alias)
		if err != nil {
			t.Errorf("template %s: %v", alias, err)
			continue
		}
		if info.CPUs != s.CPUs() || info.MemoryMB != s.MemoryMB() {
			t.Errorf("template %s has %d vCPU and %d MiB, want %d and %d", alias, info.CPUs, info.MemoryMB, s.CPUs(), s.MemoryMB())
		}
	}
}

func requireSize(t *testing.T, ctx context.Context, name, size string, cpus int, minGiB, maxGiB float64) {
	t.Helper()
	out := hib(t, ctx, "exec", name, "--", "nproc && grep MemTotal: /proc/meminfo")
	f := strings.Fields(out)
	if len(f) != 4 {
		t.Fatalf("%s: nproc and MemTotal printed %q", name, out)
	}
	nproc, err := strconv.Atoi(f[0])
	if err != nil {
		t.Fatalf("%s: nproc printed %q", name, f[0])
	}
	kib, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		t.Fatalf("%s: MemTotal is %q", name, f[2])
	}
	if gib := float64(kib) / (1 << 20); nproc != cpus || gib < minGiB || gib > maxGiB {
		t.Errorf("%s has %d vCPU and %.2f GiB, want %d vCPU and %.2f to %.2f GiB", name, nproc, gib, cpus, minGiB, maxGiB)
	}

	ls := hib(t, ctx, "ls")
	column := slices.Index(strings.Fields(strings.SplitN(ls, "\n", 2)[0]), "SIZE")
	if row := strings.Fields(nameRow(ls, name)); column < 0 || len(row) <= column || row[column] != size {
		t.Errorf("hib ls lists %s as %q, want size %s:\n%s", name, row, size, ls)
	}
}
