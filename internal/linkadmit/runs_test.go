package linkadmit_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/links"
)

func cgroup(t *testing.T, root, id string, run links.Run, populated bool) string {
	t.Helper()
	dir := filepath.Join(root, "sbx-"+id+"-"+string(run))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setPopulated(t, dir, populated)
	return dir
}

func setPopulated(t *testing.T, dir string, populated bool) {
	t.Helper()
	v := "0"
	if populated {
		v = "1"
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte("populated "+v+"\nfrozen 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRuns(t *testing.T) (*linkadmit.RunWatcher, string) {
	t.Helper()
	root := t.TempDir()
	w, err := linkadmit.NewRuns(root)
	if err != nil {
		t.Fatal(err)
	}
	if w.Root() != root {
		t.Fatalf("Root = %q, want %q", w.Root(), root)
	}
	return w, root
}

func closedWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	default:
	}
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func TestRunWatcherFiresOnPopulatedZero(t *testing.T) {
	w, root := newRuns(t)
	dir := cgroup(t, root, "api", "r1", true)
	other := cgroup(t, root, "web", "r1", true)
	ended, err := w.Watch("api", "r1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.Watch("api", "r1")
	if err != nil || again != ended {
		t.Errorf("a second Watch of the same run = %v, %v, want the same channel", again, err)
	}
	otherEnded, err := w.Watch("web", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if closedWithin(ended, 300*time.Millisecond) {
		t.Fatal("closed while the run is live")
	}
	setPopulated(t, dir, false)
	if !closedWithin(ended, 5*time.Second) {
		t.Fatal("not closed after populated 0")
	}
	if closedWithin(otherEnded, 300*time.Millisecond) {
		t.Error("another machine's run ended with it")
	}
	setPopulated(t, other, false)
	if !closedWithin(otherEnded, 5*time.Second) {
		t.Fatal("the other run not closed after populated 0")
	}
}

func TestRunWatcherFiresOnRemoval(t *testing.T) {
	w, root := newRuns(t)
	dir := cgroup(t, root, "api", "r1", true)
	ended, err := w.Watch("api", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if !closedWithin(ended, 5*time.Second) {
		t.Fatal("not closed after the cgroup went away")
	}
	// A new run of the same machine is a new watch, not the old one's closed channel.
	cgroup(t, root, "api", "r2", true)
	next, err := w.Watch("api", "r2")
	if err != nil {
		t.Fatal(err)
	}
	if closedWithin(next, 300*time.Millisecond) {
		t.Error("the next run's watch is closed")
	}
}

// Admission cannot race the first read: a run that has ended, lingering or gone, is already
// closed.
func TestRunWatcherEndedRunIsClosed(t *testing.T) {
	w, root := newRuns(t)
	cgroup(t, root, "api", "r1", false)
	for _, run := range []links.Run{"r1", "r9"} {
		ended, err := w.Watch("api", run)
		if err != nil {
			t.Fatal(err)
		}
		if !closedWithin(ended, 0) {
			t.Errorf("Watch(api, %s) is open, want already closed", run)
		}
	}
	for _, bad := range []struct {
		id  string
		run links.Run
	}{{"../x", "r1"}, {"api", "../x"}, {"api", ""}} {
		if _, err := w.Watch(bad.id, bad.run); err == nil {
			t.Errorf("Watch(%q, %q) = nil error", bad.id, bad.run)
		}
	}
}

// Lingering cgroups of ended runs are not live, and identity is the directory name.
func TestLiveListsOnlyPopulatedRuns(t *testing.T) {
	w, root := newRuns(t)
	cgroup(t, root, "api", "r1", true)
	cgroup(t, root, "web", "r1", false)
	cgroup(t, root, "db", "old", false)
	cgroup(t, root, "db", "a-b-c", true)
	cgroup(t, root, "two", "r1", true)
	cgroup(t, root, "two", "r2", true)
	if err := os.MkdirAll(filepath.Join(root, "unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}
	live, err := w.Live()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]links.Run{"api": "r1", "db": "a-b-c"}
	if !maps.Equal(live, want) {
		t.Errorf("Live = %v, want %v", live, want)
	}
	if run, err := w.Current("db"); err != nil || run != "a-b-c" {
		t.Errorf("Current(db) = %q, %v", run, err)
	}
	if _, err := w.Current("web"); !errors.Is(err, links.ErrNotRunning) {
		t.Errorf("Current(web) = %v, want ErrNotRunning", err)
	}
	if _, err := w.Current("nope"); !errors.Is(err, links.ErrNotRunning) {
		t.Errorf("Current(nope) = %v, want ErrNotRunning", err)
	}
	if _, err := w.Current("two"); err == nil || errors.Is(err, links.ErrNotRunning) {
		t.Errorf("Current(two) = %v, want a refusal that is not ErrNotRunning", err)
	}
	if _, err := w.Current("Bad/ID"); err == nil {
		t.Error("Current(Bad/ID) = nil error")
	}
}
