package snapheader_test

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/snapheader"
)

const build = "11111111-1111-4111-8111-111111111111"
const source = "22222222-2222-4222-8222-222222222222"

func wire(version, size uint64) []byte {
	b := make([]byte, 144)
	for i, v := range []uint64{version, 2 << 20, size, 7} {
		binary.LittleEndian.PutUint64(b[i*8:], v)
	}
	id, _ := hex.DecodeString(strings.ReplaceAll(build, "-", ""))
	copy(b[32:48], id)
	copy(b[48:64], id)
	for i, offset := range []uint64{4096, 0} {
		p := b[64+i*40:]
		binary.LittleEndian.PutUint64(p, offset)
		binary.LittleEndian.PutUint64(p[8:], 4096)
		id, _ := hex.DecodeString(strings.ReplaceAll(source, "-", ""))
		copy(p[16:32], id)
		binary.LittleEndian.PutUint64(p[32:], offset+8192)
	}
	return b
}

func TestParseUncompressedStorageMappings(t *testing.T) {
	for _, version := range []uint64{1, 2, 3} {
		h, err := snapheader.Parse(wire(version, 8192), build)
		if err != nil {
			t.Fatal(err)
		}
		if h.Build != build || h.BaseBuild != build || h.Size != 8192 || len(h.Mappings) != 2 {
			t.Fatalf("header = %+v", h)
		}
		if m := h.Mappings[0]; m.Build != source || m.Offset != 0 || m.StorageOffset != 8192 || m.Length != 4096 {
			t.Fatalf("first mapping = %+v", m)
		}
	}
}

func TestParseRefusesUnsafeHeaders(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   int
		word uint64
		want string
	}{
		{"version zero", 0, 0, "version"},
		{"compressed", 0, 4, "version"},
		{"flag bits", 0, 0x10003, "version"},
		{"zero block", 8, 0, "block size"},
		{"small block", 8, 2048, "block size"},
		{"nonpower block", 8, 12288, "block size"},
		{"large size", 16, math.MaxUint64, "size"},
		{"gap", 64, 8192, "gap"},
		{"overlap", 64, 0, "overlap"},
		{"overflow", 72, math.MaxUint64 - 4095, "overflow"},
		{"past size", 72, 8192, "size"},
		{"unaligned offset", 64, 4097, "aligned"},
		{"unaligned length", 72, 4097, "aligned"},
		{"unaligned storage", 96, 1, "aligned"},
		{"storage overflow", 96, math.MaxUint64 - 4095, "storage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := wire(3, 8192)
			binary.LittleEndian.PutUint64(b[tc.at:], tc.word)
			if _, err := snapheader.Parse(b, build); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse = %v, want %q", err, tc.want)
			}
		})
	}
	for _, b := range [][]byte{nil, wire(3, 8192)[:63], wire(3, 8192)[:143]} {
		if _, err := snapheader.Parse(b, build); err == nil {
			t.Fatal("accepted a cut-short header")
		}
	}
	if _, err := snapheader.Parse(wire(3, 8192), source); err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("accepted another build's header: %v", err)
	}
	b := wire(3, 12288)
	if _, err := snapheader.Parse(b, build); err == nil || !strings.Contains(err.Error(), "cover") {
		t.Fatalf("accepted incomplete coverage: %v", err)
	}
}

func TestParseIdentityAndZeroMappings(t *testing.T) {
	h, err := snapheader.Parse(wire(3, 8192)[:64], build)
	if err != nil || len(h.Mappings) != 1 || h.Mappings[0].Build != build || h.Mappings[0].Length != 8192 {
		t.Fatalf("identity = %+v, %v", h, err)
	}
	b := wire(3, 8192)
	b = append(b, make([]byte, 40)...)
	binary.LittleEndian.PutUint64(b[144:], math.MaxUint64)
	clear(b[80:96])
	h, err = snapheader.Parse(b, build)
	if err != nil || len(h.Mappings) != 2 || h.Mappings[1].Build != snapheader.NilBuild {
		t.Fatalf("zero mappings = %+v, %v", h, err)
	}
	h, err = snapheader.Parse(wire(3, 0)[:64], build)
	if err != nil || len(h.Mappings) != 0 {
		t.Fatalf("empty device = %+v, %v", h, err)
	}
}

func FuzzParse(f *testing.F) {
	for _, b := range [][]byte{nil, wire(1, 8192), wire(2, 8192), wire(3, 8192), wire(3, 8192)[:64], wire(4, 8192)} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := snapheader.Parse(b, build)
		if err != nil {
			return
		}
		if len(b) < 64 || (len(b)-64)%40 != 0 || h.Version < 1 || h.Version > 3 ||
			h.BlockSize < 4096 || h.BlockSize&(h.BlockSize-1) != 0 || h.Size > math.MaxInt64 || h.Build != build {
			t.Fatalf("accepted invalid metadata: %+v", h)
		}
		var end uint64
		for _, m := range h.Mappings {
			if m.Offset != end || m.Length == 0 || m.Offset%4096 != 0 || m.Length%4096 != 0 ||
				m.StorageOffset%4096 != 0 || m.Length > math.MaxInt64 || m.StorageOffset > math.MaxInt64-m.Length {
				t.Fatalf("accepted invalid mapping: %+v", m)
			}
			end += m.Length
			if end > h.Size {
				t.Fatal("mapping exceeds logical size")
			}
		}
		if end != h.Size {
			t.Fatal("mappings do not cover device")
		}
	})
}
