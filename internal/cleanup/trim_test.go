package cleanup_test

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/snapheader"
)

func TestTrimPlanKindsUnreferencedFilesAndRounding(t *testing.T) {
	files := []cleanup.TrimFile{
		{Key: cleanup.FileKey{orphanA, "memfile"}, State: cleanup.FileState{Size: 32769}},
		{Key: cleanup.FileKey{orphanA, "rootfs.ext4"}, State: cleanup.FileState{Size: 32769}},
		{Key: cleanup.FileKey{orphanB, "memfile"}, State: cleanup.FileState{Size: 32769}},
	}
	headers := []cleanup.RootHeader{{Kind: "memfile", Header: snapheader.Header{Mappings: []snapheader.Mapping{
		{Build: orphanA, StorageOffset: 4096, Length: 8192},
	}}}}
	p, err := cleanup.PlanTrim(headers, files, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p[0].Holes, []cleanup.Range{{Start: 16384, End: 32768}}) || p[0].Kept != 8192 {
		t.Fatalf("rounded memory = %+v", p[0])
	}
	for _, f := range p[1:] {
		if f.Kept != 0 || !slices.Equal(f.Holes, []cleanup.Range{{Start: 0, End: 32768}}) {
			t.Fatalf("unmapped file = %+v", f)
		}
	}
	for _, block := range []int64{0, -4096} {
		if _, err := cleanup.PlanTrim(nil, files, block); err == nil {
			t.Fatal("accepted an invalid filesystem block")
		}
	}
}

type trimStore struct {
	dirs     map[string]bool
	headers  map[cleanup.FileKey][]byte
	files    map[cleanup.FileKey]cleanup.FileState
	applied  []cleanup.FilePlan
	apply    func(context.Context, cleanup.FilePlan, bool) (cleanup.TrimResult, error)
	blockErr error
}

func (s *trimStore) Directory(id string) error {
	if !s.dirs[id] {
		return os.ErrNotExist
	}
	return nil
}

func (s *trimStore) Header(k cleanup.FileKey) ([]byte, error) {
	if b, ok := s.headers[k]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}

func (s *trimStore) Stat(k cleanup.FileKey) (cleanup.FileState, error) {
	if f, ok := s.files[k]; ok {
		return f, nil
	}
	return cleanup.FileState{}, os.ErrNotExist
}

func (s *trimStore) BlockSize() (int64, error) { return 4096, s.blockErr }
func (s *trimStore) Close() error              { return nil }
func (s *trimStore) Apply(ctx context.Context, p cleanup.FilePlan, dry bool) (cleanup.TrimResult, error) {
	s.applied = append(s.applied, p)
	if s.apply != nil {
		return s.apply(ctx, p, dry)
	}
	var freed int64
	for _, r := range p.Holes {
		freed += r.End - r.Start
	}
	return cleanup.TrimResult{Freed: freed}, nil
}

func trimHeader(id, base string, ms ...snapheader.Mapping) []byte {
	b := make([]byte, 64+40*len(ms))
	binary.LittleEndian.PutUint64(b, 3)
	binary.LittleEndian.PutUint64(b[8:], 2<<20)
	var size uint64
	for _, m := range ms {
		size += m.Length
	}
	binary.LittleEndian.PutUint64(b[16:], size)
	put := func(dst []byte, id string) {
		p, _ := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
		copy(dst, p)
	}
	put(b[32:48], id)
	put(b[48:64], base)
	for i, m := range ms {
		p := b[64+i*40:]
		binary.LittleEndian.PutUint64(p, m.Offset)
		binary.LittleEndian.PutUint64(p[8:], m.Length)
		put(p[16:32], m.Build)
		binary.LittleEndian.PutUint64(p[32:], m.StorageOffset)
	}
	return b
}

func trimFixture() (cleanup.Listing, *trimStore) {
	l := cleanup.Listing{
		Envs:     []cleanup.Env{{ID: "web", Machine: "web"}},
		Machines: []cleanup.Machine{{ID: "web", Started: ago(4 * time.Hour), Paused: true, Metadata: map[string]string{"hib.name": "e2e-web"}}},
		Builds:   []cleanup.Build{ready(orphanA, "web", ago(3*time.Hour)), ready(imageBuild, "web", ago(2*time.Hour))},
		Dirs:     []cleanup.Dir{dir(orphanA, ago(3*time.Hour)), dir(imageBuild, ago(2*time.Hour), orphanA)},
	}
	s := &trimStore{dirs: map[string]bool{orphanA: true, imageBuild: true}, headers: map[cleanup.FileKey][]byte{}, files: map[cleanup.FileKey]cleanup.FileState{}}
	for _, kind := range []string{"memfile", "rootfs.ext4"} {
		s.headers[cleanup.FileKey{imageBuild, kind + ".header"}] = trimHeader(imageBuild, cachedBuild,
			snapheader.Mapping{Build: orphanA, StorageOffset: 1 << 20, Length: 1 << 20})
		s.headers[cleanup.FileKey{orphanA, kind + ".header"}] = trimHeader(orphanA, cachedBuild)
		s.files[cleanup.FileKey{orphanA, kind}] = cleanup.FileState{Size: 4 << 20, Regular: true, Links: 1}
	}
	return l, s
}

