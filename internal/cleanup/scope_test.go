package cleanup_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/snapheader"
)

func TestTrimScopeAndCandidateRules(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		change      func(*cleanup.Listing, *trimStore)
		want        bool
	}{
		{"default named e2e", "", nil, true},
		{"e2e label", "e2e", func(l *cleanup.Listing, _ *trimStore) { l.Machines[0].Metadata = map[string]string{"hib-test": "e2e"} }, true},
		{"ordinary machine excluded", "e2e", func(l *cleanup.Listing, _ *trimStore) { l.Machines[0].Metadata = map[string]string{"hib.name": "web"} }, false},
		{"ordinary machine all", "all", func(l *cleanup.Listing, _ *trimStore) { l.Machines[0].Metadata = nil }, true},
		{"not listed", "e2e", func(l *cleanup.Listing, _ *trimStore) { l.Machines = nil }, false},
		{"off", "off", nil, false},
		{"unknown", "unexpected", nil, false},
		{"failed", "all", func(l *cleanup.Listing, _ *trimStore) { l.Builds[0].Status = "failed" }, false},
		{"abandoned unfinished", "all", func(l *cleanup.Listing, _ *trimStore) {
			l.Builds[0].Status = "pending"
			l.Builds[0].Created = ago(7 * time.Hour)
		}, false},
		{"recent root", "all", func(l *cleanup.Listing, _ *trimStore) { l.Dirs[0].Modified = ago(30 * time.Minute) }, false},
		{"image build", "all", func(l *cleanup.Listing, _ *trimStore) {
			l.Envs = append(l.Envs, cleanup.Env{ID: "image"})
			l.Builds[0].In = append(l.Builds[0].In, cleanup.Assignment{Env: "image", Tag: "default", At: ago(3 * time.Hour)})
		}, false},
		{"base", "all", func(_ *cleanup.Listing, s *trimStore) {
			s.headers[cleanup.FileKey{imageBuild, "memfile.header"}] = trimHeader(imageBuild, orphanA, snapheader.Mapping{Build: orphanA, Length: 1 << 20})
		}, false},
		{"running root", "all", func(l *cleanup.Listing, _ *trimStore) { l.Machines[0].Paused = false }, false},
		{"layer cache root", "all", func(l *cleanup.Listing, _ *trimStore) { l.Cached = []string{orphanA} }, false},
		{"unassigned", "all", func(l *cleanup.Listing, _ *trimStore) { l.Builds[0].In = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, s := trimFixture()
			if tc.change != nil {
				tc.change(&l, s)
			}
			log, err := runTrim(t, l, s, tc.scope, false)
			if err != nil || (len(s.applied) > 0) != tc.want {
				t.Fatalf("applied %v, log %v, err %v", s.applied, log, err)
			}
			if tc.name == "unknown" && !log.has(`not trimming: HIBD_TRIM is "unexpected", not off, e2e or all`) {
				t.Fatal(log)
			}
		})
	}
}

func TestSavedImageProtectsSharedSourcesOnlyInE2EScope(t *testing.T) {
	for _, scope := range []string{"e2e", "all"} {
		l, s := trimFixture()
		l.Envs = append(l.Envs, cleanup.Env{ID: "saved"})
		l.Builds = append(l.Builds, ready(freshBuild, "saved", ago(2*time.Hour)))
		l.Dirs = append(l.Dirs, dir(freshBuild, ago(2*time.Hour), orphanA))
		s.dirs[freshBuild] = true
		for _, kind := range []string{"memfile", "rootfs.ext4"} {
			s.headers[cleanup.FileKey{freshBuild, kind + ".header"}] = trimHeader(freshBuild, cachedBuild, snapheader.Mapping{Build: orphanA, StorageOffset: 2 << 20, Length: 1 << 20})
		}
		log, err := runTrim(t, l, s, scope, false)
		if err != nil || (len(s.applied) > 0) != (scope == "all") {
			t.Fatalf("%s: applied %v, log %v, err %v", scope, s.applied, log, err)
		}
		if scope == "all" && s.applied[0].Kept != 2<<20 {
			t.Fatal(s.applied)
		}
	}
}

func TestUnmappedAncestorLosesBothBodies(t *testing.T) {
	l, s := trimFixture()
	l.Builds = append(l.Builds, ready(orphanB, "web", ago(5*time.Hour)))
	l.Dirs[0].Refs = append(l.Dirs[0].Refs, orphanB)
	l.Dirs = append(l.Dirs, dir(orphanB, ago(5*time.Hour)))
	s.dirs[orphanB] = true
	for _, kind := range []string{"memfile", "rootfs.ext4"} {
		s.files[cleanup.FileKey{orphanB, kind}] = cleanup.FileState{Size: 4 << 20, Regular: true, Links: 1}
	}
	log, err := runTrim(t, l, s, "all", false)
	if err != nil || len(s.applied) != 4 || !log.has("trimmed 14 MiB in 4 files") {
		t.Fatalf("%v, %v, %v", s.applied, log, err)
	}
}

func TestInterruptedTrimReportsFreedBlocksAndContinues(t *testing.T) {
	l, s := trimFixture()
	done := map[cleanup.FileKey]bool{}
	for pass := 0; pass < 3; pass++ {
		ctx, cancel := context.WithCancel(t.Context())
		s.apply = func(ctx context.Context, p cleanup.FilePlan, _ bool) (cleanup.TrimResult, error) {
			if done[p.File.Key] {
				return cleanup.TrimResult{}, nil
			}
			done[p.File.Key] = true
			cancel()
			return cleanup.TrimResult{Freed: 3 << 20}, ctx.Err()
		}
		p, err := cleanup.Garbage(l, now)
		if err != nil {
			t.Fatal(err)
		}
		var log lines
		err = (cleanup.Trimmer{Storage: s, Scope: "all"}).Trim(ctx, l, p, now, false, log.logf)
		cancel()
		if pass < 2 && (err != context.Canceled || !log.has("trimmed 3 MiB in 1 files")) {
			t.Fatalf("pass %d: %v, %v", pass, log, err)
		}
		if pass == 2 && (err != nil || !log.has("nothing to trim")) {
			t.Fatalf("last pass: %v, %v", log, err)
		}
	}
	if len(done) != 2 {
		t.Fatal(fmt.Sprint(done))
	}
}
