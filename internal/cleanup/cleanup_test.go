package cleanup_test

import (
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
)

func TestParseHeader(t *testing.T) {
	// The memory header of a machine paused five times, copied from the host on 2026-10-02 and
	// decoded there by a separate script. Its memory is spread over its own build, three older
	// ones and the image's; the image's is also its base.
	header, err := os.ReadFile("testdata/memfile.header")
	if err != nil {
		t.Fatal(err)
	}
	got, err := cleanup.ParseHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"1d1f70d3-b7f3-409c-8d70-6bbed68ac641",
		"2a34beb7-7905-4247-961f-8dc69dece14a",
		"32c6a325-5931-4c5f-bad8-d46dbcd44499",
		"3ec94b54-c045-4dcc-8f8b-7dd451fa4dfe",
		"a5351269-f6d4-4975-aa38-df3ff5692353",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParseHeader = %q, want %q", got, want)
	}
}

func TestParseHeaderRefuses(t *testing.T) {
	header, err := os.ReadFile("testdata/memfile.header")
	if err != nil {
		t.Fatal(err)
	}
	version := func(v uint64) []byte {
		h := slices.Clone(header)
		binary.LittleEndian.PutUint64(h, v)
		return h
	}
	for name, h := range map[string][]byte{
		"empty":                  nil,
		"cut in its metadata":    header[:63],
		"cut in a mapping":       header[:len(header)-1],
		"compressed (version 4)": version(4),
		"unknown version 0":      version(0),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := cleanup.ParseHeader(h); err == nil {
				t.Errorf("ParseHeader = %q, want an error", got)
			}
		})
	}
}

var now = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

// ready is a build E2B started in env, and finished a minute later, at finished.
func ready(id, env string, finished time.Time) cleanup.Build {
	created := finished.Add(-time.Minute)
	return cleanup.Build{ID: id, Status: "ready", Created: created, Finished: finished, In: in(env, created)}
}

// in says E2B put a build in env at at, under the default tag.
func in(env string, at time.Time) []cleanup.Assignment {
	return []cleanup.Assignment{{Env: env, Tag: "default", At: at}}
}

// dir is a build's directory, last changed at modified, whose headers name refs.
func dir(id string, modified time.Time, refs ...string) cleanup.Dir {
	return cleanup.Dir{Build: id, Modified: modified, Refs: refs}
}

// garbage returns the directories Garbage deletes, and checks that it keeps all the others.
func garbage(t *testing.T, l cleanup.Listing) []string {
	t.Helper()
	plan, err := cleanup.Garbage(l, now)
	if err != nil {
		t.Fatalf("Garbage: %v", err)
	}
	for _, d := range l.Dirs {
		_, kept := plan.Keep[d.Build]
		if kept == slices.Contains(plan.Delete, d.Build) {
			t.Errorf("%s is kept (%v) and deleted (%v)", d.Build, kept, !kept)
		}
	}
	return plan.Delete
}

func wantGarbage(t *testing.T, l cleanup.Listing, want ...string) {
	t.Helper()
	if got := garbage(t, l); !slices.Equal(got, want) {
		t.Errorf("Garbage deletes %q, want %q", got, want)
	}
}

func TestKeepsMachinesSnapshots(t *testing.T) {
	// web was paused three times. Its latest snapshot holds ranges of its first one and of the
	// image build it started from, but none of its second.
	wantGarbage(t, cleanup.Listing{
		Envs: []cleanup.Env{
			{ID: "img"},
			{ID: "snap-web", Machine: "web"},
		},
		Builds: []cleanup.Build{
			ready("img-1", "img", ago(30*time.Hour)),
			ready("img-2", "img", ago(20*time.Hour)),
			ready("web-1", "snap-web", ago(10*time.Hour)),
			ready("web-2", "snap-web", ago(9*time.Hour)),
			ready("web-3", "snap-web", ago(8*time.Hour)),
		},
		Dirs: []cleanup.Dir{
			dir("img-1", ago(30*time.Hour), "img-1"),
			dir("img-2", ago(20*time.Hour), "img-2"),
			dir("web-1", ago(10*time.Hour), "img-1", "web-1"),
			dir("web-2", ago(9*time.Hour), "img-1", "web-1", "web-2"),
			dir("web-3", ago(8*time.Hour), "img-1", "web-1", "web-3"),
		},
		Machines: []cleanup.Machine{{ID: "web", Started: ago(9 * time.Hour), Paused: true}},
	}, "web-2")
}

