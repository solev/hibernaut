// Package leakscan searches E2B snapshot builds for real secret values.
package leakscan

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/solev/hibernaut/internal/secretencoding"
)

// Secret is a real value to search for. The value is never reported in hits.
type Secret struct {
	Name  string
	Value string
}

// Needle is one byte pattern derived from a secret value.
type Needle struct {
	Secret   string
	Encoding string
	Pattern  []byte
}

// Hit is one place where a needle was found. It does not hold the secret value.
type Hit struct {
	Secret   string
	Encoding string
	Where    string
	Offset   int64
}

// Query runs SQL in E2B's database and returns tab-separated fields as rows.
type Query func(ctx context.Context, sql string) ([][]string, error)

const (
	minSecretBytes = 8
	chunkSize      = 1 << 20

	headerMetadataSize = 64
	headerMappingSize  = 40

	nilUUID = "00000000-0000-0000-0000-000000000000"
)

// Needles returns the byte patterns leak scan searches for each secret.
func Needles(secrets []Secret) ([]Needle, error) {
	var out []Needle
	seen := map[string]bool{}
	add := func(secret, encoding string, pattern []byte) {
		if len(pattern) < minSecretBytes {
			return
		}
		key := string(pattern)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Needle{Secret: secret, Encoding: encoding, Pattern: slices.Clone(pattern)})
	}
	for _, s := range secrets {
		value := []byte(s.Value)
		if len(value) < minSecretBytes {
			return nil, fmt.Errorf("secret %s is %d bytes, too short to scan safely", s.Name, len(value))
		}
		add(s.Name, "raw", value)
		base64Needles(add, s.Name, "base64", base64.StdEncoding, value)
		base64Needles(add, s.Name, "base64url", base64.URLEncoding, value)
		if escaped := url.QueryEscape(s.Value); escaped != s.Value {
			add(s.Name, "percent-query", []byte(escaped))
		}
		if escaped := url.PathEscape(s.Value); escaped != s.Value {
			add(s.Name, "percent-path", []byte(escaped))
		}
		if b, err := json.Marshal(s.Value); err != nil {
			return nil, fmt.Errorf("json-escaping %s: %w", s.Name, err)
		} else if escaped := string(b[1 : len(b)-1]); escaped != s.Value {
			add(s.Name, "json", []byte(escaped))
		}
		var noHTML bytes.Buffer
		encoder := json.NewEncoder(&noHTML)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(s.Value); err != nil {
			return nil, fmt.Errorf("json-escaping %s: %w", s.Name, err)
		}
		escaped := strings.TrimSuffix(noHTML.String(), "\n")
		escaped = escaped[1 : len(escaped)-1]
		if escaped != s.Value {
			add(s.Name, "json", []byte(escaped))
		}
		add(s.Name, "utf-16le", utf16LE(s.Value))
	}
	return out, nil
}

func base64Needles(add func(secret, encoding string, pattern []byte), secret, name string, enc *base64.Encoding, value []byte) {
	for align := range 3 {
		add(secret, name, secretencoding.Base64Core(enc, value, align))
	}
}

func utf16LE(s string) []byte {
	words := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(words)*2)
	for _, w := range words {
		out = binary.LittleEndian.AppendUint16(out, w)
	}
	return out
}

// Search reads r and returns every needle match with its byte offset.
func Search(r io.Reader, needles []Needle) ([]Hit, error) {
	scanner, err := newScanner(needles)
	if err != nil {
		return nil, err
	}
	if len(needles) == 0 {
		return nil, nil
	}
	var hits []Hit
	if err := scanner.scan(r, func(h Hit) error {
		hits = append(hits, h)
		return nil
	}); err != nil {
		return nil, err
	}
	return hits, nil
}

type scanner struct {
	needles []Needle
	twoHead [65536]int32
	next    []int32
	maxTail int
	tail    []byte
	buf     []byte
	window  []byte
	offset  int64
}

func newScanner(needles []Needle) (*scanner, error) {
	if len(needles) == 0 {
		return &scanner{}, nil
	}
	maxLen := 0
	for _, n := range needles {
		if len(n.Pattern) < 2 {
			return nil, fmt.Errorf("needle %s/%s is %d byte, want at least 2", n.Secret, n.Encoding, len(n.Pattern))
		}
		maxLen = max(maxLen, len(n.Pattern))
	}
	if maxLen > chunkSize {
		return nil, fmt.Errorf("longest needle is %d bytes, want at most %d", maxLen, chunkSize)
	}
	s := &scanner{needles: needles, maxTail: maxLen - 1}
	s.next = make([]int32, len(needles))
	for i, n := range needles {
		key := uint16(n.Pattern[0])<<8 | uint16(n.Pattern[1])
		s.next[i] = s.twoHead[key]
		s.twoHead[key] = int32(i + 1)
	}
	return s, nil
}

