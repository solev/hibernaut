package room_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/solev/hibernaut/internal/room"
)

const gib = 1 << 30

func meminfo(free, rsvd int) []byte {
	return fmt.Appendf(nil, `MemTotal:       16366548 kB
MemFree:         7398240 kB
MemAvailable:    7160524 kB
AnonHugePages:         0 kB
ShmemHugePages:        0 kB
FileHugePages:     32768 kB
HugePages_Total:    3200
HugePages_Free:     %d
HugePages_Rsvd:     %d
HugePages_Surp:        0
Hugepagesize:       2048 kB
Hugetlb:         6553600 kB
`, free, rsvd)
}

func TestRoom(t *testing.T) {
	// E2B itself holds about 75 of the host's 3200 huge pages. A running 2 GiB dev machine
	// reserves 1024 more as it starts, and touches some of them.
	var (
		none  = meminfo(3125, 0)
		one   = meminfo(3098, 997)
		three = meminfo(3000, 2947)
		api   = room.Machine{Name: "api", MemoryMB: 2048}
		inf   = room.Machine{Name: "inf", MemoryMB: 2048}
		web   = room.Machine{Name: "web", MemoryMB: 2048}
	)
	for _, tc := range []struct {
		name     string
		meminfo  []byte
		diskFree int64
		running  []room.Machine
		fits     int
		err      string
	}{
		{name: "nothing running", meminfo: none, diskFree: 80 * gib, fits: 3},
		{name: "one running", meminfo: one, diskFree: 80 * gib, running: []room.Machine{api}, fits: 2},
		{
			name: "three running", meminfo: three, diskFree: 80 * gib, running: []room.Machine{web, api, inf},
			err: `no room on the host for "app": it needs 2 GiB of memory and 106 MiB is free. ` +
				`Running now: api, inf, web. To free memory, pause one: hib pause NAME`,
		},
		{
			name: "memory held by something else", meminfo: meminfo(2000, 1947), diskFree: 80 * gib,
			err: `no room on the host for "app": it needs 2 GiB of memory and 106 MiB is free. ` +
				`No machine is running, so an image build may be using it: try again when it's done`,
		},
		{
			name: "no disk to pause", meminfo: one, diskFree: 5 * gib, running: []room.Machine{api},
			err: `no room on the host for "app": it and the running machines may need 6 GiB of disk ` +
				`to pause, and 5 GiB is free. Running now: api. To free disk, remove machines you ` +
				`don't need: hib rm NAME`,
		},
		{name: "just enough", meminfo: meminfo(2048, 1024), diskFree: 6 * gib, running: []room.Machine{api}, fits: 1},
		{
			name: "a page short", meminfo: meminfo(2047, 1024), diskFree: 6 * gib, running: []room.Machine{api},
			err: `no room on the host for "app": it needs 2 GiB of memory and 1.9 GiB is free. ` +
				`Running now: api. To free memory, pause one: hib pause NAME`,
		},
		{
			name: "a byte of disk short", meminfo: meminfo(2048, 1024), diskFree: 6*gib - 1, running: []room.Machine{api},
			err: `no room on the host for "app": it and the running machines may need 6 GiB of disk ` +
				`to pause, and 5.9 GiB is free. Running now: api. To free disk, remove machines you ` +
				`don't need: hib rm NAME`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := room.Parse(tc.meminfo, tc.diskFree)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := h.Fits(2048, tc.running); got != tc.fits {
				t.Errorf("Fits(2048) = %d, want %d", got, tc.fits)
			}
			err = h.Check("app", 2048, tc.running)
			switch {
			case tc.err == "" && err != nil:
				t.Errorf("Check = %v, want nil", err)
			case tc.err != "" && (err == nil || err.Error() != tc.err):
				t.Errorf("Check = %v\nwant %s", err, tc.err)
			case tc.err != "" && !errors.Is(err, room.ErrFull):
				t.Errorf("Check = %v, want it to wrap ErrFull", err)
			}
		})
	}
}

