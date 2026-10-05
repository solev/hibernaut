package cleanup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
)

type listedEngine struct {
	engine.Engine
	list func(context.Context, engine.ListOptions) ([]engine.Machine, error)
}

func (e listedEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	return e.list(ctx, opts)
}

func TestCleanupReadsInsideGuardAndTrimsAfterRelease(t *testing.T) {
	h, s := hostForTrim(t)
	held, reads, released := false, 0, 0
	h.ReadGuard = func(context.Context) (func(), error) {
		held = true
		return func() {
			held = false
			released++
			index := filepath.Join(h.Cache, "scope", "index")
			if err := os.MkdirAll(index, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(index, strings.Repeat("0a", 32)), []byte("not JSON"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(h.Storage, imageBuild, "memfile.header"), []byte("invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, nil
	}
	e := h.Engine
	h.Engine = listedEngine{e, func(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
		if !held {
			t.Error("List ran outside guard")
		}
		reads++
		return e.List(ctx, opts)
	}}
	query := h.Query
	h.Query = func(ctx context.Context, sql string) ([][]string, error) {
		if !held {
			t.Error("SQL ran outside guard")
		}
		reads++
		return query(ctx, sql)
	}
	h.CheckSaves = func(context.Context, cleanup.Listing, bool, func(string, ...any)) error {
		if !held {
			t.Error("save records ran outside guard")
		}
		reads++
		return nil
	}
	h.OpenTrim = func(string) (cleanup.TrimStorage, error) {
		if held || released != 1 {
			t.Error("trimming opened storage before releasing guard")
		}
		return s, nil
	}
	var log lines
	if err := h.Clean(t.Context(), now, true, log.logf); err != nil {
		t.Fatal(err)
	}
	if reads != 4 || released != 1 || len(s.applied) != 2 {
		t.Fatalf("reads %d, released %d, trims %d: %v", reads, released, len(s.applied), log)
	}
}

func TestCleanupGuardFailureStartsNoReads(t *testing.T) {
	h, _ := hostForTrim(t)
	want := errors.New("save is in progress")
	h.ReadGuard = func(context.Context) (func(), error) { return nil, want }
	h.Engine = listedEngine{h.Engine, func(context.Context, engine.ListOptions) ([]engine.Machine, error) {
		t.Fatal("List ran without the guard")
		return nil, nil
	}}
	var log lines
	if err := h.Clean(t.Context(), now, false, log.logf); !errors.Is(err, want) || len(log) != 0 {
		t.Fatalf("guard failure = %v, log = %v", err, log)
	}
}
