package cleanup_test

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/snapheader"
)

func TestCleanupLaterPauseProtectsFailedSaveSources(t *testing.T) {
	l, s := trimFixture()
	failed := ready(orphanB, "web", ago(4*time.Hour))
	failed.Status = "failed"
	l.Builds = append(l.Builds, failed)
	l.Dirs = append(l.Dirs, dir(orphanB, ago(4*time.Hour)))
	l.Dirs[1].Refs = append(l.Dirs[1].Refs, orphanB)
	s.dirs[orphanB] = true
	for _, kind := range []string{"memfile", "rootfs.ext4"} {
		s.headers[cleanup.FileKey{orphanB, kind + ".header"}] = trimHeader(orphanB, cachedBuild)
		s.files[cleanup.FileKey{orphanB, kind}] = cleanup.FileState{Size: 4 << 20, Regular: true, Links: 1}
		s.headers[cleanup.FileKey{imageBuild, kind + ".header"}] = trimHeader(imageBuild, cachedBuild,
			snapheader.Mapping{Build: orphanB, Length: 1 << 20},
			snapheader.Mapping{Offset: 1 << 20, Build: orphanA, StorageOffset: 1 << 20, Length: 1 << 20})
	}
	p, err := cleanup.Garbage(l, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := p.Keep[orphanB]; !kept || slices.Contains(p.Delete, orphanB) {
		t.Fatal("the later pause did not keep its failed input", p)
	}
	var log lines
	if err := (cleanup.Trimmer{Storage: s, Scope: "all"}).Trim(t.Context(), l, p, now, false, log.logf); err != nil {
		t.Fatal(err)
	}
	if len(s.applied) != 2 {
		t.Fatalf("trim plans = %v, log = %v", s.applied, log)
	}
	for _, p := range s.applied {
		if p.File.Key.Build != orphanA || p.Kept != 1<<20 || !slices.Equal(p.Holes, []cleanup.Range{{Start: 0, End: 1 << 20}, {Start: 2 << 20, End: 4 << 20}}) {
			t.Fatalf("failed source or required range was punched: %+v", p)
		}
	}
}

func TestCleanupUnsettledFailedSaveStopsBeforePlanning(t *testing.T) {
	h, _ := hostForTrim(t)
	query := h.Query
	h.Query = func(ctx context.Context, sql string) ([][]string, error) {
		rows, err := query(ctx, sql)
		if strings.Contains(sql, "FROM env_builds") {
			rows = slices.DeleteFunc(rows, func(row []string) bool { return row[0] == imageBuild })
			for _, row := range rows {
				row[1] = "failed"
			}
		}
		return rows, err
	}
	h.OpenTrim = func(string) (cleanup.TrimStorage, error) {
		t.Fatal("unsettled failed save opened trim storage")
		return nil, nil
	}
	want := errors.New("failed save is unsettled")
	h.CheckSaves = func(_ context.Context, l cleanup.Listing, _ bool, _ func(string, ...any)) error {
		r := cleanup.SaveRecord{Machine: "web", Name: "web", Started: ago(4 * time.Hour), Ended: ago(3 * time.Hour)}
		if cleanup.SaveSettled(l, r, "", 10*time.Minute) {
			t.Fatal("failed save without a later pause settled")
		}
		return want
	}
	var log lines
	if err := h.Clean(t.Context(), now, false, log.logf); !errors.Is(err, want) || len(log) != 0 {
		t.Fatalf("unsettled save = %v, log = %v", err, log)
	}
	for _, id := range []string{orphanA, imageBuild} {
		if _, err := os.Stat(filepath.Join(h.Storage, id)); err != nil {
			t.Fatalf("cleanup deleted %s: %v", id, err)
		}
	}
}

func TestCleanupStrictHeaderRefusalDoesNotChangeDeletion(t *testing.T) {
	h, s := hostForTrim(t)
	key := cleanup.FileKey{imageBuild, "memfile.header"}
	binary.LittleEndian.PutUint64(s.headers[key], 0x10003)
	if err := os.WriteFile(filepath.Join(h.Storage, imageBuild, key.Name), s.headers[key], 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(h.Storage, imageBuild), ago(2*time.Hour))
	unused := filepath.Join(h.Storage, orphanB)
	if err := os.Mkdir(unused, 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, unused, ago(2*time.Hour))
	var log lines
	if err := h.Clean(t.Context(), now, false, log.logf); err != nil {
		t.Fatal(err)
	}
	if len(s.applied) != 0 || !log.has("deleted "+orphanB) || !log.has("not trimming: "+imageBuild+" memfile.header") {
		t.Fatalf("trim = %v, log %v", s.applied, log)
	}
}

func TestCleanupMissingRootHeaderReportsTrimRefusalAndKeepsM2Failure(t *testing.T) {
	h, _ := hostForTrim(t)
	if err := os.Remove(filepath.Join(h.Storage, imageBuild, "memfile.header")); err != nil {
		t.Fatal(err)
	}
	var log lines
	err := h.Clean(t.Context(), now, false, log.logf)
	if err == nil || !log.has("not trimming: ") || !log.has(imageBuild) || !log.has("memfile.header") {
		t.Fatalf("missing header = %v, %v", log, err)
	}
}

func hostForTrim(t *testing.T) (cleanup.Host, *trimStore) {
	t.Helper()
	l, s := trimFixture()
	storage := t.TempDir()
	for _, d := range l.Dirs {
		path := filepath.Join(storage, d.Build)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range cleanup.Headers {
			if err := os.WriteFile(filepath.Join(path, name), s.headers[cleanup.FileKey{d.Build, name}], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		touch(t, path, d.Modified)
	}
	e := fake.New()
	for _, m := range l.Machines {
		e.Add(engine.Machine{ID: m.ID, StartedAt: m.Started, State: engine.Paused, Metadata: m.Metadata})
	}
	query := func(ctx context.Context, sql string) ([][]string, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(sql, "FROM envs") {
			return [][]string{{"web", "web", ""}}, nil
		}
		var rows [][]string
		for _, b := range l.Builds {
			micros := func(at time.Time) string { return strconv.FormatInt(at.UnixMicro(), 10) }
			rows = append(rows, []string{b.ID, b.Status, micros(b.Created), micros(b.Finished), "web", "default", micros(b.In[0].At)})
		}
		slices.SortFunc(rows, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
		return rows, nil
	}
	return cleanup.Host{Storage: storage, Cache: t.TempDir(), Query: query, Engine: e, OpenTrim: func(string) (cleanup.TrimStorage, error) { return s, nil }}, s
}

func TestCleanupPassesE2EMetadataAndAppendsTrimOutput(t *testing.T) {
	h, s := hostForTrim(t)
	var log lines
	if err := h.Clean(t.Context(), now, true, log.logf); err != nil {
		t.Fatal(err)
	}
	if len(s.applied) != 2 || !log.has("would trim about 6 MiB in 2 files") {
		t.Fatalf("%v, %v", s.applied, log)
	}
	deletion, trim := -1, -1
	for i, line := range log {
		if strings.HasPrefix(line, "nothing to delete") {
			deletion = i
		}
		if strings.HasPrefix(line, "would trim ") && trim < 0 {
			trim = i
		}
	}
	if deletion < 0 || trim <= deletion {
		t.Fatal(log)
	}
}

func TestCleanupTrimBudgetIsSeparateAndRepeatedCutsFinish(t *testing.T) {
	h, s := hostForTrim(t)
	h.ListingTimeout = 30 * time.Second
	h.TrimTimeout = 2 * time.Minute
	var budgets []time.Duration
	var listCtx context.Context
	var cut context.CancelFunc
	h.Timeout = func(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
		budgets = append(budgets, budget)
		child, cancel := context.WithCancel(ctx)
		if budget == h.ListingTimeout {
			listCtx = child
		} else {
			cut = cancel
		}
		return child, cancel
	}
	done := map[cleanup.FileKey]bool{}
	s.apply = func(ctx context.Context, p cleanup.FilePlan, _ bool) (cleanup.TrimResult, error) {
		if listCtx.Err() == nil || ctx.Err() != nil {
			t.Error("listing budget wasn't closed before fresh trim budget")
		}
		if done[p.File.Key] {
			return cleanup.TrimResult{}, nil
		}
		done[p.File.Key] = true
		cut()
		return cleanup.TrimResult{Freed: 3 << 20}, ctx.Err()
	}
	for pass := 0; pass < 3; pass++ {
		var log lines
		err := h.Clean(t.Context(), now, false, log.logf)
		if pass < 2 && (!errors.Is(err, cleanup.ErrTrimTimeout) || !log.has("trimmed 3 MiB in 1 files")) {
			t.Fatalf("pass %d: %v, %v", pass, log, err)
		}
		if pass == 2 && (err != nil || !log.has("nothing to trim")) {
			t.Fatalf("final pass: %v, %v", log, err)
		}
	}
	if !slices.Equal(budgets, []time.Duration{30 * time.Second, 2 * time.Minute, 30 * time.Second, 2 * time.Minute, 30 * time.Second, 2 * time.Minute}) {
		t.Fatal(budgets)
	}
}

func TestCleanupUnknownTrimValueDoesNotStopDeletion(t *testing.T) {
	h := newHost(t, now)
	h.TrimScope = "mistyped"
	h.OpenTrim = func(string) (cleanup.TrimStorage, error) {
		t.Error("opened storage with trimming disabled")
		return nil, nil
	}
	var log lines
	if err := h.Clean(t.Context(), now, false, log.logf); err != nil {
		t.Fatal(err)
	}
	wantDirs(t, h, imageBuild, cachedBuild, freshBuild)
	if !log.has(`not trimming: HIBD_TRIM is "mistyped", not off, e2e or all`) {
		t.Fatal(log)
	}
}