func TestKeepsOnlyTheCurrentImageBuild(t *testing.T) {
	// img was built twice, and failed to build a third time.
	wantGarbage(t, cleanup.Listing{
		Envs: []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{
			ready("img-1", "img", ago(30*time.Hour)),
			ready("img-2", "img", ago(20*time.Hour)),
			{ID: "img-3", Status: "failed", Created: ago(10 * time.Hour), Finished: ago(10 * time.Hour), In: in("img", ago(10*time.Hour))},
		},
		Dirs: []cleanup.Dir{
			dir("img-1", ago(30*time.Hour), "img-1"),
			dir("img-2", ago(20*time.Hour), "img-2"),
			dir("img-3", ago(10*time.Hour), "img-3"),
		},
	}, "img-1", "img-3")
}

func TestKeepsEachTagsBuild(t *testing.T) {
	// Each tag of an image names a build of its own: the ready one E2B put in the image last
	// under that tag. img-1 is tagged stable as well.
	img1 := ready("img-1", "img", ago(30*time.Hour))
	img1.In = append(img1.In, cleanup.Assignment{Env: "img", Tag: "stable", At: ago(25 * time.Hour)})
	wantGarbage(t, cleanup.Listing{
		Envs:   []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{img1, ready("img-2", "img", ago(20*time.Hour)), ready("img-3", "img", ago(10*time.Hour))},
		Dirs: []cleanup.Dir{
			dir("img-1", ago(30*time.Hour), "img-1"),
			dir("img-2", ago(20*time.Hour), "img-2"),
			dir("img-3", ago(10*time.Hour), "img-3"),
		},
	}, "img-2")
}

func TestKeepsTheBuildE2BStartsFrom(t *testing.T) {
	// img-1 was put back in img 5 hours ago, so E2B starts machines from it again, though img-3
	// is newer. img-3 stays too, in case a later E2B goes by when builds finished.
	img1 := ready("img-1", "img", ago(30*time.Hour))
	img1.In = append(img1.In, cleanup.Assignment{Env: "img", Tag: "default", At: ago(5 * time.Hour)})
	wantGarbage(t, cleanup.Listing{
		Envs:   []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{img1, ready("img-2", "img", ago(20*time.Hour)), ready("img-3", "img", ago(10*time.Hour))},
		Dirs: []cleanup.Dir{
			dir("img-1", ago(30*time.Hour), "img-1"),
			dir("img-2", ago(20*time.Hour), "img-2"),
			dir("img-3", ago(10*time.Hour), "img-3"),
		},
	}, "img-2")
}

func TestKeepsWhatChangedInTheLastHour(t *testing.T) {
	// E2B may still be writing a directory changed in the last hour, maybe before its build is
	// in the database.
	wantGarbage(t, cleanup.Listing{
		Envs: []cleanup.Env{{ID: "snap-web", Machine: "web"}},
		Builds: []cleanup.Build{
			ready("web-1", "snap-web", ago(40*time.Minute)),
			ready("web-2", "snap-web", ago(5*time.Minute)),
		},
		Dirs: []cleanup.Dir{
			dir("web-1", ago(40*time.Minute), "web-1"),
			dir("web-2", ago(5*time.Minute), "web-2"),
			dir("fresh", ago(10*time.Minute), "fresh"),
			dir("stale", ago(2*time.Hour), "stale"),
		},
		Machines: []cleanup.Machine{{ID: "web", Started: ago(30 * time.Minute), Paused: true}},
	}, "stale")
}

func TestDeletesRemovedMachinesSnapshotsAtOnce(t *testing.T) {
	// hib rm frees the disk a machine's snapshots took as soon as hibd notices.
	wantGarbage(t, cleanup.Listing{
		Envs: []cleanup.Env{{ID: "snap-gone", Machine: "gone", Deleted: ago(time.Minute)}},
		Builds: []cleanup.Build{
			ready("gone-1", "snap-gone", ago(20*time.Minute)),
			ready("gone-2", "snap-gone", ago(5*time.Minute)),
		},
		Dirs: []cleanup.Dir{
			dir("gone-1", ago(20*time.Minute), "gone-1"),
			dir("gone-2", ago(5*time.Minute), "gone-1", "gone-2"),
		},
	}, "gone-1", "gone-2")
}

