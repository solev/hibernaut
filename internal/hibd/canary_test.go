package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/vault"
)

func TestLeakscanCanaryDoesNotReconcileAnActiveRun(t *testing.T) {
	const canary = "hibcanary_0123456789abcdef0123456789abcdef"
	entered := make(chan struct{})
	release := make(chan struct{})
	e := fake.New(fake.WithExec(func(ctx context.Context, _ engine.Machine, _ string, _ engine.ExecRequest) (engine.ExecResult, error) {
		select {
		case <-entered:
			return engine.ExecResult{}, nil
		default:
			close(entered)
		}
		select {
		case <-release:
			return engine.ExecResult{}, nil
		case <-ctx.Done():
			return engine.ExecResult{}, ctx.Err()
		}
	}))
	a, out, errOut, cfg := newLeakscanApp(t, e)
	a.NewCanary = func() (string, error) { return canary, nil }
	a.Query = oneBuildQuery(leakBuildB)
	writeLeakBuild(t, cfg.e2bStorage, leakBuildB, []byte("mem "+canary), []byte("disk "+canary))
	done := make(chan int, 1)
	go func() { done <- a.Run(t.Context(), []string{"leakscan", "-canary"}) }()
	released := false
	defer func() {
		if !released {
			close(release)
			<-done
		}
	}()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("first canary did not start")
	}
	second := *a
	var secondOut, secondErr bytes.Buffer
	second.Stdout = &secondOut
	second.Stderr = &secondErr
	if code := second.Run(t.Context(), []string{"leakscan", "-canary"}); code != 1 ||
		!strings.Contains(secondErr.String(), "another canary scan is running") {
		t.Fatalf("second run = %d, stdout %q stderr %q, want active-run error", code, secondOut.String(), secondErr.String())
	}
	close(release)
	released = true
	if code := <-done; code != 0 || !strings.Contains(out.String(), "canary found in memory and disk") {
		t.Fatalf("first run = %d, stdout %q stderr %q, want successful canary", code, out.String(), errOut.String())
	}
}

func TestLeakscanCanaryReconcilesCrashedRunsAndPreservesUserState(t *testing.T) {
	const canary = "hibcanary_0123456789abcdef0123456789abcdef"
	var cfg config
	e := fake.New(
		fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
			return engine.ExecResult{}, nil
		}),
		fake.WithPauseHook(func(context.Context, string) error {
			writeLeakBuild(t, cfg.e2bStorage, leakBuildB, []byte("mem "+canary), []byte("disk "+canary))
			return nil
		}),
	)
	for _, m := range []engine.Machine{
		{ID: "stale-running", State: engine.Running, Metadata: map[string]string{"hib.name": "leakscan-canary-aaaaaaaaaaaa"}},
		{ID: "stale-paused", State: engine.Paused, Metadata: map[string]string{"hib.name": "leakscan-canary-bbbbbbbbbbbb"}},
		{ID: "user-machine", State: engine.Running, Metadata: map[string]string{"hib.name": "test"}},
		{ID: "user-prefix", State: engine.Paused, Metadata: map[string]string{"hib.name": "leakscan-canary-not-a-canary"}},
	} {
		e.Add(m)
	}
	a, out, errOut, configured := newLeakscanApp(t, e)
	cfg = configured
	a.NewCanary = func() (string, error) { return canary, nil }
	a.Query = oneBuildQuery(leakBuildB)
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "user-secret-123456789")
	if err := vault.Update(cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, key vault.Key) error {
		_, err := f.Put(key, "leakscan-canary-aaaaaaaaaaaa", "HIB_CANARY", []string{"example.com"}, "old-canary-value-123456789")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out.Reset()
		errOut.Reset()
		if code := a.Run(t.Context(), []string{"leakscan", "-json", "-canary"}); code != 0 {
			t.Fatalf("code = %d, stdout %q stderr %q", code, out.String(), errOut.String())
		}
		var response hibapi.Response
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.LeakScan == nil || !response.LeakScan.MemoryFound || !response.LeakScan.DiskFound ||
			response.LeakScan.BuildID != leakBuildB {
			t.Fatalf("canary result = %+v, want latest memory and disk hits", response.LeakScan)
		}
		ms, err := e.List(t.Context(), engine.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, m := range ms {
			names = append(names, machines.Name(m))
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"leakscan-canary-not-a-canary", "test"}) {
			t.Fatalf("machines = %v, want only the user machines", names)
		}
		f, err := vault.Load(cfg.vaultPath)
		if err != nil {
			t.Fatal(err)
		}
		secrets := f.Secrets()
		if len(secrets) != 1 || secrets[0].Name != "github" {
			t.Fatalf("secrets = %+v, want only the user secret", secrets)
		}
	}
}
