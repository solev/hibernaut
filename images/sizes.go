package images

import (
	"fmt"
	"slices"
	"strings"
)

// Size is how many vCPUs and how much RAM a machine has. A machine keeps its size for life. Sizes
// come from a fixed table: get one from Sizes, SizeNamed, SizeWithMemory or DefaultSize. The zero
// Size is none.
type Size struct {
	name     string
	cpus     int
	memoryMB int
}

var sizes = []Size{
	{name: "small", cpus: 1, memoryMB: 1024},
	{name: "medium", cpus: 2, memoryMB: 2048},
	{name: "large", cpus: 4, memoryMB: 4096},
}

const defaultSize = 1

// Name returns the size's name, such as "small".
func (s Size) Name() string { return s.name }

// CPUs returns how many vCPUs a machine of the size has.
func (s Size) CPUs() int { return s.cpus }

// MemoryMB returns how much RAM a machine of the size has, in MiB.
func (s Size) MemoryMB() int { return s.memoryMB }

// Sizes returns the sizes there are, smallest first.
func Sizes() []Size {
	return slices.Clone(sizes)
}

// DefaultSize returns the size machines are made at when no other is asked for: medium, the size
// every machine had before there were sizes.
func DefaultSize() Size {
	return sizes[defaultSize]
}

// BuildSizes returns the sizes an image build builds, in order: the default first, so it's
// ready first.
func BuildSizes() []Size {
	out := []Size{DefaultSize()}
	for _, s := range sizes {
		if s != DefaultSize() {
			out = append(out, s)
		}
	}
	return out
}

// SizeNamed returns the size called name. Any other name, the empty one included, is an error
// that lists the sizes.
func SizeNamed(name string) (Size, error) {
	for _, s := range sizes {
		if s.name == name {
			return s, nil
		}
	}
	return Size{}, fmt.Errorf("no size %q (there is: %s)", name, strings.Join(SizeNames(), ", "))
}

// SizeNames returns the names of the sizes, smallest first.
func SizeNames() []string {
	names := make([]string, len(sizes))
	for i, s := range sizes {
		names[i] = s.name
	}
	return names
}

// SizeWithMemory returns the size whose machines have memoryMB of RAM, and whether there is one.
func SizeWithMemory(memoryMB int) (Size, bool) {
	for _, s := range sizes {
		if s.memoryMB == memoryMB {
			return s, true
		}
	}
	return Size{}, false
}

// SizeTemplate returns the template built-in image name is in at size s. The default size, and
// the zero Size, keep the template Template returns, hib-NAME. The others are hib-NAME-SIZE.
func SizeTemplate(name string, s Size) string {
	if s.name == "" || s == DefaultSize() {
		return Template(name)
	}
	return builtPrefix + name + "-" + s.name
}