func TestKeepsBuildsAnotherEnvUses(t *testing.T) {
	// E2B can put a build in more than one env. A snapshot template is an env made from a
	// machine's snapshot, and it outlives the machine.
	web2 := ready("web-2", "snap-web", ago(10*time.Hour))
	web2.In = append(web2.In, cleanup.Assignment{Env: "tmpl", Tag: "default", At: ago(9 * time.Hour)})
	wantGarbage(t, cleanup.Listing{
		Envs: []cleanup.Env{
			{ID: "snap-web", Machine: "web", Deleted: ago(2 * time.Hour)},
			{ID: "tmpl"},
			{ID: "snap-gone", Machine: "gone", Deleted: ago(2 * time.Hour)},
		},
		Builds: []cleanup.Build{
			ready("web-1", "snap-web", ago(11*time.Hour)),
			web2,
			ready("gone-1", "snap-gone", ago(11*time.Hour)),
		},
		Dirs: []cleanup.Dir{
			dir("web-1", ago(11*time.Hour), "web-1"),
			dir("web-2", ago(10*time.Hour), "web-1", "web-2"),
			dir("gone-1", ago(11*time.Hour), "gone-1"),
		},
	}, "gone-1")
}

func TestKeepsImageBuildsMachinesMayHaveStartedFrom(t *testing.T) {
	// A machine that started from an image build needs it until its first pause is done: only
	// then do its snapshot's headers name it. E2B lists a machine it's still pausing as paused.
	// img was built 10, 5 and 2 hours ago.
	builds := []cleanup.Build{
		ready("img-1", "img", ago(10*time.Hour)),
		ready("img-2", "img", ago(5*time.Hour)),
		ready("img-3", "img", ago(2*time.Hour)),
	}
	dirs := []cleanup.Dir{
		dir("img-1", ago(10*time.Hour), "img-1"),
		dir("img-2", ago(5*time.Hour), "img-2"),
		dir("img-3", ago(2*time.Hour), "img-3"),
	}
	snapshotted := []cleanup.Env{{ID: "img"}, {ID: "snap-web", Machine: "web"}}
	for _, tc := range []struct {
		name    string
		envs    []cleanup.Env
		builds  []cleanup.Build
		machine cleanup.Machine
		want    []string
	}{
		{name: "running for 6 hours", machine: cleanup.Machine{ID: "web", Started: ago(6 * time.Hour)}},
		{name: "running for 3 hours", machine: cleanup.Machine{ID: "web", Started: ago(3 * time.Hour)}, want: []string{"img-1"}},
		{name: "running for 10 minutes", machine: cleanup.Machine{ID: "web", Started: ago(10 * time.Minute)}, want: []string{"img-1", "img-2"}},
		{
			name:    "pausing for the first time",
			machine: cleanup.Machine{ID: "web", Started: ago(6 * time.Hour), Paused: true},
		},
		{
			name: "pausing, its snapshot not done yet", envs: snapshotted,
			builds:  []cleanup.Build{{ID: "web-1", Status: "failed", Created: ago(2 * time.Hour), In: in("snap-web", ago(2*time.Hour))}},
			machine: cleanup.Machine{ID: "web", Started: ago(6 * time.Hour), Paused: true},
		},
		{
			name: "paused", envs: snapshotted,
			builds:  []cleanup.Build{ready("web-1", "snap-web", ago(3*time.Hour))},
			machine: cleanup.Machine{ID: "web", Started: ago(6 * time.Hour), Paused: true},
			want:    []string{"img-1", "img-2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envs := tc.envs
			if envs == nil {
				envs = []cleanup.Env{{ID: "img"}}
			}
			wantGarbage(t, cleanup.Listing{
				Envs:     envs,
				Builds:   append(slices.Clone(builds), tc.builds...),
				Dirs:     dirs,
				Machines: []cleanup.Machine{tc.machine},
			}, tc.want...)
		})
	}
}

func TestKeepsAnImageBuildAMachineStartedFromThisHour(t *testing.T) {
	// img was rebuilt 30 minutes ago. hibd may have started a machine from the build before it
	// just before that, and may not list that machine yet.
	wantGarbage(t, cleanup.Listing{
		Envs: []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{
			ready("img-1", "img", ago(10*time.Hour)),
			ready("img-2", "img", ago(5*time.Hour)),
			ready("img-3", "img", ago(30*time.Minute)),
		},
		Dirs: []cleanup.Dir{
			dir("img-1", ago(10*time.Hour), "img-1"),
			dir("img-2", ago(5*time.Hour), "img-2"),
			dir("img-3", ago(30*time.Minute), "img-3"),
		},
	}, "img-1")
}

