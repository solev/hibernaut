package slots_test

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/slots"
)

func TestFinderMapsSlotSourceToSandbox(t *testing.T) {
	host := newHost(t)
	host.machine("machineone", 1, 101)
	host.machine("machinetwo", 2, 202)

	got, err := host.finder.Sandbox(netip.MustParseAddr("10.11.0.2"), netip.MustParseAddr("10.12.0.4"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "machinetwo" {
		t.Errorf("Sandbox = %q, want machinetwo", got)
	}
}

func TestFinderRejectsMismatchedLocalAddress(t *testing.T) {
	host := newHost(t)
	host.machine("machineone", 1, 101)
	host.machine("machinetwo", 2, 202)

	_, err := host.finder.Sandbox(netip.MustParseAddr("10.11.0.2"), netip.MustParseAddr("10.12.0.2"))
	if err == nil {
		t.Fatal("Sandbox succeeded with source slot 2 and local slot 1, want an error")
	}
}

func TestFinderReturnsClearErrorForUnknownSlot(t *testing.T) {
	host := newHost(t)
	host.machine("machineone", 1, 101)

	_, err := host.finder.Sandbox(netip.MustParseAddr("10.11.0.2"), netip.MustParseAddr("10.12.0.4"))
	if err == nil {
		t.Fatal("Sandbox succeeded for an unused slot, want an error")
	}
}

func TestFinderSlotIsTheReverseOfSandbox(t *testing.T) {
	host := newHost(t)
	host.machine("machineone", 1, 101)
	host.machine("machinetwo", 258, 202)

	slot, err := host.finder.Slot("machinetwo")
	if err != nil {
		t.Fatal(err)
	}
	if slot != 258 {
		t.Fatalf("Slot = %d, want 258", slot)
	}
	addr, err := slots.Addr(slot)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddr("10.11.1.2"); addr != want {
		t.Fatalf("Addr(%d) = %s, want %s", slot, addr, want)
	}
	id, err := host.finder.Sandbox(addr, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	if id != "machinetwo" {
		t.Errorf("Sandbox(Addr(Slot(machinetwo))) = %q", id)
	}
}

func TestFinderSlotMatchesTheWholeSandboxID(t *testing.T) {
	host := newHost(t)
	host.machine("machine-x", 2, 202)
	host.machine("machine", 1, 101)

	slot, err := host.finder.Slot("machine")
	if err != nil {
		t.Fatal(err)
	}
	if slot != 1 {
		t.Errorf("Slot(machine) = %d, want 1, not machine-x's 2", slot)
	}
	for _, id := range []string{"", "*", "machine*", "../machine", "mach[i]ne"} {
		if slot, err := host.finder.Slot(id); err == nil {
			t.Errorf("Slot(%q) = %d, want an error", id, slot)
		}
	}
}

func TestFinderSlotFailsForSandboxWithNoProcess(t *testing.T) {
	host := newHost(t)
	host.machine("machineone", 1, 101)
	cgroup := filepath.Join(host.finder.Cgroups, "sbx-machinetwo-runone")
	if err := os.MkdirAll(cgroup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cgroup, "cgroup.procs"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"machinetwo", "machinethree"} {
		if slot, err := host.finder.Slot(id); err == nil {
			t.Errorf("Slot(%s) = %d, want an error", id, slot)
		}
	}
}

func TestFinderSlotFailsForSlotWithNoNetns(t *testing.T) {
	host := newHost(t)
	host.machine("machineone", 1, 101)
	if err := os.Remove(filepath.Join(host.finder.Netns, "ns-1")); err != nil {
		t.Fatal(err)
	}

	if slot, err := host.finder.Slot("machineone"); err == nil {
		t.Fatalf("Slot = %d with no ns-1, want an error", slot)
	}
}

func TestAddrIsTheInverseOfTheSourceSlot(t *testing.T) {
	for slot, want := range map[int]string{1: "10.11.0.1", 2: "10.11.0.2", 258: "10.11.1.2", 65535: "10.11.255.255"} {
		got, err := slots.Addr(slot)
		if err != nil {
			t.Errorf("Addr(%d): %v", slot, err)
			continue
		}
		if got != netip.MustParseAddr(want) {
			t.Errorf("Addr(%d) = %s, want %s", slot, got, want)
		}
	}
	for _, slot := range []int{-1, 0, 65536} {
		if got, err := slots.Addr(slot); err == nil {
			t.Errorf("Addr(%d) = %s, want an error", slot, got)
		}
	}
}

func TestResolverAddrReturnsSlotAndRun(t *testing.T) {
	host := newHost(t)
	host.machine("machineone", 3, 101)
	r := slots.Resolver{Finder: host.finder, Runs: newRuns(map[string][]links.Run{"machineone": {"runone"}})}

	addr, run, err := r.Addr("machineone")
	if err != nil {
		t.Fatal(err)
	}
	if addr != netip.MustParseAddr("10.11.0.3") || run != "runone" {
		t.Errorf("Addr = %s, %s, want 10.11.0.3, runone", addr, run)
	}

	slot, err := r.At(netip.MustParseAddr("10.11.0.3"))
	if err != nil {
		t.Fatal(err)
	}
	if slot != (links.Slot{ID: "machineone", Run: "runone"}) {
		t.Errorf("At = %+v, want machineone at runone", slot)
	}
}

func TestResolverFailsWhenTheRunChangesBetweenReads(t *testing.T) {
	tests := []struct {
		name string
		runs []links.Run
		proc bool
	}{
		{name: "not running", runs: nil, proc: true},
		{name: "run changed", runs: []links.Run{"runone", "runtwo"}, proc: true},
		{name: "run changed and slot gone", runs: []links.Run{"runone", "runtwo"}, proc: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := newHost(t)
			if tt.proc {
				host.machine("machineone", 3, 101)
			}
			r := slots.Resolver{Finder: host.finder, Runs: newRuns(map[string][]links.Run{"machineone": tt.runs})}
			if addr, run, err := r.Addr("machineone"); !errors.Is(err, links.ErrNotRunning) {
				t.Errorf("Addr = %s, %s, %v, want ErrNotRunning", addr, run, err)
			}
			if !tt.proc {
				return
			}
			r.Runs = newRuns(map[string][]links.Run{"machineone": tt.runs})
			if slot, err := r.At(netip.MustParseAddr("10.11.0.3")); !errors.Is(err, links.ErrNotRunning) {
				t.Errorf("At = %+v, %v, want ErrNotRunning", slot, err)
			}
		})
	}
}

// fakeRuns answers Current from a list per machine, one entry per call, repeating the last.
type fakeRuns struct {
	runs  map[string][]links.Run
	calls map[string]int
}

func newRuns(runs map[string][]links.Run) *fakeRuns {
	return &fakeRuns{runs: runs, calls: map[string]int{}}
}

func (r *fakeRuns) Current(id string) (links.Run, error) {
	list := r.runs[id]
	if len(list) == 0 {
		return "", links.ErrNotRunning
	}
	i := min(r.calls[id], len(list)-1)
	r.calls[id]++
	return list[i], nil
}

func (r *fakeRuns) Watch(string, links.Run) (<-chan struct{}, error) {
	return nil, errors.New("fakeRuns: Watch is not used")
}

func (r *fakeRuns) Live() (map[string]links.Run, error) {
	return nil, errors.New("fakeRuns: Live is not used")
}

type fakeHost struct {
	t      *testing.T
	root   string
	finder slots.Finder
}

func newHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	h := &fakeHost{
		t:    t,
		root: root,
		finder: slots.Finder{
			Proc:    filepath.Join(root, "proc"),
			Netns:   filepath.Join(root, "run", "netns"),
			Cgroups: filepath.Join(root, "sys", "fs", "cgroup", "e2b"),
		},
	}
	for _, dir := range []string{h.finder.Proc, h.finder.Netns, h.finder.Cgroups} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *fakeHost) machine(id string, slot int, pid int) {
	h.t.Helper()
	ns := filepath.Join(h.finder.Netns, fmt.Sprintf("ns-%d", slot))
	if err := os.WriteFile(ns, []byte{}, 0o644); err != nil {
		h.t.Fatal(err)
	}
	info, err := os.Stat(ns)
	if err != nil {
		h.t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)

	procNS := filepath.Join(h.finder.Proc, fmt.Sprint(pid), "ns")
	if err := os.MkdirAll(procNS, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Symlink(fmt.Sprintf("net:[%d]", stat.Ino), filepath.Join(procNS, "net")); err != nil {
		h.t.Fatal(err)
	}

	cgroup := filepath.Join(h.finder.Cgroups, fmt.Sprintf("sbx-%s-runone", id))
	if err := os.MkdirAll(cgroup, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cgroup, "cgroup.procs"), []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		h.t.Fatal(err)
	}
}
