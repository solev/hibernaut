package machines

import (
	"cmp"
	"context"
	"fmt"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

type machinePlan struct {
	template string
	memoryMB int
	cpus     int
	size     images.Size
}

func (m *Manager) planMachine(ctx context.Context, image string, size images.Size) (machinePlan, error) {
	if images.IsBuiltIn(image) {
		size = cmp.Or(size, images.DefaultSize())
		return machinePlan{template: images.SizeTemplate(image, size), memoryMB: size.MemoryMB(),
			cpus: size.CPUs(), size: size}, nil
	}
	if err := hibapi.CheckName(image); err != nil {
		return machinePlan{}, fmt.Errorf("no image %q: %w", image, err)
	}
	tmpl, err := m.e.Template(ctx, images.Template(image))
	if err != nil {
		return machinePlan{}, describe(err, fmt.Sprintf("no image %q: %v. List images with: hib image ls", image, err))
	}
	memoryMB := tmpl.MemoryMB
	if memoryMB <= 0 {
		return machinePlan{}, fmt.Errorf("image %s has no memory size: check it with: hib image ls", image)
	}
	saved, _ := images.SizeWithMemory(memoryMB)
	if size != (images.Size{}) {
		return machinePlan{}, fmt.Errorf("image %s is a saved image, and its machines are the size of the machine it was saved from (%s). Leave out --size",
			image, cmp.Or(saved.Name(), hibapi.FormatMB(int64(memoryMB))))
	}
	return machinePlan{template: images.Template(image), memoryMB: memoryMB,
		cpus: cmp.Or(tmpl.CPUs, images.DefaultSize().CPUs()), size: saved}, nil
}

func notBuilt(image string, size images.Size) string {
	if size == images.DefaultSize() {
		return fmt.Sprintf("image %q isn't built yet (run: hib image build %s)", image, image)
	}
	return fmt.Sprintf("image %s isn't built in size %s yet (run: hib image build %s)", image, size.Name(), image)
}

func machineSize(em engine.Machine) (images.Size, bool) {
	if s, err := images.SizeNamed(em.Metadata[labelSize]); err == nil {
		return s, true
	}
	return images.SizeWithMemory(em.MemoryMB)
}

// CheckBuildRoom returns nil if there's room on the host to build image at size, which boots a
// machine of that size. Otherwise it returns an error wrapping room.ErrFull that says what to do.
func (m *Manager) CheckBuildRoom(ctx context.Context, image string, size images.Size) error {
	reading, err := m.roomNow(ctx)
	if err != nil {
		return err
	}
	return reading.host.Check(fmt.Sprintf("%s (%s)", image, size.Name()), size.MemoryMB(), roomMachines(reading.running))
}
