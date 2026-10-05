package machines_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/machines"
)

func writeSaveFixture(t *testing.T, path string, record cleanup.SaveRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func checkSaveFixture(t *testing.T, m *machines.Manager, l cleanup.Listing, dry bool) (error, []string) {
	t.Helper()
	release, err := m.TryCleanupRead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var logs []string
	err = m.CheckSaves(t.Context(), l, dry, func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	})
	return err, logs
}

func TestSavePublicationDebrisRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, kind                         string
		record, succeeded, blocked, damage bool
	}{
		{"partial temp alone", "partial", false, false, false, false},
		{"complete replacement temp", "complete", true, false, true, false},
		{"linked successful record", "linked", true, true, false, false},
		{"linked failed record", "linked", true, false, true, false},
		{"unexplained external link", "extra link", true, true, true, true},
		{"debris symlink", "symlink", false, false, true, false},
		{"debris directory", "directory", false, false, true, false},
		{"unexpected name", "unexpected", false, false, true, false},
		{"uppercase token", "uppercase", false, false, true, false},
		{"short token", "short", false, false, true, false},
		{"damaged record", "damaged", true, false, true, true},
		{"mismatched machine", "mismatch", true, false, true, true},
		{"success without end", "no end", true, true, true, true},
	} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%v", tc.name, dry), func(t *testing.T) {
				state := t.TempDir()
				dir := filepath.Join(state, "saves")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				key := "web-" + strings.Repeat("a", 32)
				path := filepath.Join(dir, key)
				temp := filepath.Join(dir, "."+key+".tmp-"+strings.Repeat("b", 32))
				started := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
				r := cleanup.SaveRecord{Machine: "web", Name: "web", Started: started, Ended: started.Add(time.Minute), BootID: "boot1", Succeeded: tc.succeeded}
				if tc.record {
					writeSaveFixture(t, path, r)
				}
				messagePath := temp
				switch tc.kind {
				case "partial":
					if err := os.WriteFile(temp, []byte("{"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "complete":
					r.Succeeded = true
					writeSaveFixture(t, temp, r)
				case "linked":
					if err := os.Link(path, temp); err != nil {
						t.Fatal(err)
					}
				case "extra link":
					if err := os.Link(path, filepath.Join(t.TempDir(), "unexplained")); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(t.TempDir(), temp); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(temp, 0o700); err != nil {
						t.Fatal(err)
					}
				case "unexpected", "uppercase", "short":
					name := "web-record"
					if tc.kind == "uppercase" {
						name = "web-" + strings.Repeat("A", 32)
					} else if tc.kind == "short" {
						name = "web-" + strings.Repeat("a", 31)
					}
					messagePath = filepath.Join(dir, name)
					if err := os.WriteFile(messagePath, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				case "damaged":
					if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "mismatch":
					r.Machine = "other"
					writeSaveFixture(t, path, r)
				case "no end":
					r.Ended = time.Time{}
					writeSaveFixture(t, path, r)
				}
				m := machines.New(machines.Config{StateDir: state, BootID: func() string { return "boot1" }})
				l := cleanup.Listing{Machines: []cleanup.Machine{{ID: "web"}}}
				err, logs := checkSaveFixture(t, m, l, dry)
				if errors.Is(err, machines.ErrSaveUnsettled) != tc.blocked || err != nil && !tc.blocked {
					t.Fatalf("cleanup = %v, logs = %v", err, logs)
				}
				if tc.damage {
					for _, part := range []string{path, "web", "host has restarted", "sudo mkdir -p", "sudo mv"} {
						if !strings.Contains(err.Error(), part) {
							t.Errorf("damaged-record message lacks %q: %v", part, err)
						}
					}
					if strings.Contains(err.Error(), "0001-01") {
						t.Errorf("damaged-record message printed a zero time: %v", err)
					}
				} else if tc.blocked && tc.kind != "complete" && tc.kind != "linked" {
					if !strings.Contains(err.Error(), messagePath) || !strings.Contains(strings.ToLower(err.Error()), "move it out of saves") || strings.Contains(err.Error(), "host has restarted") {
						t.Errorf("unexpected-entry procedure = %v", err)
					}
				}
				if tc.kind == "partial" || tc.kind == "complete" || tc.kind == "linked" {
					_, err := os.Lstat(temp)
					if dry && err != nil || !dry && !errors.Is(err, os.ErrNotExist) {
						t.Errorf("debris dry=%v: %v", dry, err)
					}
					verb := "removed save publication debris"
					if dry {
						verb = "would remove save publication debris"
					}
					if len(logs) == 0 || !strings.Contains(logs[0], verb) || !strings.Contains(logs[0], "web") {
						t.Errorf("debris log = %v", logs)
					}
				}
				if tc.record {
					_, err := os.Lstat(path)
					kept := dry || tc.blocked
					if kept && err != nil || !kept && !errors.Is(err, os.ErrNotExist) {
						t.Errorf("record kept=%v: %v", kept, err)
					}
				}
			})
		}
	}
}
