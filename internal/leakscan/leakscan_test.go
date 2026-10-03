package leakscan_test

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	mrand "math/rand"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/leakscan"
)

const (
	buildA = "11111111-1111-4111-8111-111111111111"
	buildB = "22222222-2222-4222-8222-222222222222"
	buildC = "33333333-3333-4333-8333-333333333333"
	nilID  = "00000000-0000-0000-0000-000000000000"
)

func TestNeedlesFindCommonEncodings(t *testing.T) {
	value := "token~~~ /with space+\"snowman☃<>&\"!"
	needles, err := leakscan.Needles([]leakscan.Secret{{Name: "api", Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		"raw":           []byte(value),
		"percent-query": []byte(url.QueryEscape(value)),
		"percent-path":  []byte(url.PathEscape(value)),
		"json-html":     []byte(`token~~~ /with space+\"snowman☃\u003c\u003e\u0026\"!`),
		"json":          []byte(`token~~~ /with space+\"snowman☃<>&\"!`),
		"utf-16le":      utf16le(value),
	}
	for encoding, pattern := range want {
		if !hasNeedle(needles, "api", "json", pattern) && encoding == "json-html" {
			t.Fatalf("Needles lacks %s %q in:\n%s", encoding, pattern, dumpNeedles(needles))
		}
		if encoding != "json-html" && !hasNeedle(needles, "api", encoding, pattern) {
			t.Fatalf("Needles lacks %s %q in:\n%s", encoding, pattern, dumpNeedles(needles))
		}
	}
	for _, n := range needles {
		hits, err := leakscan.Search(bytes.NewReader(append(append([]byte("prefix"), n.Pattern...), []byte("suffix")...)), []leakscan.Needle{n})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].Secret != "api" || hits[0].Encoding != n.Encoding || hits[0].Offset != 6 {
			t.Fatalf("Search(%s) = %+v, want one hit at 6", n.Encoding, hits)
		}
	}
}

func TestNeedlesFindBase64InsideLongerStringsAtEachAlignment(t *testing.T) {
	value := []byte("~~~~0123456789abcdef")
	needles, err := leakscan.Needles([]leakscan.Secret{{Name: "secret", Value: string(value)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, enc := range []struct {
		name     string
		encoding *base64.Encoding
	}{
		{"base64", base64.StdEncoding},
		{"base64url", base64.URLEncoding},
	} {
		for align := range 3 {
			prefix := bytes.Repeat([]byte{'x'}, align)
			text := enc.encoding.EncodeToString(append(append(prefix, value...), []byte("tail")...))
			want := fmt.Sprintf("%s-align-%d", enc.name, align)
			hits, err := leakscan.Search(strings.NewReader("before"+text+"after"), needles)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(hits, func(h leakscan.Hit) bool { return h.Encoding == enc.name }) {
				t.Fatalf("Search did not find %s in %q; hits: %+v", want, text, hits)
			}
		}
	}
}

func TestNeedlesRefuseShortValues(t *testing.T) {
	if _, err := leakscan.Needles([]leakscan.Secret{{Name: "tiny", Value: "short"}}); err == nil {
		t.Fatal("Needles accepted a short value")
	}
}

func TestSearchFindsMatchesAcrossEveryReadBoundary(t *testing.T) {
	needles := []leakscan.Needle{
		{Secret: "a", Encoding: "raw", Pattern: []byte("abc")},
		{Secret: "c", Encoding: "raw", Pattern: []byte("abcd")},
	}
	text := []byte("xxabcdyyabc")
	for n := 1; n <= len(text)+2; n++ {
		hits, err := leakscan.Search(&chunkReader{text: text, n: n}, needles)
		if err != nil {
			t.Fatal(err)
		}
		got := hitKeys(hits)
		want := []string{"a/raw@2", "c/raw@2", "a/raw@8"}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("chunk %d: hits = %v, want %v", n, got, want)
		}
	}
}

func TestSearchHasNoFalsePositivesOnRandomData(t *testing.T) {
	needles := []leakscan.Needle{
		{Secret: "one", Encoding: "raw", Pattern: []byte("not-in-random-one")},
		{Secret: "two", Encoding: "raw", Pattern: []byte("not-in-random-two")},
	}
	data := make([]byte, 1<<20)
	if _, err := crand.Read(data); err != nil {
		t.Fatal(err)
	}
	hits, err := leakscan.Search(bytes.NewReader(data), needles)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("Search found random hits: %+v", hits)
	}
}

func TestScanBuildReadsLogicalFilesAndRawBuildFiles(t *testing.T) {
	storage := t.TempDir()
	writeBuildFile(t, storage, buildA, "memfile", []byte("aaaaVALUE-"))
	writeBuildFile(t, storage, buildB, "memfile", []byte("SPANbbbb"))
	writeBuildFile(t, storage, buildA, "rootfs.ext4", []byte("raw disk secret is here"))
	writeBuildFile(t, storage, buildB, "rootfs.ext4", []byte("disk-VALUE-"))
	writeBuildFile(t, storage, buildC, "rootfs.ext4", []byte("SPAN-disk"))
	writeBuildFile(t, storage, buildC, "snapfile", []byte("cpu state has VALUE-SPAN too"))

	writeHeader(t, storage, buildB, "memfile.header", 3, 20,
		mapping{off: 10, len: 5, build: buildB, at: 0},
		mapping{off: 0, len: 10, build: buildA, at: 0},
	)
	writeHeader(t, storage, buildB, "rootfs.ext4.header", 3, 40,
		mapping{off: 0, len: 11, build: buildB, at: 0},
		mapping{off: 11, len: 9, build: buildC, at: 0},
		mapping{off: 25, len: 5, build: nilID, at: 0},
	)
	needles := []leakscan.Needle{{Secret: "token", Encoding: "raw", Pattern: []byte("VALUE-SPAN")}}
	hits, err := leakscan.ScanBuild(storage, buildB, needles)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"token/raw@memory:4",
		"token/raw@disk:5",
		"token/raw@" + filepath.Join(storage, buildC, "snapfile") + ":14",
	}
	got := scanHitKeys(hits)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("ScanBuild hits = %v, want %v", got, want)
	}
}

