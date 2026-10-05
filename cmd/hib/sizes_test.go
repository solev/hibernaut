package main

import (
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestNewSize(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("image", "build", "dev"); code != 0 {
		t.Fatalf("image build: exit %d: %s", code, c.errOut)
	}
	if want := "built image dev in sizes medium, small and large\n"; c.out.String() != want {
		t.Errorf("image build printed %q, want %q", c.out, want)
	}
	h.takeCalls()
	for _, args := range [][]string{{"new", "tiny", "--size", "small"}, {"new", "big", "--size=large"}, {"new", "plain"}} {
		if code := c.hib(args...); code != 0 {
			t.Fatalf("%q: exit %d: %s", args, code, c.errOut)
		}
	}
	want := []string{
		"hibd new tiny --image dev --size small --json",
		"hibd new big --image dev --size large --json",
		"hibd new plain --image dev --json",
	}
	if got := remoteCommands(h.takeCalls()); !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want %q", got, want)
	}
	for name, memoryMB := range map[string]int{"tiny": 1024, "big": 4096, "plain": 2048} {
		if m, _ := h.engine.Get(t.Context(), machineID(t, h, name)); m.MemoryMB != memoryMB {
			t.Errorf("%s has %d MiB, want %d", name, m.MemoryMB, memoryMB)
		}
	}

	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls: exit %d: %s", code, c.errOut)
	}
	sizes := map[string]string{}
	for _, line := range tableLines(c.out.String())[1:] {
		f := strings.Fields(line)
		sizes[f[0]] = f[3]
	}
	if want := map[string]string{"tiny": "small", "big": "large", "plain": "medium"}; !maps.Equal(sizes, want) {
		t.Errorf("SIZE column = %v, want %v; ls =\n%s", sizes, want, c.out)
	}
}

func TestBadSizesNeverReachTheHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	for _, size := range []string{"", "huge", "Small", "small;reboot", "small --image x"} {
		code := c.hib("new", "app", "--size", size)
		if code != 2 || !strings.HasSuffix(c.errOut.String(), "(there is: small, medium, large)\n") {
			t.Errorf("new --size %q: exit %d, stderr %q; want 2 and the sizes", size, code, c.errOut)
		}
	}
	if code := c.hib("new", "app", "--size"); code != 2 {
		t.Errorf("new --size with no value: exit %d, want 2", code)
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("bad sizes reached the host: %q", calls)
	}
}

func TestSizesWithAnOlderHibd(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	var newWords []string
	h.override = func(words []string, stdout, stderr io.Writer) (int, bool) {
		switch words[0] {
		case "new":
			newWords = words
			if slices.Contains(words, "--size") {
				io.WriteString(stderr, "flag provided but not defined: -size\n")
				return 2, true
			}
			return 0, false
		case "ls":
			return writeTestResponse(stdout, hibapi.Response{
				Machines: []hibapi.Machine{{Name: "web", ID: "web1", Image: "dev", State: "running", StartedAt: now, EndAt: now.Add(time.Hour)}},
				Room:     &hibapi.Room{Fits: 2, Image: "dev", MemoryFreeMB: 4202, MemoryMB: 6400, DiskFreeMB: 79 << 10},
			}), true
		case "image":
			return writeTestResponse(stdout, hibapi.Response{Template: "tmpl-hib-dev"}), true
		}
		return 0, false
	}
	if code := c.hib("new", "web"); code != 0 || slices.Contains(newWords, "--size") {
		t.Errorf("new web: exit %d, sent %q; stderr %s", code, newWords, c.errOut)
	}
	if m, _ := h.engine.Get(t.Context(), machineID(t, h, "web")); m.Template != "hib-dev" {
		t.Errorf("web is from %q, want hib-dev", m.Template)
	}
	if code := c.hib("new", "tiny", "--size", "small"); code != 1 || !strings.Contains(c.errOut.String(), "flag provided but not defined: -size") {
		t.Errorf("new --size against an older hibd: exit %d, stderr %q; want hibd's refusal", code, c.errOut)
	}

	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls: exit %d: %s", code, c.errOut)
	}
	table, line, _ := strings.Cut(c.out.String(), "\n\n")
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if len(lines) != 2 || strings.Fields(lines[1])[3] != "-" {
		t.Errorf("ls =\n%s\nwant web with no size", c.out)
	}
	if want := "Room for 2 more dev machines: 4.1 GiB of 6.2 GiB memory free, 79 GiB disk free."; strings.TrimSpace(line) != want {
		t.Errorf("room line = %q, want today's %q", line, want)
	}

	if code := c.hib("image", "build", "dev"); code != 0 || c.out.String() != "built image dev (template tmpl-hib-dev)\n" {
		t.Errorf("image build: exit %d, printed %q; want today's line", code, c.out)
	}
}
