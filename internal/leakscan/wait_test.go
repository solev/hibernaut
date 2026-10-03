package leakscan_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/leakscan"
	"github.com/solev/hibernaut/internal/leakscan/leakscantest"
)

func TestWaitBuildWaitsForReadyAndEveryUpload(t *testing.T) {
	storage := t.TempDir()
	leakscantest.WriteBuild(t, storage, buildA, []byte("old clean memory"), []byte("old clean disk"))
	uploads := t.TempDir()
	leakscantest.WriteBuild(t, uploads, buildB, []byte("memory value-123"), []byte("disk"))
	status := "pending"
	now := time.Unix(0, 0)
	events := []func(){
		func() { status = "in_progress" },
		func() { status = "ready" },
	}
	for _, name := range []string{"memfile.header", "rootfs.ext4.header", "snapfile", "metadata.json", "memfile", "rootfs.ext4"} {
		events = append(events, func() {
			b, err := os.ReadFile(filepath.Join(uploads, buildB, name))
			if err != nil {
				t.Fatal(err)
			}
			leakscantest.WriteBuildFile(t, storage, buildB, name, b)
		})
	}
	wait := leakscan.WaitOptions{
		Timeout: 10 * time.Second,
		Now:     func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			now = now.Add(d)
			if len(events) > 0 {
				event := events[0]
				events = events[1:]
				event()
			}
			return nil
		},
	}
	query := func(_ context.Context, _ string) ([][]string, error) {
		return [][]string{{status}}, nil
	}
	if err := leakscan.WaitBuild(t.Context(), query, storage, buildB, wait); err != nil {
		t.Fatal(err)
	}
	if got := now.Sub(time.Unix(0, 0)); got != 8*time.Second {
		t.Fatalf("ready after %s, want all uploads after 8s", got)
	}
	hits, err := leakscan.ScanBuild(storage, buildB, []leakscan.Needle{{Secret: "token", Encoding: "raw", Pattern: []byte("value-123")}})
	if err != nil || !slices.Contains(scanHitKeys(hits), "token/raw@memory:7") {
		t.Fatalf("latest snapshot = %+v, %v, want memory hit at 7", hits, err)
	}
}

func TestWaitBuildWaitsForTheFurthestStorageOffset(t *testing.T) {
	storage := t.TempDir()
	leakscantest.WriteBuild(t, storage, buildB, []byte("padding-value-123"), []byte("disk"))
	leakscantest.WriteHeader(t, storage, buildB, "memfile.header", 9,
		leakscantest.Mapping{Length: 9, Build: buildB, StorageOffset: 8})
	if err := os.Truncate(filepath.Join(storage, buildB, "memfile"), 9); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	query := func(_ context.Context, _ string) ([][]string, error) { return [][]string{{"ready"}}, nil }
	wait := leakscan.WaitOptions{
		Timeout: 2 * time.Second,
		Now:     func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			now = now.Add(d)
			leakscantest.WriteBuildFile(t, storage, buildB, "memfile", []byte("padding-value-123"))
			return nil
		},
	}
	if err := leakscan.WaitBuild(t.Context(), query, storage, buildB, wait); err != nil {
		t.Fatal(err)
	}
	if got := now.Sub(time.Unix(0, 0)); got != time.Second {
		t.Fatalf("waited %s, want 1s for the mapped storage extent", got)
	}
	hits, err := leakscan.ScanBuild(storage, buildB,
		[]leakscan.Needle{{Secret: "token", Encoding: "raw", Pattern: []byte("value-123")}})
	if err != nil || !slices.Contains(scanHitKeys(hits), "token/raw@memory:0") {
		t.Fatalf("offset scan = %+v, %v, want memory hit at 0", hits, err)
	}
}

func TestWaitBuildTimesOutWithTheSelectedBuildAndReason(t *testing.T) {
	for _, tc := range []struct {
		status string
		file   string
		reason string
	}{
		{status: "pending", reason: "pending"},
		{status: "in_progress", reason: "in_progress"},
		{status: "ready", file: "memfile", reason: "memfile"},
		{status: "ready", file: "rootfs.ext4", reason: "rootfs.ext4"},
		{status: "ready", file: "snapfile", reason: "snapfile"},
		{status: "ready", file: "metadata.json", reason: "metadata.json"},
	} {
		t.Run(tc.status+"/"+tc.file, func(t *testing.T) {
			storage := t.TempDir()
			leakscantest.WriteBuild(t, storage, buildA, []byte("old clean memory"), []byte("old clean disk"))
			leakscantest.WriteBuild(t, storage, buildB, []byte("new memory"), []byte("new disk"))
			if tc.file != "" {
				if err := os.Truncate(filepath.Join(storage, buildB, tc.file), 0); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Unix(0, 0)
			wait := leakscan.WaitOptions{
				Timeout: 2500 * time.Millisecond,
				Now:     func() time.Time { return now },
				Sleep: func(_ context.Context, d time.Duration) error {
					now = now.Add(d)
					return nil
				},
			}
			query := func(_ context.Context, _ string) ([][]string, error) { return [][]string{{tc.status}}, nil }
			err := leakscan.WaitBuild(t.Context(), query, storage, buildB, wait)
			if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), buildB) ||
				!strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("WaitBuild = %v, want timeout for newest build with reason %s", err, tc.reason)
			}
			if got := now.Sub(time.Unix(0, 0)); got != 2500*time.Millisecond {
				t.Fatalf("waited %s, want bounded 2.5s", got)
			}
		})
	}
}

func TestWaitBuildCannotSucceedAfterItsDeadline(t *testing.T) {
	storage := t.TempDir()
	leakscantest.WriteBuild(t, storage, buildB, []byte("memory"), []byte("disk"))
	now := time.Unix(0, 0)
	query := func(_ context.Context, _ string) ([][]string, error) {
		now = now.Add(3 * time.Second)
		return [][]string{{"ready"}}, nil
	}
	err := leakscan.WaitBuild(t.Context(), query, storage, buildB, leakscan.WaitOptions{
		Timeout: time.Second,
		Now:     func() time.Time { return now },
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), buildB) {
		t.Fatalf("late ready result = %v, want selected build's deadline error", err)
	}
}

func TestWaitBuildFailsForTerminalFailureAndCancellation(t *testing.T) {
	query := func(_ context.Context, _ string) ([][]string, error) { return [][]string{{"failed"}}, nil }
	if err := leakscan.WaitBuild(t.Context(), query, t.TempDir(), buildB, leakscan.WaitOptions{}); err == nil ||
		!strings.Contains(err.Error(), buildB) || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("failed build: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := leakscan.WaitBuild(ctx, query, t.TempDir(), buildB, leakscan.WaitOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait = %v, want context canceled", err)
	}
}
