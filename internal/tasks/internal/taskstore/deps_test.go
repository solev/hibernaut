package taskstore_test

import (
	"go/build"
	"strings"
	"testing"
)

const module = "github.com/solev/hibernaut/"

// TestNoOutsideCalls checks that nothing taskstore imports, directly or not, can reach a
// machine, E2B or a lock, so no transaction can span one. A store method that needs one is in
// the wrong package.
func TestNoOutsideCalls(t *testing.T) {
	forbidden := []string{"internal/engine", "internal/machines", "internal/links",
		"internal/hibd", "internal/tasks/internal/taskmachine",
		"internal/tasks/internal/taskrun", "internal/taskguest"}
	seen := map[string]bool{}
	var walk func(path, from string)
	walk = func(path, from string) {
		if seen[path] || !strings.HasPrefix(path, module) {
			return
		}
		seen[path] = true
		for _, f := range forbidden {
			if strings.HasPrefix(path, module+f) {
				t.Errorf("taskstore reaches %s through %s", path, from)
			}
		}
		p, err := build.Default.Import(path, ".", 0)
		if err != nil {
			t.Fatalf("importing %s: %v", path, err)
		}
		for _, imp := range p.Imports {
			walk(imp, path)
		}
	}
	walk(module+"internal/tasks/internal/taskstore", "")
}
