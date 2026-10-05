package machines_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

func saveRecords(t *testing.T, dir string) []cleanup.SaveRecord {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "saves"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var records []cleanup.SaveRecord
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, "saves", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var r cleanup.SaveRecord
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(dir, "saves", entry.Name()))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("record permissions = %v, %v", fi, err)
		}
		records = append(records, r)
	}
	return records
}

func TestSaveRecordExistsDuringUncancelledBoundedCall(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	v := newEnvAt(t, func() time.Time { return now })
	v.create(t, "web")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := &faultEngine{Engine: v.e}
	e.save = func(call context.Context, id, alias string) (engine.TemplateInfo, error) {
		cancel()
		if call.Err() != nil {
			t.Error("caller cancellation reached the dispatched save")
		}
		deadline, bounded := call.Deadline()
		if !bounded || time.Until(deadline) < 9*time.Minute || time.Until(deadline) > 10*time.Minute {
			t.Errorf("save deadline = %v, bounded %v", deadline, bounded)
		}
		records := saveRecords(t, v.dir)
		if len(records) != 1 || records[0].Machine != id || records[0].Name != "web" || records[0].BootID != "boot1" || !records[0].Ended.IsZero() || records[0].Succeeded {
			t.Fatalf("in-flight record = %+v", records)
		}
		now = now.Add(time.Minute)
		return v.e.SaveTemplate(call, id, alias)
	}
	m := imageManager(v, func(cfg *machines.Config) {
		cfg.Engine, cfg.Now, cfg.BootID = e, func() time.Time { return now }, func() string { return "boot1" }
	})
	if _, err := m.SaveImage(ctx, "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	records := saveRecords(t, v.dir)
	if len(records) != 1 || !records[0].Succeeded || !records[0].Ended.Equal(now) {
		t.Fatalf("ended record = %+v", records)
	}
}

func TestSaveCancellationBeforeDispatchWritesNothing(t *testing.T) {
	v := newEnv(t)
	v.create(t, "web")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := &faultEngine{Engine: v.e, save: func(context.Context, string, string) (engine.TemplateInfo, error) {
		t.Error("save dispatched after cancellation")
		return engine.TemplateInfo{}, nil
	}}
	awake := 0
	m := imageManager(v, func(cfg *machines.Config) {
		cfg.Engine = e
		cfg.Awake = func() error {
			awake++
			if awake == 2 {
				cancel()
			}
			return nil
		}
	})
	_, err := m.SaveImage(ctx, "web", "myimg")
	if !errors.Is(err, context.Canceled) || len(saveRecords(t, v.dir)) != 0 {
		t.Fatalf("pre-dispatch cancellation = %v", err)
	}
}

func TestSaveRecordsOutcomesBeforeRestoration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cause     error
		succeeded bool
		deleted   bool
	}{
		{"success", nil, true, false},
		{"answered failure", errors.New("E2B returned 503"), false, false},
		{"no answer", errors.Join(engine.ErrNoAnswer, errors.New("connection lost")), false, false},
		{"404", engine.ErrNotFound, false, true},
		{"409", engine.ErrNotRunning, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			v.create(t, "web")
			if err := v.m.Pause(t.Context(), "web"); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
			e := &faultEngine{Engine: v.e}
			e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
				var info engine.TemplateInfo
				if !tc.deleted {
					var err error
					info, err = v.e.SaveTemplate(ctx, id, alias)
					if err != nil {
						t.Fatal(err)
					}
				}
				now = now.Add(time.Minute)
				return info, tc.cause
			}
			restored := false
			e.pause = func(ctx context.Context, id string) error {
				restored = true
				records := saveRecords(t, v.dir)
				if tc.deleted {
					if len(records) != 0 {
						t.Fatal("404/409 record survived")
					}
				} else if len(records) != 1 || !records[0].Ended.Equal(now) || records[0].Succeeded != tc.succeeded {
					t.Fatalf("record not ended before restoration: %+v", records)
				}
				return v.e.Pause(ctx, id)
			}
			m := imageManager(v, func(cfg *machines.Config) {
				cfg.Engine, cfg.Now = e, func() time.Time { return now }
			})
			if _, err := m.SaveImage(t.Context(), "web", "myimg"); !errors.Is(err, tc.cause) {
				t.Fatalf("save error = %v", err)
			}
			if !restored {
				t.Fatal("save did not restore the paused origin")
			}
		})
	}
}

