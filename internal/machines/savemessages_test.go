package machines_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSaveRecoveryMessagesFollowListingAndTime(t *testing.T) {
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.FixedZone("local", 2*60*60))
	ended := started.Add(time.Minute)
	for _, tc := range []struct {
		name                     string
		listed, paused, inFlight bool
		now                      time.Time
		wait                     string
		next                     string
	}{
		{"running after end", true, false, false, ended.Add(time.Minute), "web pauses, is removed, or the host restarts", "Run: hib pause web."},
		{"running at end", true, false, false, ended, "web pauses, is removed, or the host restarts", "Run: hib pause web."},
		{"running before end", true, false, false, started, "web pauses after 2026-10-05T08:01:00Z", "After that time, run: hib pause web."},
		{"running interrupted save", true, false, true, ended, "web pauses after 2026-10-05T08:10:00Z", "After that time, run: hib pause web."},
		{"paused after end", true, true, false, ended.Add(time.Minute), "web runs and pauses again, is removed, or the host restarts", "Wake and pause it: hib exec web -- true && hib pause web."},
		{"paused before end", true, true, false, started, "web runs and pauses again after 2026-10-05T08:01:00Z", "After that time, wake and pause it: hib exec web -- true && hib pause web."},
		{"missing after end", false, false, false, ended.Add(time.Minute), "web runs and pauses again, is removed, or the host restarts", "Wake and pause it: hib exec web -- true && hib pause web."},
		{"missing interrupted save", false, false, true, ended, "web runs and pauses again after 2026-10-05T08:10:00Z", "After that time, wake and pause it: hib exec web -- true && hib pause web."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			dir := filepath.Join(state, "saves")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			r := cleanup.SaveRecord{Machine: "web", Name: "web", Started: started, Ended: ended}
			if tc.inFlight {
				r.Ended = time.Time{}
			}
			writeSaveFixture(t, filepath.Join(dir, "web-"+strings.Repeat("a", 32)), r)
			at := tc.now
			m := machines.New(machines.Config{StateDir: state, Now: func() time.Time { return at }, BootID: func() string { return "" }})
			var l cleanup.Listing
			if tc.listed {
				l.Machines = []cleanup.Machine{{ID: "web", Paused: tc.paused}}
			}
			err, _ := checkSaveFixture(t, m, l, false)
			if !errors.Is(err, machines.ErrSaveUnsettled) {
				t.Fatal(err)
			}
			message := err.Error()
			if !strings.Contains(message, "started at 2026-10-05T08:00:00Z") || !strings.Contains(message, tc.wait) || !strings.HasSuffix(message, tc.next) {
				t.Fatalf("recovery message = %s", message)
			}
			if strings.Contains(message, "begun") || strings.Contains(message, "0001-01") || strings.Contains(message, "+0200") {
				t.Fatal(message)
			}
			at = at.Add(time.Second)
			again, _ := checkSaveFixture(t, m, l, false)
			if again == nil || again.Error() != message {
				t.Fatalf("same branch changed as time advanced: %v", again)
			}
		})
	}
}

func TestSaveRecoveryMessageChangesOnlyAtTheRecoveryBoundary(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "saves")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	writeSaveFixture(t, filepath.Join(dir, "web-"+strings.Repeat("a", 32)), cleanup.SaveRecord{Machine: "web", Name: "web", Started: started})
	at := started
	m := machines.New(machines.Config{StateDir: state, Now: func() time.Time { return at }, BootID: func() string { return "" }})
	l := cleanup.Listing{Machines: []cleanup.Machine{{ID: "web"}}}
	before, _ := checkSaveFixture(t, m, l, false)
	at = started.Add(9 * time.Minute)
	still, _ := checkSaveFixture(t, m, l, false)
	at = started.Add(10 * time.Minute)
	after, _ := checkSaveFixture(t, m, l, false)
	if before == nil || still == nil || after == nil || before.Error() != still.Error() || before.Error() == after.Error() {
		t.Fatalf("boundary messages: %v, %v, %v", before, still, after)
	}
}
