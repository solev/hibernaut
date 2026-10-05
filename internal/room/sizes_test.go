package room_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/room"
)

func TestMixedSizes(t *testing.T) {
	// E2B holds about 75 of the host's 3200 huge pages. A small machine reserves 512 more, a
	// medium one 1024 and a large one 2048.
	small := room.Machine{Name: "notes", MemoryMB: 1024}
	medium := room.Machine{Name: "web", MemoryMB: 2048}
	running := []room.Machine{small, medium}
	for _, tc := range []struct {
		name                string
		freePages, diskFree int64
		small, medium, big  int
	}{
		{name: "nothing running", freePages: 3125, diskFree: 80 * gib, small: 6, medium: 3, big: 1},
		{name: "a small and a medium running", freePages: 3125 - 512 - 1024, diskFree: 80 * gib, small: 3, medium: 1},
		{name: "just enough memory for a large", freePages: 2048, diskFree: 80 * gib, small: 4, medium: 2, big: 1},
		{name: "a page short of a large", freePages: 2047, diskFree: 80 * gib, small: 3, medium: 1},
		{name: "just enough disk for a large", freePages: 3125, diskFree: 10 * gib, small: 2, medium: 1, big: 1},
		{name: "a byte of disk short of a large", freePages: 3125, diskFree: 10*gib - 1, small: 2, medium: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: tc.freePages, DiskFree: tc.diskFree}
			run := running
			if tc.name == "nothing running" {
				run = nil
			}
			for _, size := range []struct {
				memoryMB, want int
			}{{1024, tc.small}, {2048, tc.medium}, {4096, tc.big}} {
				if got := h.Fits(size.memoryMB, run); got != size.want {
					t.Errorf("Fits(%d) = %d, want %d", size.memoryMB, got, size.want)
				}
				err := h.Check("app", size.memoryMB, run)
				if (err == nil) != (size.want > 0) || err != nil && !errors.Is(err, room.ErrFull) {
					t.Errorf("Check(%d) = %v, want room for %d", size.memoryMB, err, size.want)
				}
			}
		})
	}
}

func TestPlanThatCanNeverFit(t *testing.T) {
	h := room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 3125, DiskFree: 80 * gib}
	want := `no room on the host for "big": it needs 8 GiB of memory at once, and the host has 6.2 GiB ` +
		`for machines in all. It can't fit on this host, even with every machine paused`
	for _, tc := range []struct {
		name    string
		plan    room.Plan
		running []room.Machine
	}{
		{"save running", room.Plan{MemoryMB: 4096, Running: 1, Snapshots: 2, Peak: 2}, []room.Machine{{Name: "big", MemoryMB: 4096}}},
		{"save paused", room.Plan{MemoryMB: 4096, Running: 2, Snapshots: 2, Peak: 2}, nil},
		{"fork paused", room.Plan{MemoryMB: 4096, Running: 2, Snapshots: 3, Peak: 2}, []room.Machine{{Name: "web", MemoryMB: 2048}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.CheckPlan("big", tc.plan, tc.running)
			if err == nil || err.Error() != want || !errors.Is(err, room.ErrFull) {
				t.Errorf("CheckPlan = %v\nwant %s", err, want)
			}
		})
	}
	if err := h.Check("big", 4096, nil); err != nil {
		t.Errorf("one large machine on an empty host: %v", err)
	}
	h.Pages = 4096
	h.FreePages = 2047
	err := h.CheckPlan("big", room.Plan{MemoryMB: 4096, Running: 1, Snapshots: 2, Peak: 2}, []room.Machine{{Name: "big", MemoryMB: 4096}})
	if err == nil || !strings.Contains(err.Error(), "pause one: hib pause NAME") {
		t.Errorf("a plan the whole pool holds = %v, want the usual message", err)
	}
	h.FreePages = 4096
	if err := h.CheckPlan("big", room.Plan{MemoryMB: 4096, Running: 2, Snapshots: 2, Peak: 2}, nil); err != nil {
		t.Errorf("a plan that just fits the whole pool = %v", err)
	}
	h = room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 1000, DiskFree: 80 * gib}
	err = h.CheckPlan("app", room.Plan{MemoryMB: 4096, Running: 2, Snapshots: 2}, nil)
	if err == nil || !strings.Contains(err.Error(), "even with every machine paused") {
		t.Errorf("a plan with no Peak = %v, want its Running to count as its peak", err)
	}
}
