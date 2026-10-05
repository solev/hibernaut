package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

type getEngine struct {
	engine.Engine
	get func(context.Context, string) (engine.Machine, error)
}

func (e getEngine) Get(ctx context.Context, id string) (engine.Machine, error) {
	return e.get(ctx, id)
}

func TestCleanupRechecksPausedMachineBeforeTrimming(t *testing.T) {
	previousStart := ago(4 * time.Hour)
	currentStart := previousStart.Add(3 * time.Minute)
	for _, tc := range []struct {
		name string
		get  engine.Machine
		err  error
		root bool
	}{
		{"snapshotting", engine.Machine{State: engine.Running, StartedAt: currentStart}, nil, true},
		{"unknown running start", engine.Machine{State: engine.Running}, nil, true},
		{"paused cache has older start", engine.Machine{State: engine.Paused, StartedAt: ago(10 * time.Hour)}, nil, false},
		{"not found", engine.Machine{}, fmt.Errorf("gone: %w", engine.ErrNotFound), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, s := trimFixture()
			l.Machines[0].Started = previousStart
			l.Builds[0] = ready(orphanA, "web", previousStart.Add(-time.Hour))
			l.Builds[1] = ready(imageBuild, "web", previousStart.Add(2*time.Minute))
			h := hostForTrimListing(t, l, s)
			ctx := t.Context()
			gets, queries := 0, 0
			h.Engine = getEngine{h.Engine, func(got context.Context, id string) (engine.Machine, error) {
				gets++
				if got != ctx || id != "web" || queries != 0 {
					t.Errorf("Get context = %v, id = %q, preceding queries = %d", got, id, queries)
				}
				return tc.get, tc.err
			}}
			query := h.Query
			h.Query = func(ctx context.Context, sql string) ([][]string, error) {
				queries++
				return query(ctx, sql)
			}
			var log lines
			if err := h.Clean(ctx, now, true, log.logf); err != nil {
				t.Fatal(err)
			}
			if gets != 1 || queries != 2 {
				t.Errorf("Get calls = %d, database queries = %d", gets, queries)
			}
			root := false
			for _, line := range log {
				if strings.HasPrefix(line, "keep "+orphanA+" ") && strings.HasSuffix(line, "a machine may have started from it") {
					root = true
				}
			}
			if root != tc.root {
				t.Errorf("warm-cache input is a root = %v, want %v: %v", root, tc.root, log)
			}
			if tc.root && len(s.applied) != 0 {
				t.Fatalf("trimmed a running machine's warm-cache input: %v", s.applied)
			}
			if !tc.root && (len(s.applied) != 2 || !log.has("would trim about 6 MiB in 2 files")) {
				t.Fatalf("paused control didn't trim the warm-cache input: %v, %v", s.applied, log)
			}
		})
	}
}

func TestCleanupPausedGetErrorStopsDeletionAndTrimming(t *testing.T) {
	h := newHost(t, now)
	e := fake.New()
	e.Add(engine.Machine{ID: "web", State: engine.Paused})
	want := errors.New("E2B is unavailable")
	h.Engine = getEngine{e, func(context.Context, string) (engine.Machine, error) {
		return engine.Machine{}, fmt.Errorf("getting web: %w", want)
	}}
	query := h.Query
	h.Query = func(ctx context.Context, sql string) ([][]string, error) {
		t.Error("read the database after Get failed")
		return query(ctx, sql)
	}
	h.OpenTrim = func(string) (cleanup.TrimStorage, error) {
		t.Error("opened storage after Get failed")
		return nil, errors.New("unexpected trim")
	}
	var log lines
	if err := h.Clean(t.Context(), now, false, log.logf); !errors.Is(err, want) {
		t.Errorf("Clean error = %v, want %v", err, want)
	}
	wantDirs(t, h, imageBuild, cachedBuild, freshBuild, orphanA, orphanB)
	if len(log) != 0 {
		t.Errorf("cleanup logged changes after Get failed: %v", log)
	}
}