func TestScanBuildRejectsBadHeadersAndShortFiles(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, storage string){
		"cut mapping": func(t *testing.T, storage string) {
			writeBuildFile(t, storage, buildA, "memfile", []byte("abcdef"))
			writeBuildFile(t, storage, buildA, "rootfs.ext4", []byte("abcdef"))
			writeHeader(t, storage, buildA, "memfile.header", 3, 6, mapping{off: 0, len: 6, build: buildA, at: 0})
			b, err := os.ReadFile(filepath.Join(storage, buildA, "memfile.header"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(storage, buildA, "memfile.header"), b[:len(b)-1], 0o644); err != nil {
				t.Fatal(err)
			}
			writeHeader(t, storage, buildA, "rootfs.ext4.header", 3, 6, mapping{off: 0, len: 6, build: buildA, at: 0})
		},
		"version 4": func(t *testing.T, storage string) {
			writeBuildFile(t, storage, buildA, "memfile", []byte("abcdef"))
			writeBuildFile(t, storage, buildA, "rootfs.ext4", []byte("abcdef"))
			writeHeader(t, storage, buildA, "memfile.header", 4, 6, mapping{off: 0, len: 6, build: buildA, at: 0})
			writeHeader(t, storage, buildA, "rootfs.ext4.header", 3, 6, mapping{off: 0, len: 6, build: buildA, at: 0})
		},
		"mapping past file end": func(t *testing.T, storage string) {
			writeBuildFile(t, storage, buildA, "memfile", []byte("abc"))
			writeBuildFile(t, storage, buildA, "rootfs.ext4", []byte("abcdef"))
			writeHeader(t, storage, buildA, "memfile.header", 3, 6, mapping{off: 0, len: 6, build: buildA, at: 0})
			writeHeader(t, storage, buildA, "rootfs.ext4.header", 3, 6, mapping{off: 0, len: 6, build: buildA, at: 0})
		},
	} {
		t.Run(name, func(t *testing.T) {
			storage := t.TempDir()
			setup(t, storage)
			_, err := leakscan.ScanBuild(storage, buildA, []leakscan.Needle{{Secret: "x", Encoding: "raw", Pattern: []byte("abcdef")}})
			if err == nil {
				t.Fatal("ScanBuild succeeded")
			}
		})
	}
}

