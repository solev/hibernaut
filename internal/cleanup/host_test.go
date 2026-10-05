package cleanup_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine/fake"
)

const (
	imageBuild  = "11111111-1111-4111-8111-111111111111"
	cachedBuild = "22222222-2222-4222-8222-222222222222"
	freshBuild  = "33333333-3333-4333-8333-333333333333"
	orphanA     = "44444444-4444-4444-8444-444444444444"
	orphanB     = "55555555-5555-4555-8555-555555555555"
)

func newHost(t *testing.T, now time.Time) cleanup.Host {
	t.Helper()
	header, err := os.ReadFile("testdata/memfile.header")
	if err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	for _, id := range []string{imageBuild, cachedBuild, freshBuild, orphanA, orphanB} {
		dir := filepath.Join(storage, id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range cleanup.Headers {
			if err := os.WriteFile(filepath.Join(dir, name), header, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if id != freshBuild {
			touch(t, dir, now.Add(-2*time.Hour))
		}
	}
	cache := t.TempDir()
	index := filepath.Join(cache, "scope", "index")
	if err := os.MkdirAll(index, 0o755); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("0a", 32)
	for name, data := range map[string]string{
		hash: `{"template":{"build_id":"` + cachedBuild + `"}}`,
		// E2B writes each file of the index to a hidden temporary file first, and a crash
		// leaves that half-written.
		"." + hash + ".tmp-1234": `{"templ`,
	} {
		if err := os.WriteFile(filepath.Join(index, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	built := strconv.FormatInt(now.Add(-3*time.Hour).UnixMicro(), 10)
	query := func(_ context.Context, sql string) ([][]string, error) {
		if strings.Contains(sql, "FROM envs") {
			return [][]string{{"img", "", ""}}, nil
		}
		return [][]string{{imageBuild, "ready", built, built, "img", "default", built}}, nil
	}
	return cleanup.Host{Storage: storage, Cache: cache, Query: query, Engine: fake.New()}
}

func touch(t *testing.T, dir string, at time.Time) {
	t.Helper()
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
}

func wantDirs(t *testing.T, h cleanup.Host, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(h.Storage)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("build directories = %q, want %q", got, want)
	}
}

type lines []string

func (l *lines) logf(format string, args ...any) { *l = append(*l, fmt.Sprintf(format, args...)) }

func (l lines) has(s string) bool {
	return slices.ContainsFunc(l, func(line string) bool { return strings.Contains(line, s) })
}

func TestCleanupDeletesWhatNothingUses(t *testing.T) {
	now := time.Now()
	h := newHost(t, now)
	var l lines
	if err := h.Clean(t.Context(), now, false, l.logf); err != nil {
		t.Fatal(err)
	}
	wantDirs(t, h, imageBuild, cachedBuild, freshBuild)
	if !l.has("deleted "+orphanA) || !l.has("deleted "+orphanB) || !l.has("in 2 build directories, keeping 3") {
		t.Errorf("log = %q, want each deletion and what it freed", l)
	}
}

func TestCleanupDryRunDeletesNothing(t *testing.T) {
	now := time.Now()
	h := newHost(t, now)
	var l lines
	if err := h.Clean(t.Context(), now, true, l.logf); err != nil {
		t.Fatal(err)
	}
	wantDirs(t, h, imageBuild, cachedBuild, freshBuild, orphanA, orphanB)
	for _, want := range []string{
		"would delete " + orphanA,
		"would delete " + orphanB,
		"keep " + imageBuild,
		"the current build of image img",
		"keep " + cachedBuild,
		"E2B's layer cache points to it",
		"keep " + freshBuild,
	} {
		if !l.has(want) {
			t.Errorf("log doesn't say %q: %q", want, l)
		}
	}
}

func TestCleanupLeavesWhatChangesWhileItCleansUp(t *testing.T) {
	now := time.Now()
	h := newHost(t, now)
	var l lines
	logf := func(format string, args ...any) {
		l.logf(format, args...)
		if strings.HasPrefix(l[len(l)-1], "deleted "+orphanA) {
			touch(t, filepath.Join(h.Storage, orphanB), time.Now())
		}
	}
	if err := h.Clean(t.Context(), now, false, logf); err != nil {
		t.Fatal(err)
	}
	wantDirs(t, h, imageBuild, cachedBuild, freshBuild, orphanB)
	if !l.has("left " + orphanB + ": it changed while cleaning up") {
		t.Errorf("log = %q, want it to say it left %s", l, orphanB)
	}
}

func TestCleanupDeletesNothingIfTheCacheMakesNoSense(t *testing.T) {
	now := time.Now()
	h := newHost(t, now)
	bad := filepath.Join(h.Cache, "scope", "index", strings.Repeat("0b", 32))
	if err := os.WriteFile(bad, []byte(`{"template":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var l lines
	if err := h.Clean(t.Context(), now, false, l.logf); err == nil || !strings.Contains(err.Error(), "names no build") {
		t.Errorf("Clean = %v, want an error about the cache", err)
	}
	wantDirs(t, h, imageBuild, cachedBuild, freshBuild, orphanA, orphanB)
}

func fakeDocker(t *testing.T, ps, psqlOut, psqlErr string) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
ps) printf '%s' "$FAKE_PS" ;;
exec)
	shift
	printf '%s\n' "$@" >> "$FAKE_EXEC_LOG"
	if [ -n "$FAKE_PSQL_ERR" ]; then printf '%s\n' "$FAKE_PSQL_ERR" >&2; exit 1; fi
	printf '%s' "$FAKE_PSQL_OUT" ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "exec.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_PS", ps)
	t.Setenv("FAKE_PSQL_OUT", psqlOut)
	t.Setenv("FAKE_PSQL_ERR", psqlErr)
	t.Setenv("FAKE_EXEC_LOG", log)
	return log
}

func TestPostgresRunsSQLInE2BsContainer(t *testing.T) {
	log := fakeDocker(t, "c0ffee\n", "a\tb\n\nc\t\n", "")
	rows, err := cleanup.Postgres(t.Context(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"a", "b"}, {"c", ""}}; !slices.EqualFunc(rows, want, slices.Equal[[]string]) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if args[0] != "c0ffee" || args[1] != "psql" || args[len(args)-1] != "SELECT 1" {
		t.Errorf("docker exec %q, want psql in container c0ffee running the SQL", args)
	}
}

func TestPostgresWantsOneContainer(t *testing.T) {
	for _, ps := range []string{"", "c0ffee\nbeef\n"} {
		fakeDocker(t, ps, "", "")
		if _, err := cleanup.Postgres(t.Context(), "SELECT 1"); err == nil || !strings.Contains(err.Error(), "Postgres containers") {
			t.Errorf("with containers %q: err = %v, want one about the containers", ps, err)
		}
	}
}

func TestPostgresSaysWhyPsqlFailed(t *testing.T) {
	fakeDocker(t, "c0ffee\n", "", `ERROR:  relation "envs" does not exist`)
	if _, err := cleanup.Postgres(t.Context(), "SELECT 1"); err == nil || !strings.Contains(err.Error(), `relation "envs" does not exist`) {
		t.Errorf("err = %v, want psql's error", err)
	}
}
