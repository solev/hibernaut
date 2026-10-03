// Package slots maps an intercepted E2B slot connection back to its sandbox.
package slots

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Finder reads host state. Empty roots use the real host paths.
type Finder struct {
	Proc    string
	Netns   string
	Cgroups string
	Net     string
}

// Sandbox returns the E2B sandbox ID that owns an intercepted connection.
func (f Finder) Sandbox(src, local netip.Addr) (string, error) {
	slot, err := slotFromSource(src)
	if err != nil {
		return "", err
	}
	if local.IsValid() {
		localSlot, ok := slotFromLocal(local)
		if !ok {
			return "", fmt.Errorf("slots: local address %s is not an E2B veth host address", local)
		}
		if localSlot != slot {
			return "", fmt.Errorf("slots: source address %s is slot %d but local address %s is slot %d", src, slot, local, localSlot)
		}
	}

	netns, err := f.netnsInode(slot)
	if err != nil {
		return "", err
	}
	id, err := f.sandboxForNetns(netns)
	if err != nil {
		return "", fmt.Errorf("slots: slot %d: %w", slot, err)
	}
	return id, nil
}

func (f Finder) procRoot() string {
	if f.Proc == "" {
		return "/proc"
	}
	return f.Proc
}

func (f Finder) netnsRoot() string {
	if f.Netns == "" {
		return "/run/netns"
	}
	return f.Netns
}

func (f Finder) cgroupsRoot() string {
	if f.Cgroups == "" {
		return "/sys/fs/cgroup/e2b"
	}
	return f.Cgroups
}

func slotFromSource(addr netip.Addr) (int, error) {
	if !addr.Is4() {
		return 0, fmt.Errorf("slots: source address %s is not an E2B IPv4 slot address", addr)
	}
	a := addr.As4()
	if a[0] != 10 || a[1] != 11 {
		return 0, fmt.Errorf("slots: source address %s is not in E2B host slot range 10.11.0.0/16", addr)
	}
	slot := int(a[2])<<8 | int(a[3])
	if slot == 0 {
		return 0, fmt.Errorf("slots: source address %s is slot 0, not a sandbox slot", addr)
	}
	return slot, nil
}

func slotFromLocal(addr netip.Addr) (int, bool) {
	if !addr.Is4() {
		return 0, false
	}
	a := addr.As4()
	if a[0] != 10 || a[1] != 12 {
		return 0, false
	}
	n := int(a[2])<<8 | int(a[3])
	if n == 0 || n%2 != 0 {
		return 0, false
	}
	return n / 2, true
}

func (f Finder) netnsInode(slot int) (uint64, error) {
	path := filepath.Join(f.netnsRoot(), fmt.Sprintf("ns-%d", slot))
	inode, err := inode(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("slots: no network namespace for slot %d at %s", slot, path)
		}
		return 0, fmt.Errorf("slots: reading %s: %w", path, err)
	}
	return inode, nil
}

func (f Finder) sandboxForNetns(netns uint64) (string, error) {
	dirs, err := filepath.Glob(filepath.Join(f.cgroupsRoot(), "sbx-*-*"))
	if err != nil {
		return "", err
	}
	for _, dir := range dirs {
		id, ok := sandboxID(dir)
		if !ok {
			continue
		}
		pids, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		for _, pid := range strings.Fields(string(pids)) {
			same, err := f.pidInNetns(pid, netns)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if same {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("no sandbox cgroup has a process in netns inode %d", netns)
}

func (f Finder) pidInNetns(pid string, want uint64) (bool, error) {
	if _, err := strconv.Atoi(pid); err != nil {
		return false, fmt.Errorf("bad pid %q", pid)
	}
	path := filepath.Join(f.procRoot(), pid, "ns", "net")
	target, err := os.Readlink(path)
	if err == nil {
		got, ok := parseNetnsLink(target)
		return ok && got == want, nil
	}
	if !errors.Is(err, fs.ErrInvalid) {
		return false, err
	}
	got, err := inode(path)
	if err != nil {
		return false, err
	}
	return got == want, nil
}

func parseNetnsLink(target string) (uint64, bool) {
	target = strings.TrimPrefix(target, "net:[")
	target = strings.TrimSuffix(target, "]")
	n, err := strconv.ParseUint(target, 10, 64)
	return n, err == nil
}

func sandboxID(dir string) (string, bool) {
	name := filepath.Base(dir)
	rest, ok := strings.CutPrefix(name, "sbx-")
	if !ok {
		return "", false
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

func inode(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("stat for %s has unexpected type %T", path, info.Sys())
	}
	return uint64(stat.Ino), nil
}
