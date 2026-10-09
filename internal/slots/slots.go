// Package slots maps an intercepted E2B slot connection back to its sandbox, and a sandbox to
// its slot.
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

// Finder reads host state. Empty roots use the real host paths: /proc, /run/netns and
// /sys/fs/cgroup/e2b.
type Finder struct {
	Proc    string
	Netns   string
	Cgroups string
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

// Slot returns the E2B slot that runs sandbox id. It globs sbx-<id>-* under Cgroups, reads
// cgroup.procs, reads each PID's /proc/<pid>/ns/net inode and finds the /run/netns/ns-N with
// that inode: the walk sandboxForNetns does, in the other direction.
func (f Finder) Slot(id string) (int, error) {
	if id == "" || strings.ContainsAny(id, `*?[\/`) {
		return 0, fmt.Errorf("slots: %q is not a sandbox ID", id)
	}
	netns, err := f.sandboxNetns(id)
	if err != nil {
		return 0, err
	}
	if len(netns) == 0 {
		return 0, fmt.Errorf("slots: sandbox %s has no running process", id)
	}

	root := f.netnsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, fmt.Errorf("slots: reading %s: %w", root, err)
	}
	var found []int
	for _, entry := range entries {
		n, ok := strings.CutPrefix(entry.Name(), "ns-")
		if !ok {
			continue
		}
		slot, err := strconv.Atoi(n)
		if err != nil || slot <= 0 || slot > maxSlot {
			continue
		}
		ino, err := inode(filepath.Join(root, entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("slots: reading %s: %w", filepath.Join(root, entry.Name()), err)
		}
		if netns[ino] {
			found = append(found, slot)
		}
	}
	switch len(found) {
	case 0:
		return 0, fmt.Errorf("slots: no slot network namespace holds sandbox %s", id)
	case 1:
		return found[0], nil
	default:
		return 0, fmt.Errorf("slots: sandbox %s is in more than one slot: %v", id, found)
	}
}

// Addr returns the host address of a slot, the inverse of slotFromSource.
func Addr(slot int) (netip.Addr, error) {
	if slot <= 0 || slot > maxSlot {
		return netip.Addr{}, fmt.Errorf("slots: %d is not a sandbox slot", slot)
	}
	return netip.AddrFrom4([4]byte{10, 11, byte(slot >> 8), byte(slot)}), nil
}

// maxSlot is the highest slot 10.11.0.0/16 can address.
const maxSlot = 1<<16 - 1

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

// sandboxNetns returns the network namespace inodes of every process in id's run cgroups.
func (f Finder) sandboxNetns(id string) (map[uint64]bool, error) {
	dirs, err := filepath.Glob(filepath.Join(f.cgroupsRoot(), "sbx-"+id+"-*"))
	if err != nil {
		return nil, err
	}
	netns := map[uint64]bool{}
	for _, dir := range dirs {
		if got, ok := sandboxID(dir); !ok || got != id {
			continue
		}
		pids, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, pid := range strings.Fields(string(pids)) {
			ino, ok, err := f.pidNetns(pid)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if ok {
				netns[ino] = true
			}
		}
	}
	return netns, nil
}

func (f Finder) pidInNetns(pid string, want uint64) (bool, error) {
	got, ok, err := f.pidNetns(pid)
	if err != nil {
		return false, err
	}
	return ok && got == want, nil
}

// pidNetns returns the inode of pid's network namespace. It reports false for a link it cannot
// parse, which belongs to no slot.
func (f Finder) pidNetns(pid string) (uint64, bool, error) {
	if _, err := strconv.Atoi(pid); err != nil {
		return 0, false, fmt.Errorf("bad pid %q", pid)
	}
	path := filepath.Join(f.procRoot(), pid, "ns", "net")
	target, err := os.Readlink(path)
	if err == nil {
		got, ok := parseNetnsLink(target)
		return got, ok, nil
	}
	if !errors.Is(err, fs.ErrInvalid) {
		return 0, false, err
	}
	got, err := inode(path)
	if err != nil {
		return 0, false, err
	}
	return got, true, nil
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
