package hibd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestCleanupPrunesTrafficTokens(t *testing.T) {
	for _, mode := range []string{"cleanup", "sweep", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			fe := fake.New()
			fe.Add(engine.Machine{ID: "listed", State: engine.Running})
			a, cfg := cleanupApp(t, fe, &fakeLinks{}, false)
			e := &getEngine{Engine: fe, unlisted: map[string]engine.Machine{
				"moving": {ID: "moving", State: engine.Paused},
			}}
			for _, name := range []string{"traffic-token-listed", "traffic-token-forgotten", "traffic-token-moving",
				"traffic-token-", "traffic-token-BAD", "unrelated"} {
				if err := os.WriteFile(filepath.Join(cfg.stateDir, name), []byte("token"), 0o600); err != nil {
					t.Fatal(err)
				}

			}
			if err := os.Symlink("unrelated", filepath.Join(cfg.stateDir, "traffic-token-symlink")); err != nil {
				t.Fatal(err)
			}
			var logs []string
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := a.clean(ctx, ctx, cfg, e, time.Now(), mode == "dry-run", mode == "sweep", func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(cfg.stateDir, "traffic-token-forgotten"))
			if mode == "dry-run" {
				if err != nil {
					t.Fatalf("dry-run removed forgotten token: %v", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("forgotten traffic token remains: %v", err)
			}
			for _, name := range []string{"traffic-token-listed", "traffic-token-moving", "traffic-token-",
				"traffic-token-BAD", "traffic-token-symlink", "unrelated"} {
				if _, err := os.Lstat(filepath.Join(cfg.stateDir, name)); err != nil {
					t.Errorf("cleanup lost %s: %v", name, err)
				}
			}
			verb := "removed"
			if mode == "dry-run" {
				verb = "would remove"
			}
			if text := strings.Join(logs, "\n"); !strings.Contains(text,
				verb+" the traffic token of forgotten, which E2B has forgotten") {
				t.Errorf("cleanup did not report forgotten token: %q", text)
			}
			if got := slices.Sorted(slices.Values(e.gets)); !slices.Equal(got, []string{"forgotten", "moving"}) {
				t.Errorf("Get asked about %q, want forgotten and moving once each", got)
			}
		})
	}
}

func TestCleanupKeepsUnconfirmedTrafficToken(t *testing.T) {
	fe := fake.New()
	a, cfg := cleanupApp(t, fe, &fakeLinks{}, false)
	unavailable := errors.New("E2B unavailable")
	e := &getEngine{Engine: fe, getErr: map[string]error{"unknown": unavailable}}
	path := filepath.Join(cfg.stateDir, "traffic-token-unknown")
	if err := os.WriteFile(path, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := a.clean(ctx, ctx, cfg, e, time.Now(), false, false, t.Logf)
	if !errors.Is(err, unavailable) || !strings.Contains(err.Error(), "kept the traffic token of unknown") {
		t.Fatalf("cleanup = %v, want explicit retained-token error", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "token" {
		t.Fatalf("unconfirmed token = %q, %v, want preserved", data, err)
	}
}
