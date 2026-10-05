package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/snapheader"
)

// TrimStorage accounts for headers and bodies, and applies identity-checked hole punches.
type TrimStorage interface {
	Directory(build string) error
	Header(FileKey) ([]byte, error)
	Stat(FileKey) (FileState, error)
	BlockSize() (int64, error)
	Apply(context.Context, FilePlan, bool) (TrimResult, error)
	Close() error
}

// TrimResult reports allocated bytes freed, or estimated in a dry run, and why a file was left.
type TrimResult struct {
	Freed int64
	Left  string
}

// Trimmer frees unread ranges without changing headers, file sizes or modification times.
type Trimmer struct {
	Storage TrimStorage
	Scope   string
}

// Trim checks every root before applying any file plan. Unsafe storage is reported, not changed.
func (t Trimmer) Trim(ctx context.Context, l Listing, p Plan, now time.Time, dry bool, logf func(string, ...any)) error {
	scope := t.Scope
	if scope == "" {
		scope = "e2e"
	}
	switch scope {
	case "off":
		return nil
	case "e2e", "all":
	default:
		logf("not trimming: HIBD_TRIM is %q, not off, e2e or all", scope)
		return nil
	}
	headers, err := t.account(ctx, l, p.Roots)
	if err != nil {
		if ctx.Err() != nil {
			if !dry {
				logf("trimmed %s in 0 files", hibapi.FormatMB(0))
			}
			return ctx.Err()
		}
		logf("not trimming: %s", err)
		return nil
	}
	block, err := t.Storage.BlockSize()
	if err != nil {
		logf("not trimming: %s", err)
		return nil
	}
	ids := trimCandidates(l, p, headers, scope, now)
	var files []TrimFile
	for _, id := range ids {
		for _, kind := range []string{"memfile", "rootfs.ext4"} {
			key := FileKey{id, kind}
			st, err := t.Storage.Stat(key)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil || !st.Regular {
				logf("left %s %s: %s", id, kind, fileProblem(err))
				continue
			}
			files = append(files, TrimFile{Key: key, State: st})
		}
	}
	plans, err := PlanTrim(headers, files, block)
	if err != nil {
		logf("not trimming: %s", err)
		return nil
	}
	return t.apply(ctx, plans, dry, logf)
}

func fileProblem(err error) string {
	if err != nil {
		return err.Error()
	}
	return "not a regular file"
}

