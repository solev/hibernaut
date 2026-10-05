package room_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/room"
)

func TestPlanRoundsEachMachinesPages(t *testing.T) {
	h := room.Host{PageSize: 2 << 20, Pages: 4, FreePages: 3, DiskFree: 80 * gib}
	p := room.Plan{MemoryMB: 3, Running: 2, Snapshots: 2}
	if err := h.CheckPlan("web", p, nil); !errors.Is(err, room.ErrFull) {
		t.Errorf("two 3 MiB machines in three 2 MiB pages = %v, want ErrFull", err)
	}
	h.FreePages++
	if err := h.CheckPlan("web", p, nil); err != nil {
		t.Errorf("two 3 MiB machines in four 2 MiB pages = %v", err)
	}
}

func TestPlanMessagesEscapeRunningNames(t *testing.T) {
	h := room.Host{PageSize: 2 << 20, Pages: 3200, DiskFree: 80 * gib}
	err := h.CheckPlan("web", room.Plan{MemoryMB: 2048, Running: 2, Snapshots: 2}, []room.Machine{{Name: "raw\x1b]52;c;name\x07", MemoryMB: 512}})
	if err == nil || strings.ContainsRune(err.Error(), 0x1b) || strings.ContainsRune(err.Error(), 7) {
		t.Errorf("unsafe room error: %q", err)
	}
}
