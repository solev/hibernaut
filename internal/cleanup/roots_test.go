package cleanup_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
)

func TestImageSaveDoesNotEndPausingMachinesRunProtection(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprintf("image removed=%v", removed), func(t *testing.T) {
			image := cleanup.Env{ID: "saved"}
			if removed {
				image.Deleted = ago(2 * time.Hour)
			}
			save := ready("save", "snap-web", ago(3*time.Hour))
			save.In = append(save.In, cleanup.Assignment{Env: "saved", Tag: "default", At: ago(3 * time.Hour)})
			replacement := ready("replacement", "snap-web", ago(11*time.Hour))
			replacement.In[0].At = ago(9 * time.Hour)
			l := cleanup.Listing{
				Envs: []cleanup.Env{{ID: "snap-web", Machine: "web"}, image},
				Builds: []cleanup.Build{
					ready("input", "snap-web", ago(12*time.Hour)),
					replacement,
					save,
				},
				Dirs: []cleanup.Dir{
					dir("input", ago(12*time.Hour)),
					dir("replacement", ago(9*time.Hour)),
					dir("save", ago(3*time.Hour)),
				},
				Machines: []cleanup.Machine{{ID: "web", Started: ago(10 * time.Hour), Paused: true}},
			}
			p, err := cleanup.Garbage(l, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, root := p.Roots["input"]; !root || slices.Contains(p.Delete, "input") {
				t.Fatalf("image save ended protection of the run's input: %+v", p)
			}
		})
	}
}

func TestMachineSelectionRoots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		start  time.Time
		paused bool
		finish time.Time
		want   []string
	}{
		{"running", ago(6 * time.Hour), false, ago(2 * time.Hour), []string{"b1", "b2", "b3", "b4"}},
		{"unknown start", time.Time{}, false, ago(2 * time.Hour), []string{"b0", "b1", "b2", "b3", "b4"}},
		{"pausing before insertion", ago(90 * time.Minute), true, ago(2 * time.Hour), []string{"b3", "b4"}},
		{"pause grace", ago(6 * time.Hour), true, ago(30 * time.Minute), []string{"b1", "b2", "b3", "b4"}},
		{"unknown finish", ago(6 * time.Hour), true, time.Time{}, []string{"b1", "b2", "b3", "b4"}},
		{"paused", ago(6 * time.Hour), true, ago(2 * time.Hour), []string{"b4"}},
		{"recent replacement", ago(6 * time.Hour), true, ago(2 * time.Hour), []string{"b3", "b4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := cleanup.Listing{
				Envs:     []cleanup.Env{{ID: "slot", Machine: "web"}},
				Machines: []cleanup.Machine{{ID: "web", Started: tc.start, Paused: tc.paused}},
			}
			for i, age := range []time.Duration{20 * time.Hour, 10 * time.Hour, 5 * time.Hour, 3 * time.Hour, 2 * time.Hour} {
				id := []string{"b0", "b1", "b2", "b3", "b4"}[i]
				b := ready(id, "slot", ago(age))
				if i == 4 {
					b.Finished = tc.finish
					if tc.name == "recent replacement" {
						b.In[0].At = ago(30 * time.Minute)
					}
				}
				l.Builds = append(l.Builds, b)
				l.Dirs = append(l.Dirs, dir(id, ago(24*time.Hour)))
			}
			p, err := cleanup.Garbage(l, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range tc.want {
				if _, ok := p.Roots[id]; !ok {
					t.Errorf("%s isn't a root: %v", id, p.Roots)
				}
				if slices.Contains(p.Delete, id) {
					t.Errorf("%s would be deleted", id)
				}
			}
			if len(p.Roots) != len(tc.want) {
				t.Errorf("roots = %v, want %v", p.Roots, tc.want)
			}
		})
	}
}

func TestNativeForkKeepsOtherMachineHistoryButNotRemovedSlots(t *testing.T) {
	for _, started := range []time.Time{ago(6 * time.Hour), time.Time{}} {
		l := cleanup.Listing{
			Envs:     []cleanup.Env{{ID: "web", Machine: "web"}, {ID: "gone", Machine: "gone", Deleted: ago(time.Minute)}},
			Machines: []cleanup.Machine{{ID: "fork", Started: ago(3 * time.Hour), Paused: true}, {ID: "older", Started: started}},
			Builds:   []cleanup.Build{ready("old", "web", ago(10*time.Hour)), ready("new", "web", ago(2*time.Hour)), ready("gone", "gone", ago(10*time.Hour))},
			Dirs:     []cleanup.Dir{dir("old", ago(10*time.Hour)), dir("new", ago(2*time.Hour)), dir("gone", ago(10*time.Hour))},
		}
		p, err := cleanup.Garbage(l, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.Roots["old"]; !ok || !slices.Equal(p.Delete, []string{"gone"}) {
			t.Fatalf("fork history = %+v", p)
		}
	}
}

func TestRootAccountingIncludesMissingDirectories(t *testing.T) {
	p, err := cleanup.Garbage(cleanup.Listing{Cached: []string{"missing"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Roots["missing"]; !ok || len(p.Keep) != 0 {
		t.Fatalf("missing root = %+v", p)
	}
}
