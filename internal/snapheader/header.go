// Package snapheader reads the exact source ranges of E2B's uncompressed snapshot headers.
package snapheader

import (
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
)

// NilBuild identifies a mapping that reads zeros rather than a file.
const NilBuild = "00000000-0000-0000-0000-000000000000"

// Header describes a logical device and its source files.
type Header struct {
	Version, BlockSize, Size, Generation uint64
	Build, BaseBuild                     string
	Mappings                             []Mapping
}

// Mapping reads Length bytes at StorageOffset in Build, starting at logical Offset.
type Mapping struct {
	Offset, Length, StorageOffset uint64
	Build                         string
}

// Parse reads a header whose build must match directory, returning mappings in logical order.
func Parse(b []byte, directory string) (Header, error) {
	if len(b) < 64 {
		return Header{}, fmt.Errorf("header is %d bytes, too short", len(b))
	}
	if (len(b)-64)%40 != 0 {
		return Header{}, fmt.Errorf("header is %d bytes: its mappings are cut short", len(b))
	}
	h := Header{
		Version: binary.LittleEndian.Uint64(b), BlockSize: binary.LittleEndian.Uint64(b[8:]),
		Size: binary.LittleEndian.Uint64(b[16:]), Generation: binary.LittleEndian.Uint64(b[24:]),
		Build: uuid(b[32:48]), BaseBuild: uuid(b[48:64]),
	}
	if h.Version < 1 || h.Version > 3 {
		return Header{}, fmt.Errorf("header version word %#x isn't uncompressed version 1, 2 or 3", h.Version)
	}
	if h.BlockSize < 4096 || h.BlockSize&(h.BlockSize-1) != 0 {
		return Header{}, fmt.Errorf("block size %d isn't a power of two of at least 4096", h.BlockSize)
	}
	if h.Size > math.MaxInt64 {
		return Header{}, fmt.Errorf("size %d exceeds supported file size", h.Size)
	}
	if h.Build != directory || h.Build == NilBuild {
		return Header{}, fmt.Errorf("header build %s isn't directory %s", h.Build, directory)
	}
	for p := b[64:]; len(p) > 0; p = p[40:] {
		m := Mapping{
			Offset: binary.LittleEndian.Uint64(p), Length: binary.LittleEndian.Uint64(p[8:]),
			Build: uuid(p[16:32]), StorageOffset: binary.LittleEndian.Uint64(p[32:]),
		}
		if m.Length == 0 {
			continue
		}
		if m.Length > math.MaxInt64 || m.Offset > math.MaxInt64-m.Length {
			return Header{}, fmt.Errorf("mapping at %d overflows supported offsets", m.Offset)
		}
		if m.StorageOffset > math.MaxInt64-m.Length {
			return Header{}, fmt.Errorf("mapping storage offset %d overflows supported offsets", m.StorageOffset)
		}
		if m.Offset%4096 != 0 || m.Length%4096 != 0 || m.StorageOffset%4096 != 0 {
			return Header{}, fmt.Errorf("mapping at %d isn't aligned to 4096", m.Offset)
		}
		h.Mappings = append(h.Mappings, m)
	}
	if len(b) == 64 && h.Size > 0 {
		if h.Size%4096 != 0 {
			return Header{}, fmt.Errorf("identity mapping size %d isn't aligned to 4096", h.Size)
		}
		h.Mappings = []Mapping{{Build: h.Build, Length: h.Size}}
	}
	slices.SortFunc(h.Mappings, func(a, b Mapping) int { return cmp.Compare(a.Offset, b.Offset) })
	var end uint64
	for _, m := range h.Mappings {
		if m.Offset != end {
			return Header{}, fmt.Errorf("mapping at %d leaves a gap or overlap at %d", m.Offset, end)
		}
		end = m.Offset + m.Length
		if end > h.Size {
			return Header{}, fmt.Errorf("mapping ends at %d, past size %d", end, h.Size)
		}
	}
	if end != h.Size {
		return Header{}, fmt.Errorf("mappings cover %d bytes, want %d", end, h.Size)
	}
	return h, nil
}

func uuid(b []byte) string {
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
