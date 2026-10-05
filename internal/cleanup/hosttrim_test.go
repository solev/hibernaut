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
)

func TestHostStrictHeaderRefusalDoesNotChangeDeletion(t *testing.T) {
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

func TestHostMissingRootHeaderReportsTrimRefusalAndKeepsM2Failure(t *testing.T) {
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

func TestHostPassesE2EMetadataAndAppendsTrimOutput(t *testing.T) {
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

func TestHostTrimBudgetIsSeparateAndRepeatedCutsFinish(t *testing.T) {
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

func TestHostUnknownTrimValueDoesNotStopDeletion(t *testing.T) {
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
