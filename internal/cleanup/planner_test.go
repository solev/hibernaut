package cleanup_test

import (
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
)

func TestGarbageKeepsProtectionReasonsThroughHeaderCycles(t *testing.T) {
	l := cleanup.Listing{
		Envs:   []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{ready("current", "img", ago(2*time.Hour))},
		Cached: []string{"current", "cached", "", "missing"},
		Dirs: []cleanup.Dir{
			dir("current", now, "dependency"),
			dir("dependency", ago(2*time.Hour), "current", "missing"),
			dir("cached", now),
			dir("orphan", ago(2*time.Hour)),
		},
	}
	p, err := cleanup.Garbage(l, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Delete, []string{"orphan"}) {
		t.Errorf("deletions = %q, want only orphan", p.Delete)
	}
	for id, want := range map[string]string{
		"current":    "the current build of image img",
		"dependency": "current needs it",
		"cached":     "E2B's layer cache points to it",
	} {
		if got := p.Keep[id]; got != want {
			t.Errorf("reason for %s = %q, want %q", id, got, want)
		}
	}
	if len(p.Keep) != 3 {
		t.Errorf("kept %d directories, want 3", len(p.Keep))
	}
}