func TestBuildLookup(t *testing.T) {
	var gotSQL string
	query := func(_ context.Context, sql string) ([][]string, error) {
		gotSQL = sql
		if strings.Contains(sql, "snapshots") {
			return [][]string{{buildA}}, nil
		}
		return [][]string{{buildB}}, nil
	}
	if got, err := leakscan.SnapshotBuild(t.Context(), query, "sandbox_123"); err != nil || got != buildA {
		t.Fatalf("SnapshotBuild = %q, %v; want %s", got, err, buildA)
	}
	if !strings.Contains(gotSQL, "snapshots") || !strings.Contains(gotSQL, "sandbox_123") || strings.Contains(gotSQL, ";") {
		t.Fatalf("SnapshotBuild SQL = %q", gotSQL)
	}
	if got, err := leakscan.TemplateBuild(t.Context(), query, "hib-dev"); err != nil || got != buildB {
		t.Fatalf("TemplateBuild = %q, %v; want %s", got, err, buildB)
	}
	if !strings.Contains(gotSQL, "env_aliases") || !strings.Contains(gotSQL, "hib-dev") || strings.Contains(gotSQL, ";") {
		t.Fatalf("TemplateBuild SQL = %q", gotSQL)
	}
}

func TestBuildLookupRejectsBadInputAndRows(t *testing.T) {
	query := func(_ context.Context, _ string) ([][]string, error) { return nil, nil }
	for _, bad := range []string{"", "bad'; DROP", strings.Repeat("a", 129)} {
		if _, err := leakscan.SnapshotBuild(t.Context(), query, bad); err == nil {
			t.Errorf("SnapshotBuild accepted %q", bad)
		}
		if _, err := leakscan.TemplateBuild(t.Context(), query, bad); err == nil {
			t.Errorf("TemplateBuild accepted %q", bad)
		}
	}
	if _, err := leakscan.SnapshotBuild(t.Context(), query, "missing"); err == nil {
		t.Error("SnapshotBuild accepted no rows")
	}
	badRows := func(_ context.Context, _ string) ([][]string, error) { return [][]string{{"a", "b"}}, nil }
	if _, err := leakscan.TemplateBuild(t.Context(), badRows, "hib-dev"); err == nil {
		t.Error("TemplateBuild accepted a bad row")
	}
	badID := func(_ context.Context, _ string) ([][]string, error) {
		return [][]string{{"ABCDEF12-1111-4111-8111-111111111111"}}, nil
	}
	if _, err := leakscan.TemplateBuild(t.Context(), badID, "hib-dev"); err == nil {
		t.Error("TemplateBuild accepted a non-canonical build ID")
	}
}

func TestScanBuildRejectsBadBuildID(t *testing.T) {
	if _, err := leakscan.ScanBuild(t.TempDir(), "../bad", nil); err == nil {
		t.Fatal("ScanBuild accepted a bad build ID")
	}
}

func FuzzSearch(f *testing.F) {
	f.Add([]byte("abcabc"), []byte("abc"), []byte("bc"), 1)
	f.Add([]byte("aaaaa"), []byte("aa"), []byte("aaa"), 2)
	f.Add([]byte("xxabcdyyabc"), []byte("abc"), []byte("abcd"), 3)
	f.Fuzz(func(t *testing.T, haystack, needleA, needleB []byte, chunk int) {
		if len(needleA) < 2 || len(needleA) > 64 || len(needleB) < 2 || len(needleB) > 64 {
			return
		}
		chunk = 1 + int(uint(chunk)%uint(max(1, min(128, len(haystack)+len(needleA)+len(needleB)))))
		needles := []leakscan.Needle{
			{Secret: "a", Encoding: "raw", Pattern: needleA},
			{Secret: "b", Encoding: "raw", Pattern: needleB},
		}
		got, err := leakscan.Search(&chunkReader{text: slices.Clone(haystack), n: chunk}, needles)
		if err != nil {
			t.Fatal(err)
		}
		want := append(naiveSearch("a", haystack, needleA), naiveSearch("b", haystack, needleB)...)
		gotKeys := hitKeys(got)
		slices.Sort(gotKeys)
		slices.Sort(want)
		if !slices.Equal(gotKeys, want) {
			t.Fatalf("Search(chunk %d) = %v, want %v", chunk, gotKeys, want)
		}
	})
}