func runTrim(t *testing.T, l cleanup.Listing, s *trimStore, scope string, dry bool) (lines, error) {
	t.Helper()
	p, err := cleanup.Garbage(l, now)
	if err != nil {
		t.Fatal(err)
	}
	var log lines
	err = (cleanup.Trimmer{Storage: s, Scope: scope}).Trim(t.Context(), l, p, now, dry, log.logf)
	return log, err
}

func TestTrimsOldPausesAndReportsExactOutput(t *testing.T) {
	for _, dry := range []bool{false, true} {
		l, s := trimFixture()
		log, err := runTrim(t, l, s, "", dry)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.applied) != 2 {
			t.Fatalf("applied = %v, log %v", s.applied, log)
		}
		want := []string{"trimmed " + orphanA + " memfile: freed 3 MiB", "trimmed " + orphanA + " rootfs.ext4: freed 3 MiB", "trimmed 6 MiB in 2 files"}
		if dry {
			want = []string{"would trim " + orphanA + " memfile: keeps 1 MiB of 4 MiB, frees about 3 MiB", "would trim " + orphanA + " rootfs.ext4: keeps 1 MiB of 4 MiB, frees about 3 MiB", "would trim about 6 MiB in 2 files"}
		}
		if !slices.Equal([]string(log), want) {
			t.Fatalf("log = %v, want %v", log, want)
		}
	}
}

func TestTrimRootAccountingFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		breakStore func(cleanup.Listing, *trimStore) cleanup.Listing
		want       string
	}{
		{"missing cached root directory", func(l cleanup.Listing, _ *trimStore) cleanup.Listing {
			l.Cached = append(l.Cached, freshBuild)
			return l
		}, freshBuild + " directory"},
		{"missing root directory", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			l.Dirs = l.Dirs[:1]
			delete(s.dirs, imageBuild)
			return l
		}, imageBuild + " directory"},
		{"missing memory header", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			delete(s.headers, cleanup.FileKey{imageBuild, "memfile.header"})
			return l
		}, imageBuild + " memfile.header"},
		{"filesystem only", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			delete(s.headers, cleanup.FileKey{imageBuild, "memfile.header"})
			return l
		}, imageBuild + " memfile.header"},
		{"headerless legacy", func(l cleanup.Listing, s *trimStore) cleanup.Listing { clear(s.headers); return l }, imageBuild + " memfile.header"},
		{"invalid root header", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			s.headers[cleanup.FileKey{imageBuild, "memfile.header"}] = []byte("bad")
			return l
		}, imageBuild + " memfile.header"},
		{"missing source directory", func(l cleanup.Listing, s *trimStore) cleanup.Listing { delete(s.dirs, orphanA); return l }, orphanA + " directory"},
		{"missing source file", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			delete(s.files, cleanup.FileKey{orphanA, "memfile"})
			return l
		}, orphanA + " memfile"},
		{"nonregular source", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			s.files[cleanup.FileKey{orphanA, "memfile"}] = cleanup.FileState{Size: 4 << 20}
			return l
		}, orphanA + " memfile"},
		{"short source", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			s.files[cleanup.FileKey{orphanA, "memfile"}] = cleanup.FileState{Size: 1 << 20, Regular: true}
			return l
		}, orphanA + " memfile"},
		{"invalid source header", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			s.headers[cleanup.FileKey{orphanA, "memfile.header"}] = []byte("bad")
			return l
		}, orphanA + " memfile.header"},
		{"not ext4", func(l cleanup.Listing, s *trimStore) cleanup.Listing {
			s.blockErr = fmt.Errorf("storage isn't on ext4")
			return l
		}, "ext4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, s := trimFixture()
			l = tc.breakStore(l, s)
			log, err := runTrim(t, l, s, "all", false)
			if err != nil || len(s.applied) != 0 || len(log) != 1 || !strings.HasPrefix(log[0], "not trimming: ") || !log.has(tc.want) {
				t.Fatalf("trim = %v, %v, applied %v", log, err, s.applied)
			}
		})
	}
}

func TestTrimPlanUnionsStorageRangesBeforeComplement(t *testing.T) {
	key := cleanup.FileKey{Build: orphanA, Name: "memfile"}
	files := []cleanup.TrimFile{{Key: key, State: cleanup.FileState{Size: 4 << 20}}}
	headers := []cleanup.RootHeader{
		{Kind: "memfile", Header: snapheader.Header{Mappings: []snapheader.Mapping{
			{Build: orphanA, Offset: 0, StorageOffset: 4096, Length: 8192},
			{Build: snapheader.NilBuild, Offset: 8192, Length: 4096},
		}}},
		{Kind: "memfile", Header: snapheader.Header{Mappings: []snapheader.Mapping{
			{Build: orphanA, Offset: 0, StorageOffset: 8192, Length: 8192},
			{Build: orphanA, Offset: 8192, StorageOffset: 32768, Length: 4096},
		}}},
	}
	p, err := cleanup.PlanTrim(headers, files, 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := []cleanup.Range{{Start: 0, End: 4096}, {Start: 16384, End: 32768}, {Start: 36864, End: 4 << 20}}
	if len(p) != 1 || p[0].Kept != 16384 || !slices.Equal(p[0].Holes, want) {
		t.Fatalf("plan = %+v, want kept 16384, holes %v", p, want)
	}
}
