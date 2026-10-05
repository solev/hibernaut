//go:build host

package cleanup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/solev/hibernaut/internal/snapheader"
)

var hostBuilds = []string{
	"11111111-1111-4111-8111-111111111111",
	"22222222-2222-4222-8222-222222222222",
	"33333333-3333-4333-8333-333333333333",
	"44444444-4444-4444-8444-444444444444",
	"55555555-5555-4555-8555-555555555555",
	"66666666-6666-4666-8666-666666666666",
}

type hostFixture struct {
	path    string
	storage TrimStorage
	listing Listing
	plan    Plan
	now     time.Time
}

func hostDir(t *testing.T, parent string) (string, TrimStorage) {
	t.Helper()
	path, err := os.MkdirTemp(parent, "hibernaut-trim-")
	if err != nil {
		t.Skipf("snapshot test storage unavailable: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(path) })
	storage, err := OpenTrimStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	if _, err := storage.BlockSize(); err != nil {
		t.Skipf("snapshot test needs ext4: %v", err)
	}
	return path, storage
}

func hostHeader(id, base string, size uint64, mappings []snapheader.Mapping) []byte {
	b := make([]byte, 64+40*len(mappings))
	binary.LittleEndian.PutUint64(b, 3)
	binary.LittleEndian.PutUint64(b[8:], 2<<20)
	binary.LittleEndian.PutUint64(b[16:], size)
	binary.LittleEndian.PutUint64(b[24:], 3)
	put := func(dst []byte, id string) {
		raw, _ := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
		copy(dst, raw)
	}
	put(b[32:48], id)
	put(b[48:64], base)
	for i, m := range mappings {
		p := b[64+i*40:]
		binary.LittleEndian.PutUint64(p, m.Offset)
		binary.LittleEndian.PutUint64(p[8:], m.Length)
		put(p[16:32], m.Build)
		binary.LittleEndian.PutUint64(p[32:], m.StorageOffset)
	}
	return b
}

func writeAllocatedBody(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
}

func newHostFixture(t *testing.T) hostFixture {
	t.Helper()
	path, storage := hostDir(t, "/var/tmp")
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	l := Listing{Envs: []Env{{ID: "image"}, {ID: "a", Machine: "a"}, {ID: "b", Machine: "b"}, {ID: "c", Machine: "c"}}}
	slots := []string{"image", "a", "a", "a", "b", "c"}
	for i, id := range hostBuilds {
		at := now.Add(-time.Duration(8-i) * time.Hour)
		d := Dir{Build: id, Modified: at}
		l.Builds = append(l.Builds, Build{ID: id, Status: "ready", Created: at, Finished: at.Add(time.Minute), In: []Assignment{{Env: slots[i], Tag: "default", At: at}}})
		dir := filepath.Join(path, id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"memfile", "rootfs.ext4"} {
			size := uint64(8 << 20)
			var mappings []snapheader.Mapping
			switch i {
			case 2:
				size = 16384
				mappings = []snapheader.Mapping{
					{Build: hostBuilds[1], Length: 8192, StorageOffset: 8192},
					{Offset: 8192, Build: id, Length: 8192, StorageOffset: 4096},
				}
			case 3:
				size = 20480
				mappings = []snapheader.Mapping{
					{Build: hostBuilds[1], Length: 4096, StorageOffset: 0},
					{Offset: 4096, Build: hostBuilds[1], Length: 4096, StorageOffset: 16384},
					{Offset: 8192, Build: hostBuilds[2], Length: 4096, StorageOffset: 512 << 10},
					{Offset: 12288, Build: id, Length: 4096, StorageOffset: 0},
					{Offset: 16384, Build: snapheader.NilBuild, Length: 4096},
				}
			case 4:
				size = 12288
				mappings = []snapheader.Mapping{
					{Build: hostBuilds[1], Length: 4096, StorageOffset: 16384},
					{Offset: 4096, Build: hostBuilds[2], Length: 4096, StorageOffset: 1 << 20},
					{Offset: 8192, Build: id, Length: 4096, StorageOffset: 0},
				}
			case 5:
				size = 8192
				mappings = []snapheader.Mapping{
					{Build: hostBuilds[2], Length: 8192, StorageOffset: 2 << 20},
				}
			}
			if kind == "rootfs.ext4" && i >= 3 {
				for j := range mappings {
					if mappings[j].Build == hostBuilds[1] {
						mappings[j].StorageOffset += 2 << 20
					}
				}
			}
			header := hostHeader(id, hostBuilds[0], size, mappings)
			refs, err := ParseHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			d.Refs = append(d.Refs, refs...)
			if err := os.WriteFile(filepath.Join(dir, kind+".header"), header, 0o644); err != nil {
				t.Fatal(err)
			}
			if i != 5 {
				data := make([]byte, 8<<20)
				if _, err := rand.Read(data); err != nil {
					t.Fatal(err)
				}
				writeAllocatedBody(t, filepath.Join(dir, kind), data)
			}
		}
		for _, name := range []string{"snapfile", "metadata.json"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("snapshot metadata"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := os.Chtimes(filepath.Join(dir, e.Name()), at.Add(-time.Hour), at); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
		l.Dirs = append(l.Dirs, d)
	}
	for i, machine := range []string{"a", "b", "c"} {
		l.Machines = append(l.Machines, Machine{ID: machine, Started: now.Add(-time.Duration(6-i) * time.Hour), Paused: true, Metadata: map[string]string{"hib.name": "e2e-" + machine}})
	}
	plan, err := Garbage(l, now)
	if err != nil {
		t.Fatal(err)
	}
	return hostFixture{path: path, storage: storage, listing: l, plan: plan, now: now}
}

func readNoAtime(t *testing.T, path string) *os.File {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NOATIME|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	return os.NewFile(uintptr(fd), path)
}

func rootHashes(t *testing.T, f hostFixture) map[FileKey][32]byte {
	t.Helper()
	hashes := map[FileKey][32]byte{}
	for id := range f.plan.Roots {
		for _, kind := range []string{"memfile", "rootfs.ext4"} {
			b, err := f.storage.Header(FileKey{id, kind + ".header"})
			if err != nil {
				t.Fatal(err)
			}
			h, err := snapheader.Parse(b, id)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.New()
			for _, m := range h.Mappings {
				if m.Build == snapheader.NilBuild {
					if _, err := io.CopyN(sum, zeroReader{}, int64(m.Length)); err != nil {
						t.Fatal(err)
					}
					continue
				}
				src := readNoAtime(t, filepath.Join(f.path, m.Build, kind))
				_, err := io.Copy(sum, io.NewSectionReader(src, int64(m.StorageOffset), int64(m.Length)))
				src.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			hashes[FileKey{id, kind}] = [32]byte(sum.Sum(nil))
		}
	}
	return hashes
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func bodyStats(t *testing.T, f hostFixture) map[FileKey]unix.Stat_t {
	t.Helper()
	out := map[FileKey]unix.Stat_t{}
	for _, id := range hostBuilds {
		for _, kind := range []string{"memfile", "rootfs.ext4"} {
			var st unix.Stat_t
			err := unix.Stat(filepath.Join(f.path, id, kind), &st)
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			out[FileKey{id, kind}] = st
		}
	}
	return out
}

func sidecarHashes(t *testing.T, f hostFixture) map[FileKey][32]byte {
	t.Helper()
	out := map[FileKey][32]byte{}
	for _, id := range hostBuilds {
		for _, name := range []string{"memfile.header", "rootfs.ext4.header", "snapfile", "metadata.json"} {
			src := readNoAtime(t, filepath.Join(f.path, id, name))
			b, err := io.ReadAll(src)
			src.Close()
			if err != nil {
				t.Fatal(err)
			}
			out[FileKey{id, name}] = sha256.Sum256(b)
		}
	}
	return out
}

func hostTrim(t *testing.T, f hostFixture, ctx context.Context, dry bool) ([]string, error) {
	t.Helper()
	var log []string
	err := (Trimmer{Storage: f.storage, Scope: "all"}).Trim(ctx, f.listing, f.plan, f.now, dry, func(format string, args ...any) {
		log = append(log, fmt.Sprintf(format, args...))
	})
	return log, err
}

func TestHostTrimPreservesEveryRootAndFileIdentity(t *testing.T) {
	f := newHostFixture(t)
	hashes, sidecars, before := rootHashes(t, f), sidecarHashes(t, f), bodyStats(t, f)
	sidecarTimes := map[FileKey]unix.Stat_t{}
	for key := range sidecars {
		var st unix.Stat_t
		if err := unix.Stat(filepath.Join(f.path, key.Build, key.Name), &st); err != nil {
			t.Fatal(err)
		}
		sidecarTimes[key] = st
	}
	log, err := hostTrim(t, f, t.Context(), true)
	if err != nil || !slices.ContainsFunc(log, func(s string) bool { return strings.HasPrefix(s, "would trim about ") }) {
		t.Fatalf("dry run: %v, %v", log, err)
	}
	dryStats := bodyStats(t, f)
	for key, st := range before {
		if dryStats[key].Blocks != st.Blocks || dryStats[key].Mtim != st.Mtim || dryStats[key].Atim != st.Atim {
			t.Fatalf("dry run changed %v", key)
		}
	}
	log, err = hostTrim(t, f, t.Context(), false)
	if err != nil || !slices.ContainsFunc(log, func(s string) bool { return strings.HasPrefix(s, "trimmed ") }) {
		t.Fatalf("real trim: %v, %v", log, err)
	}
	after := bodyStats(t, f)
	var freed int64
	for key, st := range before {
		got := after[key]
		if got.Size != st.Size || got.Mtim != st.Mtim || got.Atim != st.Atim || got.Ino != st.Ino {
			t.Errorf("%v changed size, times or inode: before %+v, after %+v", key, st, got)
		}
		if _, root := f.plan.Roots[key.Build]; root && got.Blocks != st.Blocks {
			t.Errorf("root %v lost blocks", key)
		}
		freed += (st.Blocks - got.Blocks) * 512
	}
	if freed < 20<<20 {
		t.Fatalf("freed %d bytes, want unread old data", freed)
	}
	for key, sum := range rootHashes(t, f) {
		if sum != hashes[key] {
			t.Errorf("root %v reads different bytes", key)
		}
	}
	for key, sum := range sidecarHashes(t, f) {
		if sum != sidecars[key] {
			t.Errorf("sidecar %v changed", key)
		}
		var got unix.Stat_t
		if err := unix.Stat(filepath.Join(f.path, key.Build, key.Name), &got); err != nil {
			t.Fatal(err)
		}
		if st := sidecarTimes[key]; got.Size != st.Size || got.Mtim != st.Mtim || got.Atim != st.Atim {
			t.Errorf("sidecar %v changed size or times", key)
		}
	}
	log, err = hostTrim(t, f, t.Context(), false)
	if err != nil || !slices.Equal(log, []string{"nothing to trim"}) {
		t.Fatalf("second trim: %v, %v", log, err)
	}
}

func TestHostTrimLeavesReplacedResizedTouchedLinkedAndSymlinkedFiles(t *testing.T) {
	for _, change := range []string{"replace", "resize", "touch", "link", "symlink"} {
		t.Run(change, func(t *testing.T) {
			f := newHostFixture(t)
			key := FileKey{hostBuilds[1], "memfile"}
			st, err := f.storage.Stat(key)
			if err != nil {
				t.Fatal(err)
			}
			plan := FilePlan{File: TrimFile{Key: key, State: st}, Holes: []Range{{0, st.Size}}}
			path := filepath.Join(f.path, key.Build, key.Name)
			switch change {
			case "replace", "symlink":
				other := path + ".other"
				writeAllocatedBody(t, other, make([]byte, st.Size))
				if err := os.Chtimes(other, st.Modified, st.Modified); err != nil {
					t.Fatal(err)
				}
				if change == "replace" {
					err = os.Rename(other, path)
				} else {
					if err = os.Remove(path); err == nil {
						err = os.Symlink(other, path)
					}
				}
			case "resize":
				err = os.Truncate(path, st.Size+4096)
			case "touch":
				err = os.Chtimes(path, f.now, f.now)
			case "link":
				err = os.Link(path, path+".link")
			}
			if err != nil {
				t.Fatal(err)
			}
			before := bodyStats(t, f)[key]
			result, err := f.storage.Apply(t.Context(), plan, false)
			if err != nil || result.Freed != 0 || result.Left == "" {
				t.Fatalf("changed file = %+v, %v", result, err)
			}
			if after := bodyStats(t, f)[key]; after.Blocks != before.Blocks || after.Size != before.Size || after.Mtim != before.Mtim {
				t.Fatal("changed file was punched")
			}
			switch change {
			case "replace":
				if !strings.Contains(result.Left, "replaced") {
					t.Fatal(result)
				}
			case "resize":
				if !strings.Contains(result.Left, "size changed") {
					t.Fatal(result)
				}
			case "touch":
				if !strings.Contains(result.Left, "mtime changed") {
					t.Fatal(result)
				}
			case "link":
				if !strings.Contains(result.Left, "link") {
					t.Fatal(result)
				}
			}
		})
	}
}

func TestHostTrimCancellationRestoresMtimeAndFinishesNextRun(t *testing.T) {
	f := newHostFixture(t)
	hashes, before := rootHashes(t, f), bodyStats(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := f.storage.(*localTrimStorage)
	punch := store.punch
	punches := 0
	store.punch = func(fd int, start, length int64) error {
		err := punch(fd, start, length)
		if err == nil {
			punches++
			cancel()
		}
		return err
	}
	log, err := hostTrim(t, f, ctx, false)
	if !errors.Is(err, context.Canceled) || punches != 1 || !slices.ContainsFunc(log, func(s string) bool { return strings.HasPrefix(s, "trimmed ") }) {
		t.Fatalf("cancelled run: %v, %v, punches %d", log, err, punches)
	}
	for key, got := range bodyStats(t, f) {
		if got.Mtim != before[key].Mtim || got.Atim != before[key].Atim {
			t.Errorf("%v times not restored after cancellation", key)
		}
	}
	for key, sum := range rootHashes(t, f) {
		if sum != hashes[key] {
			t.Errorf("cancelled run damaged %v", key)
		}
	}
	store.punch = punch
	log, err = hostTrim(t, f, t.Context(), false)
	if err != nil || !slices.ContainsFunc(log, func(s string) bool { return strings.HasPrefix(s, "trimmed ") }) {
		t.Fatalf("continuation: %v, %v", log, err)
	}
	for key, sum := range rootHashes(t, f) {
		if sum != hashes[key] {
			t.Errorf("continuation damaged %v", key)
		}
	}
	log, err = hostTrim(t, f, t.Context(), false)
	if err != nil || !slices.Equal(log, []string{"nothing to trim"}) {
		t.Fatalf("finished trim: %v, %v", log, err)
	}
}

func TestHostTrimRefusesNonExt4(t *testing.T) {
	path, err := os.MkdirTemp("/dev/shm", "hibernaut-trim-")
	if err != nil {
		t.Skipf("non-ext4 test storage unavailable: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(path) })
	storage, err := OpenTrimStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if _, err := storage.BlockSize(); err == nil {
		t.Skip("non-ext4 test storage is ext4")
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	source, root := hostBuilds[1], hostBuilds[3]
	l := Listing{Envs: []Env{{ID: "slot", Machine: "web"}}}
	for i, id := range []string{source, root} {
		at := now.Add(-time.Duration(5-i) * time.Hour)
		dir := filepath.Join(path, id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		l.Builds = append(l.Builds, Build{ID: id, Status: "ready", Created: at, Finished: at, In: []Assignment{{Env: "slot", Tag: "default", At: at}}})
		d := Dir{Build: id, Modified: at}
		for _, kind := range []string{"memfile", "rootfs.ext4"} {
			size := uint64(1 << 20)
			var mappings []snapheader.Mapping
			if id == root {
				size = 4096
				mappings = []snapheader.Mapping{{Build: source, Length: size}}
				d.Refs = append(d.Refs, source)
			} else {
				data := make([]byte, size)
				if _, err := rand.Read(data); err != nil {
					t.Fatal(err)
				}
				writeAllocatedBody(t, filepath.Join(dir, kind), data)
			}
			if err := os.WriteFile(filepath.Join(dir, kind+".header"), hostHeader(id, hostBuilds[0], size, mappings), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := os.Chtimes(filepath.Join(dir, e.Name()), at, at); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
		l.Dirs = append(l.Dirs, d)
	}
	plan, err := Garbage(l, now)
	if err != nil {
		t.Fatal(err)
	}
	before := bodyStats(t, hostFixture{path: path})
	var log []string
	err = (Trimmer{Storage: storage, Scope: "all"}).Trim(t.Context(), l, plan, now, false, func(format string, args ...any) {
		log = append(log, fmt.Sprintf(format, args...))
	})
	if err != nil || len(log) != 1 || !strings.Contains(log[0], "not trimming: storage isn't on ext4") {
		t.Fatalf("non-ext4 trim: %v, %v", log, err)
	}
	for key, got := range bodyStats(t, hostFixture{path: path}) {
		if st := before[key]; got.Blocks != st.Blocks || got.Size != st.Size || got.Mtim != st.Mtim || got.Atim != st.Atim {
			t.Errorf("non-ext4 file %v changed", key)
		}
	}
}
