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
	size     images.Size
}

func (m *Manager) planMachine(ctx context.Context, image string, size images.Size) (machinePlan, error) {
	if images.IsBuiltIn(image) {
		size = cmp.Or(size, images.DefaultSize())
		return machinePlan{template: images.SizeTemplate(image, size), memoryMB: size.MemoryMB(), size: size}, nil
	}
	memoryMB, err := m.imageMemory(ctx, image)
	if err != nil {
		return machinePlan{}, err
	}
	saved, _ := images.SizeWithMemory(memoryMB)
	if size != (images.Size{}) {
		return machinePlan{}, fmt.Errorf("image %s is a saved image, and its machines are the size of the machine it was saved from (%s). Leave out --size",
			image, cmp.Or(saved.Name(), hibapi.FormatMB(int64(memoryMB))))
	}
	return machinePlan{template: images.Template(image), memoryMB: memoryMB, size: saved}, nil
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
