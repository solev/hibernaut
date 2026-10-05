package hibd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestTrimConfigReadsFileAndCleanupLoadsConfig(t *testing.T) {
	a, _, errOut := newApp(t, fake.New())
	path := filepath.Join(t.TempDir(), "hibd.env")
	if err := os.WriteFile(path, []byte("HIBD_TRIM=\"all\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := a.Getenv
	override := ""
	a.Getenv = func(k string) string {
		switch k {
		case "HIBD_CONFIG":
			return path
		case "HIBD_TRIM":
			return override
		}
		return getenv(k)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil || cfg.trimScope != "all" {
		t.Fatalf("file trim scope = %q, %v", cfg.trimScope, err)
	}
	override = "off"
	cfg, err = loadConfig(a.Getenv)
	if err != nil || cfg.trimScope != "off" {
		t.Fatalf("environment trim scope = %q, %v", cfg.trimScope, err)
	}
	if err := os.WriteFile(path, []byte("not an env file"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Clean = func(context.Context, engine.Engine, time.Time, bool, func(string, ...any)) error {
		t.Error("cleanup ran before loading its config")
		return nil
	}
	if code := a.Run(t.Context(), []string{"cleanup"}); code != 1 || !strings.Contains(errOut.String(), "want KEY=VALUE") {
		t.Fatalf("cleanup exit %d, %s", code, errOut)
	}
}

func TestTrimConfigDefaultsAndAcceptsUnknownValues(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	for _, value := range []string{"", "off", "e2e", "all", "bad"} {
		getenv := a.Getenv
		cfg, err := loadConfig(func(k string) string {
			if k == "HIBD_TRIM" {
				return value
			}
			return getenv(k)
		})
		want := value
		if want == "" {
			want = "e2e"
		}
		if err != nil || cfg.trimScope != want {
			t.Fatalf("%q: config = %+v, %v", value, cfg, err)
		}
	}
	if code := a.Run(t.Context(), []string{"help"}); code != 0 || !strings.Contains(out.String(), "HIBD_TRIM") {
		t.Fatalf("help omitted HIBD_TRIM: %s", out)
	}
}

func TestCleanupWaitsOnceForLockBeforeReading(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	release, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), "cleanup.lock", "cleanup")
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	waited, cleaned := 0, 0
	a.WaitCleanup = func(ctx context.Context) error {
		waited++
		if cleaned != 0 {
			t.Error("read E2B while another cleanup held its lock")
		}
		if waited == 2 {
			release()
			release = nil
		}
		return ctx.Err()
	}
	a.Clean = func(ctx context.Context, _ engine.Engine, _ time.Time, _ bool, _ func(string, ...any)) error {
		cleaned++
		if _, bounded := ctx.Deadline(); bounded {
			t.Error("the cleanup command had a budget")
		}
		if unlock, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), "cleanup.lock", "cleanup"); err != nil || ok {
			if unlock != nil {
				unlock()
			}
			t.Errorf("cleanup didn't hold its lock: %v, %v", ok, err)
		}
		return nil
	}
	if code := a.Run(t.Context(), []string{"cleanup"}); code != 0 || cleaned != 1 || waited != 2 {
		t.Fatalf("exit %d, waited %d, cleaned %d: %s", code, waited, cleaned, errOut)
	}
	if strings.Count(out.String(), "waiting for another cleanup") != 1 {
		t.Fatal(out)
	}
}

func TestCleanupLockWaitRespectsCancellation(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	release, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), "cleanup.lock", "cleanup")
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a.WaitCleanup = func(context.Context) error { cancel(); return ctx.Err() }
	a.Clean = func(context.Context, engine.Engine, time.Time, bool, func(string, ...any)) error {
		t.Error("ran while locked")
		return nil
	}
	if code := a.Run(ctx, []string{"cleanup"}); code != 1 || !strings.Contains(errOut.String(), "context canceled") || !strings.Contains(out.String(), "waiting for another cleanup") {
		t.Fatalf("exit %d, %s, %s", code, out, errOut)
	}
}