func (s *scanner) reset(offset int64) {
	s.tail = nil
	s.offset = offset
}

func (s *scanner) scan(r io.Reader, emit func(Hit) error) error {
	if len(s.needles) == 0 {
		return nil
	}
	if s.buf == nil {
		s.buf = make([]byte, chunkSize)
	}
	for {
		n, err := r.Read(s.buf)
		if n > 0 {
			oldTail := len(s.tail)
			if oldTail > 0 {
				prefix := min(s.maxTail, n)
				need := oldTail + prefix
				if cap(s.window) < need {
					s.window = make([]byte, need)
				}
				window := s.window[:need]
				copy(window, s.tail)
				copy(window[oldTail:], s.buf[:prefix])
				base := s.offset - int64(oldTail)
				if err := s.scanWindow(window, base, func(start, end int) bool {
					return start < oldTail && end > oldTail
				}, emit); err != nil {
					return err
				}
			}
			if err := s.scanWindow(s.buf[:n], s.offset, func(_, _ int) bool { return true }, emit); err != nil {
				return err
			}
			if s.maxTail > 0 && n >= s.maxTail {
				s.tail = append(s.tail[:0], s.buf[n-s.maxTail:n]...)
			} else if s.maxTail > 0 {
				keep := min(s.maxTail-n, len(s.tail))
				if cap(s.window) < keep+n {
					s.window = make([]byte, keep+n)
				}
				next := s.window[:keep+n]
				copy(next, s.tail[len(s.tail)-keep:])
				copy(next[keep:], s.buf[:n])
				s.tail = append(s.tail[:0], next...)
			}
			s.offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *scanner) scanWindow(window []byte, base int64, shouldEmit func(start, end int) bool, emit func(Hit) error) error {
	for start := 0; start+1 < len(window); start++ {
		key := uint16(window[start])<<8 | uint16(window[start+1])
		for link := s.twoHead[key]; link != 0; link = s.next[link-1] {
			needle := s.needles[link-1]
			end := start + len(needle.Pattern)
			if end <= len(window) && shouldEmit(start, end) && bytes.Equal(window[start:end], needle.Pattern) {
				if err := emit(Hit{Secret: needle.Secret, Encoding: needle.Encoding, Offset: base + int64(start)}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ScanBuild scans a snapshot build's logical memory, logical disk and raw artifact files.
//
// Guest memory is scanned in guest-physical order. If a value is split across guest pages that
// are not adjacent in physical memory, the scan can miss it. The canary check covers the common
// case.
func ScanBuild(storageDir, buildID string, needles []Needle) ([]Hit, error) {
	if !buildIDRE.MatchString(buildID) {
		return nil, fmt.Errorf("bad build ID %q", buildID)
	}
	if len(needles) == 0 {
		return nil, nil
	}
	scanner, err := newScanner(needles)
	if err != nil {
		return nil, err
	}
	var hits []Hit
	seenHits := map[Hit]bool{}
	emit := func(where string, h Hit) {
		h.Where = where
		if !seenHits[h] {
			seenHits[h] = true
			hits = append(hits, h)
		}
	}
	refs := map[string]bool{buildID: true}
	for _, spec := range []struct {
		header, data, where string
	}{
		{"memfile.header", "memfile", "memory"},
		{"rootfs.ext4.header", "rootfs.ext4", "disk"},
	} {
		headerPath := filepath.Join(storageDir, buildID, spec.header)
		b, err := os.ReadFile(headerPath)
		if err != nil {
			return nil, err
		}
		h, err := parseHeader(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", headerPath, err)
		}
		for _, ref := range h.refs {
			refs[ref] = true
		}
		if err := scanLogical(scanner, storageDir, spec.data, spec.where, h, emit); err != nil {
			return nil, err
		}
	}
	for _, ref := range slices.Sorted(maps.Keys(refs)) {
		dir := filepath.Join(storageDir, ref)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			scanner.reset(0)
			err = scanner.scan(f, func(h Hit) error {
				emit(path, h)
				return nil
			})
			cerr := f.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if cerr != nil {
				return nil, fmt.Errorf("%s: %w", path, cerr)
			}
		}
	}
	return hits, nil
}

func scanLogical(scanner *scanner, storageDir, dataName, where string, h header, emit func(string, Hit)) error {
	pos := uint64(0)
	scanner.reset(0)
	for _, m := range h.maps {
		if m.offset > h.size {
			return fmt.Errorf("%s mapping starts past logical size", where)
		}
		if m.offset > pos {
			// Holes are synthetic zeroes, not stored secret bytes. Resetting avoids reading GiB
			// of empty ranges and keeps matches from bridging data through a hole.
			scanner.reset(int64(m.offset))
			pos = m.offset
		}
		if m.offset < pos {
			return fmt.Errorf("%s mappings overlap at offset %d", where, m.offset)
		}
		end := m.offset + m.length
		if end > h.size || end < m.offset {
			return fmt.Errorf("%s mapping at offset %d is past logical size", where, m.offset)
		}
		if m.build == nilUUID {
			scanner.reset(int64(end))
			pos = end
			continue
		}
		path := filepath.Join(storageDir, m.build, dataName)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		if err := checkRange(f, m.storageOffset, m.length); err != nil {
			f.Close()
			return fmt.Errorf("%s: %w", path, err)
		}
		scanner.offset = int64(m.offset)
		err = scanner.scan(io.NewSectionReader(f, int64(m.storageOffset), int64(m.length)), func(h Hit) error {
			emit(where, h)
			return nil
		})
		cerr := f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if cerr != nil {
			return fmt.Errorf("%s: %w", path, cerr)
		}
		pos = end
	}
	return nil
}

func checkRange(f *os.File, offset, length uint64) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := uint64(info.Size())
	if offset > size || length > size-offset {
		return fmt.Errorf("mapping reads [%d,%d), but file is %d bytes", offset, offset+length, size)
	}
	return nil
}

type header struct {
	size uint64
	maps []mapping
	refs []string
}

type mapping struct {
	offset        uint64
	length        uint64
	build         string
	storageOffset uint64
}

func parseHeader(b []byte) (header, error) {
	if len(b) < headerMetadataSize {
		return header{}, fmt.Errorf("header is %d bytes, too short", len(b))
	}
	version := binary.LittleEndian.Uint64(b[0:8]) & 0xffff
	if version >= 4 {
		return header{}, fmt.Errorf("header version %d uses compressed data, which leakscan cannot read", version)
	}
	if version < 1 {
		return header{}, fmt.Errorf("header version %d is not supported", version)
	}
	if (len(b)-headerMetadataSize)%headerMappingSize != 0 {
		return header{}, fmt.Errorf("header is %d bytes: its mappings are cut short", len(b))
	}
	h := header{size: binary.LittleEndian.Uint64(b[16:24])}
	refs := map[string]bool{}
	for _, id := range []string{uuid(b[32:48]), uuid(b[48:64])} {
		if id != nilUUID {
			refs[id] = true
		}
	}
	for p := b[headerMetadataSize:]; len(p) > 0; p = p[headerMappingSize:] {
		m := mapping{
			offset:        binary.LittleEndian.Uint64(p[0:8]),
			length:        binary.LittleEndian.Uint64(p[8:16]),
			build:         uuid(p[16:32]),
			storageOffset: binary.LittleEndian.Uint64(p[32:40]),
		}
		if m.build != nilUUID {
			refs[m.build] = true
		}
		h.maps = append(h.maps, m)
	}
	slices.SortFunc(h.maps, func(a, b mapping) int { return cmp.Compare(a.offset, b.offset) })
	h.refs = slices.Sorted(maps.Keys(refs))
	return h, nil
}

func uuid(b []byte) string {
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

var tokenRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var buildIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// SnapshotBuild returns the latest ready snapshot build for sandboxID.
func SnapshotBuild(ctx context.Context, query Query, sandboxID string) (string, error) {
	if !tokenRE.MatchString(sandboxID) {
		return "", fmt.Errorf("bad sandbox ID %q", sandboxID)
	}
	sql := `SELECT b.id
FROM snapshots s
JOIN env_build_assignments a ON a.env_id = s.env_id
JOIN env_builds b ON b.id = a.build_id
WHERE s.sandbox_id = '` + sandboxID + `' AND b.status_group = 'ready'
ORDER BY coalesce(b.finished_at, b.created_at) DESC, a.created_at DESC
LIMIT 1`
	return oneBuild(ctx, query, sql)
}

// TemplateBuild returns the latest finished build of template name.
func TemplateBuild(ctx context.Context, query Query, name string) (string, error) {
	if !tokenRE.MatchString(name) {
		return "", fmt.Errorf("bad template name %q", name)
	}
	sql := `SELECT b.id
FROM env_aliases al
JOIN env_build_assignments a ON a.env_id = al.env_id
JOIN env_builds b ON b.id = a.build_id
WHERE al.alias = '` + name + `' AND b.status_group = 'ready'
ORDER BY coalesce(b.finished_at, b.created_at) DESC, a.created_at DESC
LIMIT 1`
	return oneBuild(ctx, query, sql)
}

func oneBuild(ctx context.Context, query Query, sql string) (string, error) {
	rows, err := query(ctx, sql)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", errors.New("no build found")
	}
	if len(rows) != 1 || len(rows[0]) != 1 || !buildIDRE.MatchString(rows[0][0]) {
		return "", fmt.Errorf("got rows %q, want one build ID", rows)
	}
	return rows[0][0], nil
}
