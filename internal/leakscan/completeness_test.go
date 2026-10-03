package leakscan_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/leakscan"
	"github.com/solev/hibernaut/internal/leakscan/leakscantest"
)

func TestScanBuildAcceptsEmptyDiffsWithoutInventingDataFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    uint64
		mapping leakscantest.Mapping
	}{
		{"zero blocks", 16, leakscantest.Mapping{Length: 16, Build: nilID}},
		{"unchanged ancestor", 13, leakscantest.Mapping{Length: 13, Build: buildA}},
		{"empty logical file", 0, leakscantest.Mapping{Build: buildB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			leakscantest.WriteBuild(t, storage, buildA, []byte("old value-123"), []byte("old value-123"))
			leakscantest.WriteBuild(t, storage, buildB, nil, nil)
			for _, name := range []string{"memfile", "rootfs.ext4"} {
				leakscantest.WriteHeader(t, storage, buildB, name+".header", tc.size, tc.mapping)
				if err := os.Remove(filepath.Join(storage, buildB, name)); err != nil {
					t.Fatal(err)
				}
			}
			leakscantest.WriteBuildFile(t, storage, buildB, "snapfile", []byte("state value-123"))
			hits, err := leakscan.ScanBuild(storage, buildB,
				[]leakscan.Needle{{Secret: "token", Encoding: "raw", Pattern: []byte("value-123")}})
			want := "token/raw@" + filepath.Join(storage, buildB, "snapfile") + ":6"
			if err != nil || !slices.Contains(scanHitKeys(hits), want) {
				t.Fatalf("empty diff scan = %+v, %v, want state hit at 6", hits, err)
			}
			if tc.name == "unchanged ancestor" && !slices.Contains(scanHitKeys(hits), "token/raw@memory:4") {
				t.Fatalf("unchanged memory did not find ancestor value: %+v", hits)
			}
		})
	}
}

func TestScanBuildAcceptsLegacyMetadataAndRejectsAnotherBuildsHeader(t *testing.T) {
	storage := t.TempDir()
	leakscantest.WriteBuild(t, storage, buildA, []byte("old value-123"), []byte("old disk"))
	leakscantest.WriteBuild(t, storage, buildB, []byte("new memory"), []byte("new disk"))
	leakscantest.WriteBuildFile(t, storage, buildA, "metadata.json", []byte(`{"version":"v1"}`))
	needles := []leakscan.Needle{{Secret: "token", Encoding: "raw", Pattern: []byte("value-123")}}
	hits, err := leakscan.ScanBuild(storage, buildA, needles)
	if err != nil || !slices.Contains(scanHitKeys(hits), "token/raw@memory:4") {
		t.Fatalf("legacy metadata scan = %+v, %v, want memory hit", hits, err)
	}
	b, err := os.ReadFile(filepath.Join(storage, buildB, "memfile.header"))
	if err != nil {
		t.Fatal(err)
	}
	leakscantest.WriteBuildFile(t, storage, buildA, "memfile.header", b)
	if _, err := leakscan.ScanBuild(storage, buildA, needles); err == nil ||
		!strings.Contains(err.Error(), "belongs to build") {
		t.Fatalf("another build's header = %v, want header identity error", err)
	}
}

func TestScanBuildRejectsFilesystemOnlySnapshotsInsteadOfCallingThemEmptyDiffs(t *testing.T) {
	storage := t.TempDir()
	leakscantest.WriteBuild(t, storage, buildA, []byte("old value-123"), []byte("old disk"))
	needles := []leakscan.Needle{{Secret: "token", Encoding: "raw", Pattern: []byte("value-123")}}
	hits, err := leakscan.ScanBuild(storage, buildA, needles)
	if err != nil || !slices.Contains(scanHitKeys(hits), "token/raw@memory:4") {
		t.Fatalf("memory snapshot = %+v, %v, want memory hit", hits, err)
	}
	leakscantest.WriteBuildFile(t, storage, buildA, "metadata.json", []byte(`{"version":2,"filesystem_only":true}`))
	for _, name := range []string{"memfile.header", "memfile", "snapfile"} {
		if err := os.Remove(filepath.Join(storage, buildA, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := leakscan.ScanBuild(storage, buildA, needles); err == nil ||
		!strings.Contains(err.Error(), "filesystem-only snapshot cannot prove memory is clean") {
		t.Fatalf("filesystem-only snapshot = %v, want memory coverage error", err)
	}
}

func TestScanBuildRejectsIncompleteLogicalMappings(t *testing.T) {
	for _, tc := range []struct {
		name string
		maps []leakscantest.Mapping
	}{
		{"missing mapping", nil},
		{"leading gap", []leakscantest.Mapping{{Offset: 4, Length: 9, Build: buildA}}},
		{"trailing gap", []leakscantest.Mapping{{Length: 4, Build: buildA}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			leakscantest.WriteBuild(t, storage, buildA, []byte("old value-123"), []byte("old value-123"))
			needles := []leakscan.Needle{{Secret: "token", Encoding: "raw", Pattern: []byte("value-123")}}
			hits, err := leakscan.ScanBuild(storage, buildA, needles)
			if err != nil || !slices.Contains(scanHitKeys(hits), "token/raw@memory:4") {
				t.Fatalf("valid mappings scan = %+v, %v, want memory hit", hits, err)
			}
			leakscantest.WriteHeader(t, storage, buildA, "memfile.header", 13, tc.maps...)
			if _, err := leakscan.ScanBuild(storage, buildA, needles); err == nil ||
				!strings.Contains(err.Error(), "logical") {
				t.Fatalf("incomplete logical mapping = %v, want logical coverage error", err)
			}
		})
	}
}