func TestParseFailsClosed(t *testing.T) {
	for name, info := range map[string]string{
		"empty":             "",
		"no huge pages":     "MemTotal:       16366548 kB\nMemFree:         7398240 kB\n",
		"no page size":      "HugePages_Total:    3200\nHugePages_Free:     3125\nHugePages_Rsvd:        0\n",
		"unreadable number": "HugePages_Total:    3200\nHugePages_Free:     lots\nHugePages_Rsvd:        0\nHugepagesize:       2048 kB\n",
	} {
		t.Run(name, func(t *testing.T) {
			if h, err := room.Parse([]byte(info), 80*gib); err == nil {
				t.Errorf("Parse = %+v, want an error", h)
			}
		})
	}
}

func TestMemoryMB(t *testing.T) {
	h, err := room.Parse(meminfo(3098, 997), 80*gib)
	if err != nil {
		t.Fatal(err)
	}

	if free, total := h.MemoryMB(); free != 4202 || total != 6400 {
		t.Errorf("MemoryMB() = %d, %d, want 4202, 6400", free, total)
	}
}

func TestOperationPlans(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan room.Plan
	}{
		{"new", room.Plan{MemoryMB: 2048, Running: 1, Snapshots: 1}},
		{"save running", room.Plan{MemoryMB: 2048, Running: 1, Snapshots: 2}},
		{"save paused", room.Plan{MemoryMB: 2048, Running: 2, Snapshots: 2}},
		{"fork one running", room.Plan{MemoryMB: 2048, Running: 1, Snapshots: 3}},
		{"fork one paused", room.Plan{MemoryMB: 2048, Running: 2, Snapshots: 3}},
		{"fork three running", room.Plan{MemoryMB: 2048, Running: 3, Snapshots: 5}},
		{"fork three paused", room.Plan{MemoryMB: 2048, Running: 3, Snapshots: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			running := []room.Machine{{Name: "web", MemoryMB: 2048}}
			h := room.Host{PageSize: 2 << 20, Pages: 8192, FreePages: 1024 * int64(tc.plan.Running), DiskFree: int64(1+tc.plan.Snapshots) * 3 * gib}
			if err := h.CheckPlan("web", tc.plan, running); err != nil {
				t.Fatalf("exactly enough room: %v", err)
			}
			h.FreePages--
			if err := h.CheckPlan("web", tc.plan, running); !errors.Is(err, room.ErrFull) {
				t.Fatalf("one page short: %v", err)
			}
			h.FreePages++
			h.DiskFree--
			if err := h.CheckPlan("web", tc.plan, running); !errors.Is(err, room.ErrFull) {
				t.Fatalf("one byte short: %v", err)
			}
		})
	}
}

func TestPlanMessages(t *testing.T) {
	h := room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 1024, DiskFree: 80 * gib}
	running := []room.Machine{{Name: "web", MemoryMB: 2048}, {Name: "api", MemoryMB: 2048}}
	p := room.Plan{MemoryMB: 2048, Running: 2, Snapshots: 4}
	want := `no room on the host for "web": it needs 4 GiB of memory and 2 GiB is free. Running now: api, web. To free memory, pause one: hib pause NAME`
	if err := h.CheckPlan("web", p, running); err == nil || err.Error() != want {
		t.Fatalf("memory error = %v, want %s", err, want)
	}
	h.FreePages = 2048
	h.DiskFree = 17 * gib
	want = `no room on the host for "web": the operation and the running machines may need 18 GiB of disk to pause, and 17 GiB is free. Running now: api, web. To free disk, remove machines you don't need: hib rm NAME`
	if err := h.CheckPlan("web", p, running); err == nil || err.Error() != want {
		t.Fatalf("disk error = %v, want %s", err, want)
	}
	for _, h := range []room.Host{h, {PageSize: 2 << 20, Pages: 3200, FreePages: 0, DiskFree: 0}} {
		a := h.Check("web", 2048, running)
		b := h.CheckPlan("web", room.Plan{MemoryMB: 2048, Running: 1, Snapshots: 1}, running)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("Check = %v, CheckPlan = %v", a, b)
		}
	}
}
