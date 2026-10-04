package cleanup_test

import (
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
)

func TestSavedImageSharesOriginsSnapshotBuild(t *testing.T) {
	shared := ready("B", "origin-snapshots", ago(2*time.Hour))
	shared.In = append(shared.In, cleanup.Assignment{Env: "I", Tag: "default", At: ago(2 * time.Hour)})
	l := cleanup.Listing{
		Envs: []cleanup.Env{
			{ID: "origin-snapshots", Machine: "origin"},
			{ID: "I"},
		},
		Builds:   []cleanup.Build{shared},
		Dirs:     []cleanup.Dir{dir("B", ago(2*time.Hour))},
		Machines: []cleanup.Machine{{ID: "origin", Started: ago(time.Hour), Paused: true}},
	}
	wantGarbage(t, l)
	l.Envs[1].Deleted = ago(time.Minute)
	wantGarbage(t, l)
	l.Envs[0].Deleted = ago(time.Minute)
	l.Machines = nil
	p, err := cleanup.Garbage(l, now.Add(cleanup.MinAge))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Delete) != 1 || p.Delete[0] != "B" {
		t.Errorf("shared build after both envs removed and window passed = %+v", p)
	}
	l.Envs = append(l.Envs, cleanup.Env{ID: "copy-snapshots", Machine: "copy"})
	l.Builds = append(l.Builds, ready("copy-diff", "copy-snapshots", ago(time.Hour)))
	l.Dirs = append(l.Dirs, dir("copy-diff", ago(time.Hour), "B"))
	l.Machines = []cleanup.Machine{{ID: "copy", Started: ago(time.Hour), Paused: true}}
	p, err = cleanup.Garbage(l, now.Add(cleanup.MinAge))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Delete) != 0 || p.Keep["B"] != "copy-diff needs it" {
		t.Errorf("paused copy must keep its shared ancestor: %+v", p)
	}
}
