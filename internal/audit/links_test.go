package audit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/links"
)

func openLinks(t *testing.T, path string, maxBytes int64) *audit.LinkLog {
	t.Helper()
	l, err := audit.OpenLinks(path, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func readLinks(t *testing.T, path string) []audit.LinkEntry {
	t.Helper()
	res, err := audit.ReadLinks(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Corrupt != 0 {
		t.Errorf("%d corrupt lines", res.Corrupt)
	}
	return res.Entries
}

func refusal(port int, reason error) audit.LinkEntry {
	return audit.LinkEntry{Event: "refused", From: "web", FromID: "web1", To: "api", ToID: "api1", Port: port, Reason: reason.Error()}
}

// A link is two lines, one when it opens and one when it ends, so a link that has been open for
// an hour is in the log for that hour.
func TestLinkOpenClosePair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.jsonl")
	l := openLinks(t, path, 1<<20)
	opened := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	if err := l.Write(audit.LinkEntry{Time: opened, Event: "open", From: "web", FromID: "web1", To: "api", ToID: "api1", Port: 8080, Woke: true}); err != nil {
		t.Fatal(err)
	}
	if got := readLinks(t, path); len(got) != 1 || got[0].Event != "open" || !got[0].Woke {
		t.Fatalf("after open = %+v, want the open line written at once", got)
	}
	if err := l.Write(audit.LinkEntry{Time: opened.Add(time.Hour), Event: "close", From: "web", FromID: "web1", To: "api", ToID: "api1", Port: 8080, BytesIn: 10, BytesOut: 20, SecondsOpen: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	got := readLinks(t, path)
	if len(got) != 2 {
		t.Fatalf("entries = %+v, want open and close", got)
	}
	if c := got[1]; c.Event != "close" || c.BytesIn != 10 || c.BytesOut != 20 || c.SecondsOpen != 3600 || !c.Time.Equal(opened.Add(time.Hour)) {
		t.Errorf("close = %+v", c)
	}
	if err := l.Write(audit.LinkEntry{Event: "open"}); err == nil {
		t.Error("Write after Close = nil, want an error")
	}
}

// A retry loop hitting the same refusal writes one line per window, not one per try. A different
// port is a different refusal, and opens are never folded into anything.
func TestLinkRefusalsFold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.jsonl")
	l := openLinks(t, path, 1<<20)
	for range 1000 {
		if err := l.Write(refusal(8080, links.ErrSourceIdle)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Write(refusal(5432, links.ErrSourceIdle)); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(refusal(8080, links.ErrNoRoom)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := l.Write(audit.LinkEntry{Event: "open", FromID: "web1", ToID: "api1", Port: 8080}); err != nil {
			t.Fatal(err)
		}
	}
	if got := readLinks(t, path); len(got) != 2 || got[0].Event != "open" || got[1].Event != "open" {
		t.Fatalf("before Close = %+v, want only the two opens", got)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range readLinks(t, path)[2:] {
		if e.Event != "refused" {
			t.Errorf("unexpected %+v", e)
		}
		counts[fmt.Sprintf("%s %d", e.Reason, e.Port)] = e.Count
	}
	want := map[string]int{"source idle 8080": 1000, "source idle 5432": 0, "no room 8080": 0}
	if len(counts) != len(want) {
		t.Fatalf("refusal lines = %v, want %v", counts, want)
	}
	for k, n := range want {
		if c, ok := counts[k]; !ok || c != n {
			t.Errorf("%s: count %d (present %v), want %d", k, c, ok, n)
		}
	}
}

// A fold is written when its window ends, without waiting for shutdown, and the next identical
// refusal starts a new window.
func TestLinkFoldFlushesAfterWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "links.jsonl")
		l := openLinks(t, path, 1<<20)
		defer l.Close()
		for range 3 {
			if err := l.Write(refusal(8080, links.ErrRateLimited)); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(links.FoldWindow - time.Second)
		synctest.Wait()
		if got := readLinks(t, path); len(got) != 0 {
			t.Fatalf("inside the window = %+v, want nothing yet", got)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		got := readLinks(t, path)
		if len(got) != 1 || got[0].Count != 3 || got[0].Reason != "rate limited" {
			t.Fatalf("after the window = %+v, want one line counting 3", got)
		}
		if err := l.Write(refusal(8080, links.ErrRateLimited)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(links.FoldWindow + time.Second)
		synctest.Wait()
		if got := readLinks(t, path); len(got) != 2 || got[1].Count != 0 {
			t.Fatalf("second window = %+v, want a second line for one refusal", got)
		}
	})
}

func TestLinkLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.jsonl")
	l := openLinks(t, path, 300)
	for i := range 6 {
		if err := l.Write(audit.LinkEntry{Event: "open", From: "web", FromID: "web1", To: "api", ToID: "api1", Port: 8000 + i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() > 300 || fi.Mode().Perm() != 0o644 {
		t.Errorf("current file = %v, %v: want at most 300 bytes at 0644", fi, err)
	}
	got := readLinks(t, path)
	if len(got) < 2 || got[len(got)-1].Port != 8005 {
		t.Fatalf("entries = %+v, want the newest last", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Port != got[i-1].Port+1 {
			t.Errorf("entries out of order: %+v", got)
		}
	}
	res, err := audit.ReadLinks(path, 1)
	if err != nil || len(res.Entries) != 1 || res.Entries[0].Port != 8005 {
		t.Errorf("ReadLinks(1) = %+v, %v", res, err)
	}
}

// A listener whose log could not be opened still carries traffic.
func TestNilLinkLogDrops(t *testing.T) {
	var l *audit.LinkLog
	if err := l.Write(refusal(8080, links.ErrNoRoom)); err != nil {
		t.Errorf("Write = %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	if l.Path() != "" || l.MaxBytes() != 0 {
		t.Errorf("Path, MaxBytes = %q, %d", l.Path(), l.MaxBytes())
	}
}

func TestLinkEntryCleaned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.jsonl")
	l := openLinks(t, path, 1<<20)
	if err := l.Write(audit.LinkEntry{Event: "open", From: "we\x1b[2Jb", To: strings.Repeat("a", 100), Count: 7}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	got := readLinks(t, path)
	if len(got) != 1 || got[0].From != "we?[2Jb" || len(got[0].To) != 64 || got[0].Count != 0 || got[0].Time.IsZero() {
		t.Errorf("entry = %+v", got)
	}
}
