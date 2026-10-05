package cleanup_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
)

func TestCleanupLegacyAnsweredSaveNeedsLaterPause(t *testing.T) {
	var record cleanup.SaveRecord
	if err := json.Unmarshal([]byte(`{"machine":"web","name":"web","started":"2026-10-05T10:00:00Z","ended":"2026-10-05T10:01:00Z","answered":true}`), &record); err != nil {
		t.Fatal(err)
	}
	l := cleanup.Listing{Machines: []cleanup.Machine{{ID: "web"}}}
	if cleanup.SaveSettled(l, record, "", 10*time.Minute) {
		t.Fatal("an HTTP answer without a successful save settled on a running listing")
	}
	b := ready(orphanA, "slot", time.Date(2026, 10, 5, 10, 3, 0, 0, time.UTC))
	l.Envs = []cleanup.Env{{ID: "slot", Machine: "web"}}
	l.Builds = []cleanup.Build{b}
	if !cleanup.SaveSettled(l, record, "", 10*time.Minute) {
		t.Fatal("a later machine-only pause did not settle the failed save")
	}
}

func TestCleanupSaveSettlementRequiresProof(t *testing.T) {
	started, ended := ago(4*time.Hour), ago(3*time.Hour)
	record := cleanup.SaveRecord{Machine: "web", Name: "web", Started: started, Ended: ended, Succeeded: true, BootID: "boot1"}
	pause := func(created time.Time) cleanup.Build {
		b := ready(orphanA, "slot", created.Add(time.Minute))
		b.Created = created
		return b
	}
	for _, tc := range []struct {
		name   string
		record cleanup.SaveRecord
		l      cleanup.Listing
		boot   string
		want   bool
	}{
		{"succeeded running", record, cleanup.Listing{Machines: []cleanup.Machine{{ID: "web"}}}, "boot1", true},
		{"unsuccessful running", cleanup.SaveRecord{Machine: "web", Started: started, Ended: ended}, cleanup.Listing{Machines: []cleanup.Machine{{ID: "web"}}}, "boot1", false},
		{"succeeded paused", record, cleanup.Listing{Machines: []cleanup.Machine{{ID: "web", Paused: true}}}, "boot1", false},
		{"later pause", record, cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web"}}, Builds: []cleanup.Build{pause(ended.Add(time.Minute))}}, "boot1", true},
		{"older pause", record, cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web"}}, Builds: []cleanup.Build{pause(ended.Add(-time.Minute))}}, "boot1", false},
		{"same instant", record, cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web"}}, Builds: []cleanup.Build{pause(ended)}}, "boot1", false},
		{"pending pause", record, cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web"}}, Builds: []cleanup.Build{{ID: orphanA, Status: "pending", Created: ended.Add(time.Minute), In: []cleanup.Assignment{{Env: "slot"}}}}}, "boot1", false},
		{"removed", record, cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web", Deleted: ended}}}, "boot1", true},
		{"one live slot", record, cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web", Deleted: ended}, {ID: "other", Machine: "web"}}}, "boot1", false},
		{"no slot", record, cleanup.Listing{}, "boot1", false},
		{"reboot", record, cleanup.Listing{}, "boot2", true},
		{"unknown current boot", record, cleanup.Listing{}, "", false},
		{"unknown saved boot", cleanup.SaveRecord{Machine: "web", Started: started}, cleanup.Listing{}, "boot2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanup.SaveSettled(tc.l, tc.record, tc.boot, 10*time.Minute); got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
	for _, deleted := range []bool{false, true} {
		l := cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web"}, {ID: "image"}}, Builds: []cleanup.Build{pause(ended.Add(time.Minute))}}
		if deleted {
			l.Envs[1].Deleted = ended
		}
		l.Builds[0].In = append(l.Builds[0].In, cleanup.Assignment{Env: "image"})
		if cleanup.SaveSettled(l, record, "boot1", 10*time.Minute) {
			t.Fatal("an image save settled a save record")
		}
	}
	for _, elapsed := range []time.Duration{9 * time.Minute, 10 * time.Minute, 11 * time.Minute} {
		r := cleanup.SaveRecord{Machine: "web", Started: started}
		l := cleanup.Listing{Envs: []cleanup.Env{{ID: "slot", Machine: "web"}}, Builds: []cleanup.Build{pause(started.Add(elapsed))}}
		if got := cleanup.SaveSettled(l, r, "boot1", 10*time.Minute); got != (elapsed > 10*time.Minute) {
			t.Fatalf("interrupted save with later pause at %v: settled %v", elapsed, got)
		}
	}
}
