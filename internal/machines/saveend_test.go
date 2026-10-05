package machines_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSaveEndFailureDoesNotChooseTheSaveOutcome(t *testing.T) {
	saveFailure := errors.New("E2B returned HTTP 500")
	pauseFailure := errors.New("re-pause failed")
	for _, tc := range []struct {
		name      string
		saveErr   error
		pauseErr  error
		keepImage bool
	}{
		{"failed save still restores and cleans alias", saveFailure, nil, false},
		{"successful save keeps image and re-pauses", nil, nil, true},
		{"re-pause failure keeps its existing cleanup path", nil, pauseFailure, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
			v := newEnvAt(t, func() time.Time { return at })
			web := v.create(t, "web")
			if err := v.m.Pause(t.Context(), "web"); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			e := &faultEngine{Engine: v.e}
			e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
				info, err := v.e.SaveTemplate(ctx, id, alias)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(v.dir, "saves")
				if err := os.Rename(path, filepath.Join(v.dir, "save-records")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				at = at.Add(time.Minute)
				return info, tc.saveErr
			}
			restored := false
			e.pause = func(ctx context.Context, id string) error {
				restored = true
				if tc.pauseErr != nil {
					return tc.pauseErr
				}
				return v.e.Pause(ctx, id)
			}
			m := imageManager(v, func(cfg *machines.Config) {
				cfg.Engine, cfg.Now = e, func() time.Time { return at }
			})
			img, err := m.SaveImage(t.Context(), "web", "myimg")
			if err == nil {
				t.Fatal("record failure was ignored")
			}
			if !strings.Contains(err.Error(), "recording the save's end failed") || !strings.Contains(err.Error(), "2026-10-05T12:10:00Z") {
				t.Errorf("record failure = %v", err)
			}
			if tc.saveErr != nil && !errors.Is(err, tc.saveErr) || tc.pauseErr != nil && !errors.Is(err, tc.pauseErr) {
				t.Fatalf("lost the engine error: %v", err)
			}
			if !strings.Contains(err.Error(), "not a directory") || !restored {
				t.Errorf("lost the record failure or skipped restoration: %v, restored %v", err, restored)
			}
			_, aliasErr := v.e.Template(t.Context(), images.SavedTemplate("myimg"))
			if tc.keepImage {
				if img.Name != "myimg" || aliasErr != nil || !strings.Contains(err.Error(), "saved "+images.SavedTemplate("myimg")) {
					t.Errorf("successful image was lost: %+v, alias %v, result %v", img, aliasErr, err)
				}
			} else if img.Name != "" || !errors.Is(aliasErr, engine.ErrNoTemplate) {
				t.Errorf("failed-save alias cleanup changed: %+v, %v", img, aliasErr)
			}
			if tc.pauseErr == nil && v.get(t, web.ID).State != engine.Paused {
				t.Error("paused origin was not restored")
			}
			blocked, _ := checkSaveFixture(t, m, cleanup.Listing{}, false)
			if !errors.Is(blocked, machines.ErrSaveUnsettled) {
				t.Fatalf("record failure did not inhibit cleanup: %v", blocked)
			}
		})
	}
}
