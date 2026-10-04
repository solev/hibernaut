// Package room says whether one more machine fits on the host (spec §5, Room on the host).
//
// A running machine's RAM comes from the host's pool of huge pages, reserved in full as it
// starts or resumes. A paused machine holds none. E2B refuses a start the pool can't hold, but
// says only "500 Failed to place sandbox … please retry". Disk matters too: pausing a machine
// writes a snapshot, and a pause that can't be written still stops the machine, losing what was
// running in it.
package room

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/solev/hibernaut/internal/hibapi"
)

// SnapshotDir is where E2B keeps the snapshots of paused machines.
const SnapshotDir = "/var/lib/e2b"

// DiskMargin is what a pause may write beyond a machine's RAM: the changes to its own disk
// since its last pause.
const DiskMargin = 1 << 30

// ErrFull is wrapped by the errors of Check.
var ErrFull = errors.New("no room on the host")

// Host is a reading of the room on the host.
type Host struct {
	// PageSize is the size of a huge page in bytes.
	PageSize int64
	// Pages is how many huge pages the pool has, and FreePages how many of them no machine
	// holds or has reserved.
	Pages, FreePages int64
	// DiskFree is the free space in SnapshotDir in bytes.
	DiskFree int64
}

// Machine is a running machine.
type Machine struct {
	// Name says which machine it is in messages.
	Name     string
	MemoryMB int
}

// Read reads the room on this host: /proc/meminfo, and the free space in SnapshotDir.
func Read() (Host, error) {
	info, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Host{}, fmt.Errorf("reading the host's memory: %w", err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(SnapshotDir, &st); err != nil {
		return Host{}, fmt.Errorf("reading the host's free disk: %w", err)
	}
	return Parse(info, int64(st.Bavail)*int64(st.Bsize))
}

var meminfoKeys = []string{"HugePages_Total", "HugePages_Free", "HugePages_Rsvd", "Hugepagesize"}

// Parse reads a /proc/meminfo, and diskFree, the free space in SnapshotDir in bytes.
func Parse(meminfo []byte, diskFree int64) (Host, error) {
	v := map[string]int64{}
	for line := range strings.Lines(string(meminfo)) {
		key, value, _ := strings.Cut(line, ":")
		f := strings.Fields(value)
		if !slices.Contains(meminfoKeys, key) || len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil || n < 0 {
			return Host{}, fmt.Errorf("bad %s in /proc/meminfo: %q", key, strings.TrimSpace(value))
		}
		if len(f) > 1 && f[1] == "kB" {
			n <<= 10
		}
		v[key] = n
	}
	for _, k := range meminfoKeys {
		if _, ok := v[k]; !ok {
			return Host{}, fmt.Errorf("no %s in /proc/meminfo: the host has no huge pages for machines", k)
		}
	}
	if v["Hugepagesize"] == 0 {
		return Host{}, errors.New("huge pages of size 0 in /proc/meminfo")
	}
	return Host{
		PageSize:  v["Hugepagesize"],
		Pages:     v["HugePages_Total"],
		FreePages: max(v["HugePages_Free"]-v["HugePages_Rsvd"], 0),
		DiskFree:  diskFree,
	}, nil
}

// MemoryMB returns the RAM free for more running machines, and the RAM running machines share,
// in MiB.
func (h Host) MemoryMB() (free, total int64) {
	return h.FreePages * h.PageSize >> 20, h.Pages * h.PageSize >> 20
}

// Fits returns how many more machines with memoryMB of RAM fit next to the running machines.
func (h Host) Fits(memoryMB int, running []Machine) int {
	byMemory := h.FreePages / h.pages(memoryMB)
	byDisk := (h.DiskFree - pauseDisk(running...)) / pauseDisk(Machine{MemoryMB: memoryMB})
	return int(max(min(byMemory, byDisk), 0))
}

// Check returns nil if machine name, with memoryMB of RAM, fits next to the running machines.
// Otherwise it returns an error wrapping ErrFull that says what to do.
func (h Host) Check(name string, memoryMB int, running []Machine) error {
	names := make([]string, len(running))
	for i, m := range running {
		names[i] = m.Name
	}
	slices.Sort(names)
	runningNow := ""
	if len(names) > 0 {
		runningNow = " Running now: " + strings.Join(names, ", ") + "."
	}

	if h.FreePages < h.pages(memoryMB) {
		free, _ := h.MemoryMB()
		msg := fmt.Sprintf("it needs %s of memory and %s is free.", hibapi.FormatMB(int64(memoryMB)), hibapi.FormatMB(free))
		if len(running) == 0 {
			return fmt.Errorf("%w for %q: %s No machine is running, so an image build may be using it: try again when it's done", ErrFull, name, msg)
		}
		return fmt.Errorf("%w for %q: %s%s To free memory, pause one: hib pause NAME", ErrFull, name, msg, runningNow)
	}
	if need := pauseDisk(running...) + pauseDisk(Machine{MemoryMB: memoryMB}); h.DiskFree < need {
		who := "it and the running machines"
		if len(running) == 0 {
			who = "it"
		}
		return fmt.Errorf("%w for %q: %s may need %s of disk to pause, and %s is free.%s To free disk, remove machines you don't need: hib rm NAME",
			ErrFull, name, who, hibapi.FormatMB(need>>20), hibapi.FormatMB(h.DiskFree>>20), runningNow)
	}
	return nil
}

func (h Host) pages(memoryMB int) int64 {
	if h.PageSize <= 0 {
		return h.FreePages + 1
	}
	return max((int64(memoryMB)<<20+h.PageSize-1)/h.PageSize, 1)
}

func pauseDisk(machines ...Machine) int64 {
	var n int64
	for _, m := range machines {
		n += int64(m.MemoryMB)<<20 + DiskMargin
	}
	return n
}
