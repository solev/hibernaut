package images

import (
	"slices"
	"strings"
	"testing"
)

func TestSizes(t *testing.T) {
	caPEM := testCAPEM(t)
	medium, err := Spec(Default, DefaultSize(), caPEM)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		cpus     int
		memoryMB int
		template string
	}{
		{"small", 1, 1024, "hib-dev-small"},
		{"medium", 2, 2048, "hib-dev"},
		{"large", 4, 4096, "hib-dev-large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := SizeNamed(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if s.Name() != tc.name || s.CPUs() != tc.cpus || s.MemoryMB() != tc.memoryMB {
				t.Errorf("SizeNamed(%s) = %s, %d CPUs, %d MiB", tc.name, s.Name(), s.CPUs(), s.MemoryMB())
			}
			if got := SizeTemplate(Default, s); got != tc.template {
				t.Errorf("SizeTemplate = %s, want %s", got, tc.template)
			}
			if got, ok := SizeWithMemory(tc.memoryMB); !ok || got != s {
				t.Errorf("SizeWithMemory(%d) = %s, %v", tc.memoryMB, got.Name(), ok)
			}
			spec, err := Spec(Default, s, caPEM)
			if err != nil {
				t.Fatal(err)
			}
			if spec.Name != tc.template || spec.CPUs != tc.cpus || spec.MemoryMB != tc.memoryMB || spec.MinFreeDiskMB != 10240 {
				t.Errorf("spec %s: %d CPUs, %d MiB, %d MiB disk", spec.Name, spec.CPUs, spec.MemoryMB, spec.MinFreeDiskMB)
			}
			if !slices.Equal(spec.Steps, medium.Steps) || spec.FromImage != medium.FromImage || spec.StartCmd != medium.StartCmd {
				t.Error("the sizes don't share the build steps")
			}
		})
	}
	if got := sizeNames(Sizes()); !slices.Equal(got, []string{"small", "medium", "large"}) {
		t.Errorf("Sizes() = %v", got)
	}
	if got := sizeNames(BuildSizes()); !slices.Equal(got, []string{"medium", "small", "large"}) {
		t.Errorf("BuildSizes() = %v, want the default first", got)
	}
	if DefaultSize().Name() != "medium" || SizeTemplate(Default, DefaultSize()) != Template(Default) {
		t.Errorf("default size %s isn't today's %s", DefaultSize().Name(), Template(Default))
	}
}

func TestSizeNamedListsTheSizes(t *testing.T) {
	for _, name := range []string{"", "huge", "Small", "small ", "medium\x1b[31m", "dev"} {
		_, err := SizeNamed(name)
		if err == nil || !strings.Contains(err.Error(), "small, medium, large") || strings.ContainsRune(err.Error(), 0x1b) {
			t.Errorf("SizeNamed(%q) = %v, want an error that lists the sizes", name, err)
		}
	}
	if s, ok := SizeWithMemory(512); ok {
		t.Errorf("SizeWithMemory(512) = %s, want none", s.Name())
	}
	if _, err := Spec(Default, Size{}, testCAPEM(t)); err == nil {
		t.Error("Spec with no size succeeded")
	}
}

func TestSizeTemplatesDontCollide(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range Names() {
		for _, s := range Sizes() {
			alias := SizeTemplate(name, s)
			if seen[alias] {
				t.Errorf("two images or sizes share template %s", alias)
			}
			seen[alias] = true
			if _, ok := SavedName(alias); ok || IsForkTemplate(alias) {
				t.Errorf("template %s looks like a saved image's or a fork's", alias)
			}
		}
	}
}

func sizeNames(sizes []Size) []string {
	names := make([]string, len(sizes))
	for i, s := range sizes {
		names[i] = s.Name()
	}
	return names
}
