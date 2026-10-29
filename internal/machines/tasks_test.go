package machines

import (
	"errors"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
)

func TestAdmit(t *testing.T) {
	const page = 2 << 20
	pages := func(mb int64) int64 { return mb << 20 / page }
	host := func(freeMB int64) room.Host {
		return room.Host{PageSize: page, Pages: pages(16384), FreePages: pages(freeMB),
			DiskFree: 100 << 30}
	}
	medium := engine.Machine{ID: "a", MemoryMB: 2048, CPUs: 2}
	taskM := engine.Machine{ID: "b", MemoryMB: 2048, CPUs: 2,
		Metadata: map[string]string{task.Label: "t-aaaaaaaa"}}
	reserve := int64(PeopleReserveMB())
	for _, c := range []struct {
		name    string
		h       room.Host
		running []engine.Machine
		lim     TaskLimits
		ok      bool
	}{
		{"fits beside the reserve", host(2048 + reserve), nil, TaskLimits{}, true},
		{"would spend the reserve", host(2048 + reserve - 2), nil, TaskLimits{}, false},
		{"within the vCPU factor", host(8192), []engine.Machine{medium}, TaskLimits{HostCPUs: 2}, true},
		{"past the vCPU factor", host(8192), []engine.Machine{medium, medium},
			TaskLimits{HostCPUs: 2}, false},
		{"max-tasks counts task machines only", host(8192), []engine.Machine{medium, taskM},
			TaskLimits{MaxTasks: 2}, true},
		{"max-tasks reached", host(8192), []engine.Machine{taskM, taskM}, TaskLimits{MaxTasks: 2},
			false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := Admit(c.h, c.running, c.lim, "t-bbbbbbbb", 2048, 2)
			if (err == nil) != c.ok {
				t.Fatalf("Admit = %v, want ok %v", err, c.ok)
			}
			if err != nil && !errors.Is(err, room.ErrFull) {
				t.Fatalf("Admit = %v, want it to wrap room.ErrFull", err)
			}
		})
	}
}
