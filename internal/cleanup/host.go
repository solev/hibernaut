package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

// Where E2B keeps builds on the host, as vm/e2b/e2b-orchestrator.env sets it up.
const (
	StorageDir = "/var/lib/e2b/local-template-storage"
	CacheDir   = "/var/lib/e2b/local-build-cache"
)

// Host is E2B on this host.
type Host struct {
	// Storage has a directory per build, and Cache the layer cache's index.
	Storage, Cache string
	// Query runs SQL in E2B's database and returns its rows, each a list of fields.
	Query  func(ctx context.Context, sql string) ([][]string, error)
	Engine engine.Engine
}

// Clean deletes the directories Garbage plans to, logging each, and then what it freed. With
// dryRun, it only logs what it would delete, and why it keeps the rest.
func (h Host) Clean(ctx context.Context, now time.Time, dryRun bool, logf func(format string, args ...any)) error {
	l, err := h.read(ctx)
	if err != nil {
		return err
	}
	p, err := Garbage(l, now)
	if err != nil {
		return err
	}
	modified := map[string]time.Time{}
	for _, d := range l.Dirs {
		modified[d.Build] = d.Modified
	}
	if dryRun {
		for _, d := range l.Dirs {
			if why, ok := p.Keep[d.Build]; ok {
				logf("keep %s (%s): %s", d.Build, size(h.path(d.Build)), why)
			}
		}
	}
	var freed int64
	n := 0
	for _, id := range p.Delete {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stopped before deleting %s, with %d deleted: %w", id, n, err)
		}
		path := h.path(id)
		used := du(path)
		if dryRun {
			logf("would delete %s (%s)", id, hibapi.FormatMB(used>>20))
		} else {
			if m, err := newest(path); err != nil || m.After(modified[id]) {
				logf("left %s: it changed while cleaning up", id)
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("deleting %s: %w", id, err)
			}
			logf("deleted %s (%s)", id, hibapi.FormatMB(used>>20))
		}
		freed += used
		n++
	}
	switch {
	case n == 0:
		logf("nothing to delete; keeping %d build directories", len(p.Keep))
	case dryRun:
		logf("would free %s in %d build directories, keeping %d", hibapi.FormatMB(freed>>20), n, len(p.Keep))
	default:
		logf("freed %s in %d build directories, keeping %d", hibapi.FormatMB(freed>>20), n, len(p.Keep))
	}
	return nil
}

func (h Host) path(build string) string { return filepath.Join(h.Storage, build) }

func (h Host) read(ctx context.Context) (Listing, error) {
	var l Listing
	machines, err := h.Engine.List(ctx, engine.ListOptions{})
	if err != nil {
		return Listing{}, err
	}
	for _, m := range machines {
		l.Machines = append(l.Machines, Machine{ID: m.ID, Started: m.StartedAt, Paused: m.State == engine.Paused})
	}
	rows, err := h.query(ctx, 3, `SELECT e.id,
		coalesce((SELECT s.sandbox_id FROM snapshots s WHERE s.env_id = e.id LIMIT 1), ''),
		coalesce(floor(extract(epoch FROM e.deleted_at) * 1000000)::bigint::text, '')
		FROM envs e`)
	if err != nil {
		return Listing{}, err
	}
	for _, r := range rows {
		e := Env{ID: r[0], Machine: r[1]}
		if r[2] != "" {
			if e.Deleted, err = micros(r[2]); err != nil {
				return Listing{}, fmt.Errorf("env %s: %w", e.ID, err)
			}
		}
		l.Envs = append(l.Envs, e)
	}
	// env_builds.env_id is only the first env a build was put in.
	rows, err = h.query(ctx, 7, `SELECT b.id, coalesce(b.status_group, ''),
		floor(extract(epoch FROM b.created_at) * 1000000)::bigint,
		coalesce(floor(extract(epoch FROM b.finished_at) * 1000000)::bigint::text, ''),
		coalesce(a.env_id, ''), coalesce(a.tag, ''),
		coalesce(floor(extract(epoch FROM a.created_at) * 1000000)::bigint::text, '')
		FROM env_builds b LEFT JOIN env_build_assignments a ON a.build_id = b.id
		ORDER BY b.id`)
	if err != nil {
		return Listing{}, err
	}
	for _, r := range rows {
		if n := len(l.Builds); n == 0 || l.Builds[n-1].ID != r[0] {
			b := Build{ID: r[0], Status: r[1]}
			if b.Created, err = micros(r[2]); err == nil && r[3] != "" {
				b.Finished, err = micros(r[3])
			}
			if err != nil {
				return Listing{}, fmt.Errorf("build %s: %w", b.ID, err)
			}
			l.Builds = append(l.Builds, b)
		}
		if r[4] == "" {
			continue
		}
		at, err := micros(r[6])
		if err != nil {
			return Listing{}, fmt.Errorf("build %s in env %s: %w", r[0], r[4], err)
		}
		b := &l.Builds[len(l.Builds)-1]
		b.In = append(b.In, Assignment{Env: r[4], Tag: r[5], At: at})
	}
	if l.Cached, err = readCache(h.Cache); err != nil {
		return Listing{}, err
	}
	if l.Dirs, err = readDirs(h.Storage); err != nil {
		return Listing{}, err
	}
	return l, nil
}

