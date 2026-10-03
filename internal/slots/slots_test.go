package slots_test

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"testing"

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