func TestSaveRecordsSurviveManagerRestartAndDryRun(t *testing.T) {
	v := newEnv(t)
	web := v.create(t, "web")
	if _, err := imageManager(v, nil).SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	m := machines.New(machines.Config{StateDir: v.dir, BootID: func() string { return "" }})
	check := func(l cleanup.Listing, dry bool) error {
		release, err := m.TryCleanupRead(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		return m.CheckSaves(t.Context(), l, dry, func(string, ...any) {})
	}
	if err := check(cleanup.Listing{Machines: []cleanup.Machine{{ID: web.ID, Paused: true}}}, false); !errors.Is(err, machines.ErrSaveUnsettled) {
		t.Fatalf("restarted manager lost unsettled record: %v", err)
	}
	l := cleanup.Listing{Machines: []cleanup.Machine{{ID: web.ID}}}
	if err := check(l, true); err != nil || len(saveRecords(t, v.dir)) != 1 {
		t.Fatalf("dry run removed the record: %v", err)
	}
	if err := check(l, false); err != nil || len(saveRecords(t, v.dir)) != 0 {
		t.Fatalf("running machine didn't settle record: %v", err)
	}
}

func TestFailedSaveRunningListingNeedsLaterPause(t *testing.T) {
	for _, cause := range []error{
		errors.New("E2B returned 500"),
		errors.New("201 template readback failed"),
		errors.Join(engine.ErrNoAnswer, errors.New("connection lost")),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			v := newEnv(t)
			web := v.create(t, "web")
			e := &faultEngine{Engine: v.e}
			e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
				if _, err := v.e.SaveTemplate(ctx, id, alias); err != nil {
					t.Fatal(err)
				}
				return engine.TemplateInfo{}, cause
			}
			m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
			if _, err := m.SaveImage(t.Context(), "web", "myimg"); !errors.Is(err, cause) {
				t.Fatalf("save error = %v", err)
			}
			records := saveRecords(t, v.dir)
			if len(records) != 1 || records[0].Ended.IsZero() || records[0].Succeeded {
				t.Fatalf("failed save record = %+v", records)
			}
			release, err := m.TryCleanupRead(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			l := cleanup.Listing{Machines: []cleanup.Machine{{ID: web.ID}}}
			if err := m.CheckSaves(t.Context(), l, false, func(string, ...any) {}); !errors.Is(err, machines.ErrSaveUnsettled) {
				t.Fatalf("running listing settled a failed save: %v", err)
			}
			l.Envs = []cleanup.Env{{ID: "slot", Machine: web.ID}}
			l.Builds = []cleanup.Build{{
				ID: "pause", Status: "ready", Created: records[0].Ended.Add(time.Minute),
				In: []cleanup.Assignment{{Env: "slot"}},
			}}
			if err := m.CheckSaves(t.Context(), l, false, func(string, ...any) {}); err != nil {
				t.Fatalf("later pause did not settle failed save: %v", err)
			}
		})
	}
}

func TestLegacyAnsweredRecordStaysUnsuccessful(t *testing.T) {
	v := newEnv(t)
	web := v.create(t, "web")
	dir := filepath.Join(v.dir, "saves")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, web.ID+"-"+strings.Repeat("a", 32))
	data := []byte(`{"machine":"` + web.ID + `","name":"web","started":"2026-10-05T10:00:00Z","ended":"2026-10-05T10:01:00Z","answered":true}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := machines.New(machines.Config{StateDir: v.dir, BootID: func() string { return "" }})
	release, err := m.TryCleanupRead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	l := cleanup.Listing{Machines: []cleanup.Machine{{ID: web.ID}}}
	err = m.CheckSaves(t.Context(), l, false, func(string, ...any) {})
	if !errors.Is(err, machines.ErrSaveUnsettled) || strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("legacy record did not decode as an unsuccessful save: %v", err)
	}
}

func TestSaveRecordWriteFailuresPreventUnsafeProgress(t *testing.T) {
	for _, duringCall := range []bool{false, true} {
		t.Run(map[bool]string{false: "before dispatch", true: "after answer"}[duringCall], func(t *testing.T) {
			v := newEnv(t)
			v.create(t, "web")
			if err := v.m.Pause(t.Context(), "web"); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			path := filepath.Join(v.dir, "saves")
			if !duringCall {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			e := &faultEngine{Engine: v.e}
			e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
				if !duringCall {
					t.Fatal("dispatched without a durable record")
				}
				if err := os.Rename(path, filepath.Join(v.dir, "saved-records")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return engine.TemplateInfo{ID: "saved", Alias: alias}, nil
			}
			if duringCall {
				e.pause = func(context.Context, string) error {
					t.Fatal("re-paused before recording the answer")
					return nil
				}
			}
			m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
			if _, err := m.SaveImage(t.Context(), "web", "myimg"); err == nil {
				t.Fatal("ignored save-record write failure")
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatalf("followed saves symlink: %v, %v", entries, err)
			}
		})
	}
}

func TestSaveCallBackstopEndsWithUnsuccessfulRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.create(t, "web")
		started := time.Now()
		e := &faultEngine{Engine: v.e}
		e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
			if _, err := v.e.SaveTemplate(ctx, id, alias); err != nil {
				t.Fatal(err)
			}
			<-ctx.Done()
			return engine.TemplateInfo{}, errors.Join(engine.ErrNoAnswer, ctx.Err())
		}
		m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
		if _, err := m.SaveImage(t.Context(), "web", "myimg"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("backstop result = %v", err)
		}
		records := saveRecords(t, v.dir)
		if len(records) != 1 || records[0].Succeeded || !records[0].Ended.Equal(started.Add(10*time.Minute)) {
			t.Fatalf("backstop record = %+v", records)
		}
	})
}