func (t Trimmer) account(ctx context.Context, l Listing, roots map[string]string) ([]RootHeader, error) {
	dirs := map[string]bool{}
	for _, d := range l.Dirs {
		dirs[d.Build] = true
	}
	parsed := map[FileKey]snapheader.Header{}
	states := map[FileKey]FileState{}
	checkedDirs := map[string]bool{}
	directory := func(id string) error {
		if checkedDirs[id] {
			return nil
		}
		if err := t.Storage.Directory(id); err != nil {
			return err
		}
		checkedDirs[id] = true
		return nil
	}
	read := func(key FileKey) (snapheader.Header, error) {
		if h, ok := parsed[key]; ok {
			return h, nil
		}
		b, err := t.Storage.Header(key)
		if err != nil {
			return snapheader.Header{}, fmt.Errorf("%s %s: %w", key.Build, key.Name, err)
		}
		h, err := snapheader.Parse(b, key.Build)
		if err != nil {
			return snapheader.Header{}, fmt.Errorf("%s %s: %w", key.Build, key.Name, err)
		}
		parsed[key] = h
		return h, nil
	}
	var headers []RootHeader
	for _, id := range slices.Sorted(maps.Keys(roots)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !dirs[id] {
			return nil, fmt.Errorf("%s directory: missing root", id)
		}
		if err := directory(id); err != nil {
			return nil, fmt.Errorf("%s directory: %w", id, err)
		}
		for _, kind := range []string{"memfile", "rootfs.ext4"} {
			h, err := read(FileKey{id, kind + ".header"})
			if err != nil {
				return nil, err
			}
			headers = append(headers, RootHeader{Kind: kind, Header: h})
			for _, m := range h.Mappings {
				if m.Build == snapheader.NilBuild || m.Length == 0 {
					continue
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if err := directory(m.Build); err != nil {
					return nil, fmt.Errorf("%s directory: source of %s %s: %w", m.Build, id, kind, err)
				}
				key := FileKey{m.Build, kind}
				st, ok := states[key]
				if !ok {
					st, err = t.Storage.Stat(key)
					if err != nil {
						return nil, fmt.Errorf("%s %s: source of %s: %w", m.Build, kind, id, err)
					}
					states[key] = st
				}
				if !st.Regular || st.Size < int64(m.StorageOffset+m.Length) {
					return nil, fmt.Errorf("%s %s: not a regular file covering mapping end %d from %s", m.Build, kind, m.StorageOffset+m.Length, id)
				}
				if _, err := read(FileKey{m.Build, kind + ".header"}); err != nil {
					return nil, err
				}
			}
		}
	}
	return headers, nil
}

func trimCandidates(l Listing, p Plan, headers []RootHeader, scope string, now time.Time) []string {
	envs := indexEnvs(l.Envs)
	e2e := map[string]bool{}
	for _, m := range l.Machines {
		e2e[m.ID] = m.Metadata["hib-test"] == "e2e" || strings.HasPrefix(m.Metadata["hib.name"], "e2e-")
	}
	builds := map[string]Build{}
	for _, b := range l.Builds {
		builds[b.ID] = b
	}
	inScope := func(b Build) bool {
		if len(b.In) == 0 {
			return false
		}
		for _, a := range b.In {
			if m := envs[a.Env].Machine; m == "" || !e2e[m] {
				return false
			}
		}
		return true
	}
	excluded := map[string]bool{}
	for _, h := range headers {
		excluded[h.Header.BaseBuild] = true
		if scope == "e2e" && !inScope(builds[h.Header.Build]) {
			for _, m := range h.Header.Mappings {
				if m.Build != snapheader.NilBuild {
					excluded[m.Build] = true
				}
			}
		}
	}
	var ids []string
	for _, d := range l.Dirs {
		b, known := builds[d.Build]
		_, kept := p.Keep[d.Build]
		_, root := p.Roots[d.Build]
		if !known || !kept || root || excluded[d.Build] || b.Status != "ready" || now.Sub(d.Modified) < MinAge || len(b.In) == 0 {
			continue
		}
		machineOnly := true
		for _, a := range b.In {
			if envs[a.Env].Machine == "" {
				machineOnly = false
			}
		}
		if machineOnly && (scope == "all" || inScope(b)) {
			ids = append(ids, b.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func (t Trimmer) apply(ctx context.Context, plans []FilePlan, dry bool, logf func(string, ...any)) error {
	var freed int64
	var n int
	var stopped error
	for _, p := range plans {
		if err := ctx.Err(); err != nil {
			stopped = err
			break
		}
		if len(p.Holes) == 0 {
			continue
		}
		result, err := t.Storage.Apply(ctx, p, dry)
		key := p.File.Key
		if result.Left != "" {
			logf("left %s %s: %s", key.Build, key.Name, result.Left)
		}
		if result.Freed > 0 {
			if dry {
				logf("would trim %s %s: keeps %s of %s, frees about %s", key.Build, key.Name,
					hibapi.FormatMB(p.Kept>>20), hibapi.FormatMB(p.File.State.Size>>20), hibapi.FormatMB(result.Freed>>20))
			} else {
				logf("trimmed %s %s: freed %s", key.Build, key.Name, hibapi.FormatMB(result.Freed>>20))
			}
			freed += result.Freed
			n++
		}
		if err != nil {
			stopped = err
			break
		}
	}
	switch {
	case n == 0 && stopped == nil:
		logf("nothing to trim")
	case n == 0 && stopped != nil && !dry:
		logf("trimmed %s in 0 files", hibapi.FormatMB(0))
	case dry && n > 0:
		logf("would trim about %s in %d files", hibapi.FormatMB(freed>>20), n)
	case n > 0:
		logf("trimmed %s in %d files", hibapi.FormatMB(freed>>20), n)
	}
	return stopped
}