func BenchmarkSearch(b *testing.B) {
	data := make([]byte, 128<<20)
	r := mrand.New(mrand.NewSource(1))
	for i := range data {
		data[i] = byte(r.Intn(256))
	}
	var secrets []leakscan.Secret
	for i := range 10 {
		secrets = append(secrets, leakscan.Secret{Name: fmt.Sprintf("s%d", i), Value: fmt.Sprintf("secret-value-%02d-0123456789", i)})
	}
	needles, err := leakscan.Needles(secrets)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := leakscan.Search(bytes.NewReader(data), needles); err != nil {
			b.Fatal(err)
		}
	}
}

type chunkReader struct {
	text []byte
	n    int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.text) == 0 {
		return 0, io.EOF
	}
	n := min(len(r.text), min(r.n, len(p)))
	copy(p, r.text[:n])
	r.text = r.text[n:]
	return n, nil
}

type mapping struct {
	off, len uint64
	build    string
	at       uint64
}

func writeBuildFile(t *testing.T, storage, build, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(storage, build)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeHeader(t *testing.T, storage, build, name string, version, size uint64, maps ...mapping) {
	t.Helper()
	dir := filepath.Join(storage, build)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 64+40*len(maps))
	binary.LittleEndian.PutUint64(b[0:8], version)
	binary.LittleEndian.PutUint64(b[8:16], 4096)
	binary.LittleEndian.PutUint64(b[16:24], size)
	copyUUID(t, b[32:48], build)
	copyUUID(t, b[48:64], buildA)
	for i, m := range maps {
		p := b[64+i*40:]
		binary.LittleEndian.PutUint64(p[0:8], m.off)
		binary.LittleEndian.PutUint64(p[8:16], m.len)
		copyUUID(t, p[16:32], m.build)
		binary.LittleEndian.PutUint64(p[32:40], m.at)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyUUID(t *testing.T, dst []byte, id string) {
	t.Helper()
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil {
		t.Fatal(err)
	}
	copy(dst, raw)
}

func utf16le(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		if r > 0xffff {
			r -= 0x10000
			hi := uint16(0xd800 + (r >> 10))
			lo := uint16(0xdc00 + (r & 0x3ff))
			out = binary.LittleEndian.AppendUint16(out, hi)
			out = binary.LittleEndian.AppendUint16(out, lo)
			continue
		}
		out = binary.LittleEndian.AppendUint16(out, uint16(r))
	}
	return out
}

func hasNeedle(needles []leakscan.Needle, secret, encoding string, pattern []byte) bool {
	return slices.ContainsFunc(needles, func(n leakscan.Needle) bool {
		return n.Secret == secret && n.Encoding == encoding && bytes.Equal(n.Pattern, pattern)
	})
}

func dumpNeedles(needles []leakscan.Needle) string {
	var b strings.Builder
	for _, n := range needles {
		fmt.Fprintf(&b, "%s %s %q\n", n.Secret, n.Encoding, n.Pattern)
	}
	return b.String()
}

func hitKeys(hits []leakscan.Hit) []string {
	keys := make([]string, len(hits))
	for i, h := range hits {
		keys[i] = fmt.Sprintf("%s/%s@%d", h.Secret, h.Encoding, h.Offset)
	}
	return keys
}

func scanHitKeys(hits []leakscan.Hit) []string {
	keys := make([]string, len(hits))
	for i, h := range hits {
		keys[i] = fmt.Sprintf("%s/%s@%s:%d", h.Secret, h.Encoding, h.Where, h.Offset)
	}
	return keys
}

func naiveSearch(secret string, haystack, needle []byte) []string {
	var hits []string
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if bytes.Equal(haystack[i:i+len(needle)], needle) {
			hits = append(hits, fmt.Sprintf("%s/raw@%d", secret, i))
		}
	}
	return hits
}