func TestForgetsRemovedImages(t *testing.T) {
	// A removed image starts no more machines, so its builds go once no machine may have started
	// from them. old was removed 3 hours ago, and new half an hour ago: hibd may have started a
	// machine from it just before.
	l := cleanup.Listing{
		Envs: []cleanup.Env{{ID: "old", Deleted: ago(3 * time.Hour)}, {ID: "new", Deleted: ago(30 * time.Minute)}},
		Builds: []cleanup.Build{
			ready("old-1", "old", ago(30*time.Hour)),
			ready("new-1", "new", ago(30*time.Hour)),
		},
		Dirs: []cleanup.Dir{dir("old-1", ago(30*time.Hour), "old-1"), dir("new-1", ago(30*time.Hour), "new-1")},
	}
	wantGarbage(t, l, "old-1")

	// A machine that has run for 4 hours may have started from old.
	l.Machines = []cleanup.Machine{{ID: "web", Started: ago(4 * time.Hour)}}
	wantGarbage(t, l)

	// E2B checks that an image isn't removed before it starts a machine, and stamps the start
	// once the machine runs, so a machine may have started from old a little after its removal.
	l.Machines = []cleanup.Machine{{ID: "web", Started: ago(3*time.Hour - time.Minute)}}
	wantGarbage(t, l)
}

func TestKeepsBuildsTheLayerCacheUses(t *testing.T) {
	// Building an image makes a build for each step, outside the database. E2B's layer cache
	// points to them, so that the next image build can reuse them.
	wantGarbage(t, cleanup.Listing{
		Dirs: []cleanup.Dir{
			dir("layer-1", ago(30*time.Hour), "layer-1"),
			dir("layer-2", ago(30*time.Hour), "layer-1", "layer-2"),
			dir("evicted", ago(30*time.Hour), "evicted"),
		},
		Cached: []string{"layer-2"},
	}, "evicted")
}

func TestWaitsWhileE2BWritesABuild(t *testing.T) {
	// Until a build is done, nothing says which builds it needs.
	for _, status := range []string{"pending", "in_progress"} {
		_, err := cleanup.Garbage(cleanup.Listing{
			Envs:   []cleanup.Env{{ID: "snap-web", Machine: "web"}},
			Builds: []cleanup.Build{{ID: "web-1", Status: status, Created: ago(time.Minute), In: in("snap-web", ago(time.Minute))}},
			Dirs:   []cleanup.Dir{dir("web-0", ago(30*time.Hour), "web-0")},
		}, now)
		if !errors.Is(err, cleanup.ErrBusy) {
			t.Errorf("%s: Garbage error = %v, want ErrBusy", status, err)
		}
	}
}

func TestIgnoresBuildsE2BGaveUpOn(t *testing.T) {
	// E2B stopped while building img-2, which is still in progress 7 hours later.
	wantGarbage(t, cleanup.Listing{
		Envs: []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{
			ready("img-1", "img", ago(30*time.Hour)),
			{ID: "img-2", Status: "in_progress", Created: ago(7 * time.Hour), In: in("img", ago(7*time.Hour))},
		},
		Dirs: []cleanup.Dir{dir("img-1", ago(30*time.Hour), "img-1"), dir("img-2", ago(7*time.Hour), "img-2")},
	}, "img-2")
}

func TestStopsOnABuildStatusItDoesntKnow(t *testing.T) {
	_, err := cleanup.Garbage(cleanup.Listing{
		Envs:   []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{{ID: "img-1", Status: "", Created: ago(30 * time.Hour), In: in("img", ago(30*time.Hour))}},
		Dirs:   []cleanup.Dir{dir("img-1", ago(30*time.Hour), "img-1")},
	}, now)
	if err == nil || errors.Is(err, cleanup.ErrBusy) {
		t.Errorf("Garbage error = %v, want one about the status", err)
	}
}

func TestStopsIfItCantReadAKeptHeader(t *testing.T) {
	// Not knowing which builds img-2 needs, any other directory may be one of them.
	unreadable := errors.New("header version 4 isn't supported")
	l := cleanup.Listing{
		Envs:   []cleanup.Env{{ID: "img"}},
		Builds: []cleanup.Build{ready("img-1", "img", ago(30*time.Hour)), ready("img-2", "img", ago(20*time.Hour))},
		Dirs: []cleanup.Dir{
			dir("img-1", ago(30*time.Hour), "img-1"),
			{Build: "img-2", Modified: ago(20 * time.Hour), Err: unreadable},
		},
	}
	if _, err := cleanup.Garbage(l, now); !errors.Is(err, unreadable) {
		t.Errorf("Garbage error = %v, want %v", err, unreadable)
	}

	// Which builds a directory that goes needs doesn't matter.
	l.Dirs = []cleanup.Dir{
		{Build: "img-1", Modified: ago(30 * time.Hour), Err: unreadable},
		dir("img-2", ago(20*time.Hour), "img-2"),
	}
	wantGarbage(t, l, "img-1")
}
