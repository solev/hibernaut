package cleanup_test

import (
	"encoding/binary"
	"maps"
	"slices"
	"testing"

	"github.com/solev/hibernaut/internal/cleanup"
)

func TestParseHeaderStats(t *testing.T) {
	header := make([]byte, 64+4*40)
	for i, value := range []uint64{3, 4096, 32768, 17} {
		binary.LittleEndian.PutUint64(header[i*8:], value)
	}
	copy(header[32:48], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	copy(header[48:64], []byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1})
	for i, length := range []uint64{4096, 8192, 16384, 4096} {
		mapping := header[64+i*40:]
		binary.LittleEndian.PutUint64(mapping, uint64(i)*4096)
		binary.LittleEndian.PutUint64(mapping[8:], length)
		binary.LittleEndian.PutUint64(mapping[32:], uint64(i)*8192)
		if i != 2 {
			copy(mapping[16:32], header[32:48])
		}
	}
	copy(header[64+40+16:], header[48:64])

	got, err := cleanup.ParseHeaderStats(header)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 3 || got.BlockSize != 4096 || got.Size != 32768 || got.Generation != 17 {
		t.Errorf("metadata = %+v, want version 3, block size 4096, size 32768, generation 17", got)
	}
	build := "01020304-0506-0708-090a-0b0c0d0e0f10"
	base := "100f0e0d-0c0b-0a09-0807-060504030201"
	if got.Build != build || got.BaseBuild != base || got.Mappings != 4 {
		t.Errorf("metadata = %+v, want build %s, base %s, 4 mappings", got, build, base)
	}
	want := map[string]uint64{build: 8192, base: 8192}
	if !maps.Equal(got.BytesByBuild, want) {
		t.Errorf("mapped bytes = %v, want %v", got.BytesByBuild, want)
	}
}

func TestParseHeaderStatsRefuses(t *testing.T) {
	header := make([]byte, 104)
	binary.LittleEndian.PutUint64(header, 3)
	version := func(v uint64) []byte {
		h := slices.Clone(header)
		binary.LittleEndian.PutUint64(h, v)
		return h
	}
	for name, h := range map[string][]byte{
		"empty":                  nil,
		"cut in metadata":        header[:63],
		"cut in a mapping":       header[:103],
		"compressed (version 4)": version(4),
		"unknown version 0":      version(0),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := cleanup.ParseHeaderStats(h)
			_, parseErr := cleanup.ParseHeader(h)
			if err == nil || parseErr == nil || err.Error() != parseErr.Error() {
				t.Errorf("stats error = %v, ParseHeader error = %v, want the same refusal", err, parseErr)
			}
		})
	}
}

func TestParseHeaderStatsAcceptsVersionFlags(t *testing.T) {
	for _, version := range []uint64{1, 2, 3} {
		header := make([]byte, 64)
		binary.LittleEndian.PutUint64(header, version|1<<32)
		got, err := cleanup.ParseHeaderStats(header)
		if err != nil {
			t.Fatal(err)
		}
		if got.Version != version || got.Mappings != 0 || len(got.BytesByBuild) != 0 {
			t.Errorf("stats = %+v, want version %d and no mappings", got, version)
		}
	}

}

func TestParseHeaderKeepsMetadataAndZeroLengthSources(t *testing.T) {
	header := make([]byte, 64+2*40)
	binary.LittleEndian.PutUint64(header, 3)
	header[32], header[48], header[64+16] = 1, 2, 3
	want := []string{
		"01000000-0000-0000-0000-000000000000",
		"02000000-0000-0000-0000-000000000000",
		"03000000-0000-0000-0000-000000000000",
	}
	got, err := cleanup.ParseHeader(header)
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("ParseHeader = %v, %v, want own, base and zero-length source UUIDs", got, err)
	}
	stats, err := cleanup.ParseHeaderStats(header)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Mappings != 2 || !maps.Equal(stats.BytesByBuild, map[string]uint64{want[2]: 0}) {
		t.Errorf("stats = %+v, want two mappings and only the non-nil source", stats)
	}
}
