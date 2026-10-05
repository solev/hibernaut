package cleanup

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/solev/hibernaut/internal/snapheader"
)

// FileKey names a body or header in a build directory.
type FileKey struct {
	Build, Name string
}

// FileState records a file's identity at planning time.
type FileState struct {
	Device, Inode, Links uint64
	Size, Blocks         int64
	Modified             time.Time
	Regular              bool
}

// TrimFile is a candidate's body and its identity.
type TrimFile struct {
	Key   FileKey
	State FileState
}

// RootHeader is one root's memory or disk header, with Kind naming its body.
type RootHeader struct {
	Kind   string
	Header snapheader.Header
}

// Range is a half-open byte range.
type Range struct {
	Start, End int64
}

// FilePlan keeps a file's mapped bytes and punches its block-aligned Holes.
type FilePlan struct {
	File  TrimFile
	Kept  int64
	Holes []Range
}

// PlanTrim unions roots' source ranges before rounding their complement inward to blockSize.
func PlanTrim(headers []RootHeader, files []TrimFile, blockSize int64) ([]FilePlan, error) {
	if blockSize <= 0 {
		return nil, fmt.Errorf("invalid filesystem block size %d", blockSize)
	}
	kept := map[FileKey][]Range{}
	for _, h := range headers {
		for _, m := range h.Header.Mappings {
			if m.Build == snapheader.NilBuild || m.Length == 0 {
				continue
			}
			if m.Length > math.MaxInt64 || m.StorageOffset > math.MaxInt64-m.Length {
				return nil, fmt.Errorf("%s %s: mapping storage range overflows", m.Build, h.Kind)
			}
			k := FileKey{m.Build, h.Kind}
			kept[k] = append(kept[k], Range{int64(m.StorageOffset), int64(m.StorageOffset + m.Length)})
		}
	}
	var plans []FilePlan
	for _, file := range files {
		if file.State.Size < 0 {
			return nil, fmt.Errorf("%s %s: negative file size", file.Key.Build, file.Key.Name)
		}
		ranges := union(kept[file.Key])
		p := FilePlan{File: file}
		var end int64
		for _, r := range ranges {
			if r.End > file.State.Size {
				return nil, fmt.Errorf("%s %s: mapping ends at %d, past file size %d", file.Key.Build, file.Key.Name, r.End, file.State.Size)
			}
			p.Holes = appendHole(p.Holes, end, r.Start, blockSize)
			p.Kept += r.End - r.Start
			end = r.End
		}
		p.Holes = appendHole(p.Holes, end, file.State.Size, blockSize)
		plans = append(plans, p)
	}
	return plans, nil
}

func union(rs []Range) []Range {
	rs = slices.Clone(rs)
	slices.SortFunc(rs, func(a, b Range) int { return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End)) })
	var out []Range
	for _, r := range rs {
		if n := len(out); n > 0 && r.Start <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, r.End)
		} else {
			out = append(out, r)
		}
	}
	return out
}

func appendHole(rs []Range, start, end, block int64) []Range {
	if remainder := start % block; remainder != 0 {
		padding := block - remainder
		if end-start < padding {
			return rs
		}
		start += padding
	}
	end -= end % block
	if start < end {
		rs = append(rs, Range{start, end})
	}
	return rs
}