func (h Host) query(ctx context.Context, fields int, sql string) ([][]string, error) {
	rows, err := h.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("reading E2B's database: %w", err)
	}
	for _, r := range rows {
		if len(r) != fields {
			return nil, fmt.Errorf("reading E2B's database: got %d fields, want %d: %q", len(r), fields, r)
		}
	}
	return rows, nil
}

// micros parses microseconds since 1970: Postgres keeps times to the microsecond.
func micros(s string) (time.Time, error) {
	us, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad time %q", s)
	}
	return time.UnixMicro(us), nil
}

// buildName matches the names of build directories, which are UUIDs.
var buildName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func readDirs(storage string) ([]Dir, error) {
	entries, err := os.ReadDir(storage)
	if err != nil {
		return nil, err
	}
	var dirs []Dir
	for _, e := range entries {
		if !e.IsDir() || !buildName.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(storage, e.Name())
		d := Dir{Build: e.Name()}
		if d.Modified, err = newest(path); err != nil {
			return nil, err
		}
		for _, name := range Headers {
			b, err := os.ReadFile(filepath.Join(path, name))
			var refs []string
			if err == nil {
				refs, err = ParseHeader(b)
			}
			if err != nil {
				d.Err = fmt.Errorf("%s: %w", name, err)
				break
			}
			d.Refs = append(d.Refs, refs...)
		}
		dirs = append(dirs, d)
	}
	return dirs, nil
}

func newest(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	t := info.ModTime()
	entries, err := os.ReadDir(path)
	if err != nil {
		return time.Time{}, err
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return time.Time{}, err
		}
		if info.ModTime().After(t) {
			t = info.ModTime()
		}
	}
	return t, nil
}

func du(path string) int64 {
	var n int64
	entries, _ := os.ReadDir(path)
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				n += st.Blocks * 512
			}
		}
	}
	return n
}

func size(path string) string { return hibapi.FormatMB(du(path) >> 20) }

// indexName matches the names of the files in the layer cache's index, which are SHA-256 hashes.
// E2B writes each to a hidden temporary file first, which a crash can leave half-written.
var indexName = regexp.MustCompile(`^[0-9a-f]{64}$`)

func readCache(cache string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(cache, "*", "index", "*"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, f := range files {
		if !indexName.MatchString(filepath.Base(f)) {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("reading E2B's layer cache: %w", err)
		}
		var entry struct {
			Template struct {
				BuildID string `json:"build_id"`
			} `json:"template"`
		}
		if err := json.Unmarshal(b, &entry); err != nil || entry.Template.BuildID == "" {
			return nil, fmt.Errorf("reading E2B's layer cache: %s names no build: %q", f, b)
		}
		ids = append(ids, entry.Template.BuildID)
	}
	return ids, nil
}

// Postgres runs sql in E2B's database: psql in the Postgres container of E2B's local-dev
// Compose project.
func Postgres(ctx context.Context, sql string) ([][]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project=local-dev",
		"--filter", "label=com.docker.compose.service=postgres").Output()
	if err != nil {
		return nil, fmt.Errorf("finding E2B's Postgres container: %w", commandError(err))
	}
	ids := strings.Fields(string(out))
	if len(ids) != 1 {
		return nil, fmt.Errorf("found %d Postgres containers of E2B, want 1", len(ids))
	}
	cmd := exec.CommandContext(ctx, "docker", "exec", ids[0],
		"psql", "-qAt", "-F", "\t", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", sql)
	if out, err = cmd.Output(); err != nil {
		return nil, fmt.Errorf("psql: %w", commandError(err))
	}
	var rows [][]string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows, nil
}

func commandError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(exit.Stderr))
	}
	return err
}
