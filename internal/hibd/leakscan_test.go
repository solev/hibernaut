package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/leakscan/leakscantest"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/vault"
)

const (
	leakBuildA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	leakBuildB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func TestLeakscanArgErrorsAndRootCheck(t *testing.T) {
	for name, args := range map[string][]string{
		"no target":         {"leakscan"},
		"machine and image": {"leakscan", "-image", "dev", "app"},
		"canary and image":  {"leakscan", "-canary", "-image", "dev"},
		"two machines":      {"leakscan", "app", "other"},
		"bad image":         {"leakscan", "-image", "bad;name"},
	} {
		t.Run(name, func(t *testing.T) {
			a, _, errOut, _ := newLeakscanApp(t, fake.New())
			if code := a.Run(t.Context(), args); code != 2 {
				t.Fatalf("code = %d, want 2; stderr %q", code, errOut.String())
			}
		})
	}

	a, _, errOut, _ := newLeakscanApp(t, fake.New())
	a.EUID = func() int { return 501 }
	if code := a.Run(t.Context(), []string{"leakscan", "app"}); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "sudo hibd leakscan") {
		t.Fatalf("stderr = %q, want sudo hint", errOut.String())
	}
}

func TestLeakscanNeverCertifiesAReadyBuildBeforeANewerFailedBuild(t *testing.T) {
	for _, args := range [][]string{
		{"leakscan", "-json", "app"},
		{"leakscan", "-json", "-image", "dev"},
		{"leakscan", "-json", "-canary"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := fake.New(fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
				return engine.ExecResult{}, nil
			}))
			e.Add(engine.Machine{ID: "sandbox123", State: engine.Running, Metadata: map[string]string{"hib.name": "app"}})
			a, out, errOut, cfg := newLeakscanApp(t, e)
			writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
			writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("old clean memory"), []byte("old clean disk"))
			writeLeakBuild(t, cfg.e2bStorage, leakBuildB, []byte("new clean memory"), []byte("new clean disk"))
			a.Query = func(_ context.Context, sql string) ([][]string, error) {
				if strings.HasPrefix(sql, "SELECT status_group") {
					return [][]string{{"failed"}}, nil
				}
				if strings.Contains(sql, "b.status_group = 'ready'") {
					return [][]string{{leakBuildA}}, nil
				}
				return [][]string{{leakBuildB}}, nil
			}
			if code := a.Run(t.Context(), args); code != 1 {
				t.Fatalf("code = %d, want failure, stdout %q stderr %q", code, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), "build "+leakBuildB+" failed") {
				t.Fatalf("stdout = %q, want newest failed build error", out.String())
			}
		})
	}
}

func TestLeakscanPendingNewestBuildCannotReturnACleanVerdict(t *testing.T) {
	for _, status := range []string{"pending", "in_progress", "ready"} {
		for _, args := range [][]string{
			{"leakscan", "-json", "app"},
			{"leakscan", "-json", "-image", "dev"},
			{"leakscan", "-json", "-canary"},
		} {
			t.Run(status+"/"+strings.Join(args, " "), func(t *testing.T) {
				e := fake.New(fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
					return engine.ExecResult{}, nil
				}))
				e.Add(engine.Machine{ID: "sandbox123", State: engine.Running, Metadata: map[string]string{"hib.name": "app"}})
				a, out, errOut, cfg := newLeakscanApp(t, e)
				writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
				writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("old clean memory"), []byte("old clean disk"))
				a.Query = func(_ context.Context, sql string) ([][]string, error) {
					if strings.HasPrefix(sql, "SELECT status_group") {
						return [][]string{{status}}, nil
					}
					if strings.Contains(sql, "b.status_group = 'ready'") {
						return [][]string{{leakBuildA}}, nil
					}
					return [][]string{{leakBuildB}}, nil
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
				defer cancel()
				if code := a.Run(ctx, args); code != 1 {
					t.Fatalf("code = %d, want failure, stdout %q stderr %q", code, out.String(), errOut.String())
				}
				if !strings.Contains(out.String(), leakBuildB) || !strings.Contains(out.String(), "deadline exceeded") {
					t.Fatalf("stdout = %q, want selected build's deadline error", out.String())
				}
			})
		}
	}
}

func TestLeakscanMachinePausesRunningMachineAndScansVault(t *testing.T) {
	value := "secret-value-123456789"
	e := fake.New()
	e.Add(engine.Machine{ID: "sandbox123", State: engine.Running, Metadata: map[string]string{"hib.name": "app", "hib.image": "dev"}})
	a, out, errOut, cfg := newLeakscanApp(t, e)
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("mem "+value), []byte("clean disk"))
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", value)
	a.Query = func(_ context.Context, sql string) ([][]string, error) {
		if strings.HasPrefix(sql, "SELECT status_group") {
			return [][]string{{"ready"}}, nil
		}
		if !strings.Contains(sql, "sandbox123") || !strings.Contains(sql, "snapshots") {
			t.Fatalf("query SQL = %q, want snapshot lookup by sandbox ID", sql)
		}
		return [][]string{{leakBuildA}}, nil
	}

	code := a.Run(t.Context(), []string{"leakscan", "app"})
	if code != 1 {
		t.Fatalf("code = %d, want hits exit 1; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	got, err := e.Get(t.Context(), "sandbox123")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != engine.Paused {
		t.Fatalf("state = %s, want paused", got.State)
	}
	if stdout := out.String(); !strings.Contains(stdout, "github raw memory 4") || strings.Contains(stdout, value) || strings.Contains(errOut.String(), value) {
		t.Fatalf("stdout/stderr leaked or missed hit:\nstdout=%q\nstderr=%q", stdout, errOut.String())
	}
}

// Scanning a task machine pauses it by hand, so it asks the task rules first and refuses one its
// run holds, or one tasks.db can't say about.
func TestLeakscanMachineRefusesATaskMachine(t *testing.T) {
	const tid = "t-abcdefgh"
	e := fake.New()
	e.Add(engine.Machine{ID: "sandbox123", State: engine.Running,
		Metadata: map[string]string{"hib.name": tid, "hib.image": "dev", task.Label: tid}})
	a, out, errOut, cfg := newLeakscanApp(t, e)
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), []byte("clean disk"))
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
	a.Query = oneBuildQuery(leakBuildA)

	code := a.Run(t.Context(), []string{"leakscan", tid})
	if want := "refused to pause task machine " + tid; code == 0 ||
		!strings.Contains(errOut.String(), want) {
		t.Fatalf("code = %d, stdout %q, stderr %q; want a failure with %q", code, out, errOut, want)
	}
	if got, err := e.Get(t.Context(), "sandbox123"); err != nil || got.State != engine.Running {
		t.Fatalf("task machine after leakscan = %+v, %v; want it running", got, err)
	}
}

func TestLeakscanMachineDoesNotPauseAlreadyPausedMachine(t *testing.T) {
	paused := 0
	e := fake.New(fake.WithPauseHook(func(context.Context, string) error {
		paused++
		return nil
	}))
	e.Add(engine.Machine{ID: "sandbox123", State: engine.Paused, Metadata: map[string]string{"hib.name": "app"}})
	a, out, errOut, cfg := newLeakscanApp(t, e)
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), []byte("clean disk"))
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
	a.Query = oneBuildQuery(leakBuildA)

	if code := a.Run(t.Context(), []string{"leakscan", "app"}); code != 0 {
		t.Fatalf("code = %d, want clean exit 0; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	if paused != 0 {
		t.Fatalf("paused %d times, want none", paused)
	}
	if !strings.Contains(out.String(), "clean: machine app build "+leakBuildA+" scanned 1 secrets") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestLeakscanImageScansTemplateBuild(t *testing.T) {
	value := "secret-value-123456789"
	a, out, errOut, cfg := newLeakscanApp(t, fake.New())
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), []byte("disk "+value))
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", value)
	a.Query = func(_ context.Context, sql string) ([][]string, error) {
		if strings.HasPrefix(sql, "SELECT status_group") {
			return [][]string{{"ready"}}, nil
		}
		if !strings.Contains(sql, "env_aliases") || !strings.Contains(sql, images.Template("dev")) {
			t.Fatalf("query SQL = %q, want template lookup", sql)
		}
		return [][]string{{leakBuildA}}, nil
	}

	if code := a.Run(t.Context(), []string{"leakscan", "-image", "dev"}); code != 1 {
		t.Fatalf("code = %d, want hits exit 1; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "github raw disk 5") || strings.Contains(out.String(), value) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestLeakscanJSONCleanAndHitsNeverPrintValues(t *testing.T) {
	for name, tc := range map[string]struct {
		disk     []byte
		wantHits int
		wantCode int
	}{
		"clean": {disk: []byte("clean disk"), wantCode: 0},
		"hit":   {disk: []byte("disk secret-value-123456789"), wantHits: 2, wantCode: 1},
	} {
		t.Run(name, func(t *testing.T) {
			value := "secret-value-123456789"
			a, out, errOut, cfg := newLeakscanApp(t, fake.New())
			writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), tc.disk)
			writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", value)
			a.Query = oneBuildQuery(leakBuildA)

			code := a.Run(t.Context(), []string{"leakscan", "-json", "-image", "dev"})
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d; stdout %q stderr %q", code, tc.wantCode, out.String(), errOut.String())
			}
			var r hibapi.Response
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if r.LeakScan == nil || r.LeakScan.Target.Kind != "image" || r.LeakScan.Target.Name != "dev" || r.LeakScan.BuildID != leakBuildA || r.LeakScan.SecretCount != 1 || len(r.LeakScan.Hits) != tc.wantHits {
				t.Fatalf("leakScan = %+v", r.LeakScan)
			}
			if bytes.Contains(out.Bytes(), []byte(value)) || strings.Contains(errOut.String(), value) {
				t.Fatalf("output leaked value:\nstdout=%q\nstderr=%q", out.String(), errOut.String())
			}
		})
	}
}

func TestLeakscanMissingVaultScansZeroSecretsAndDoesNotTouchState(t *testing.T) {
	a, out, errOut, cfg := newLeakscanApp(t, fake.New())
	before := dirEntries(t, cfg.stateDir)
	if code := a.Run(t.Context(), []string{"leakscan", "-image", "dev"}); code != 0 {
		t.Fatalf("code = %d, want 0; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "no vault secrets") {
		t.Fatalf("stdout = %q", out.String())
	}
	if after := dirEntries(t, cfg.stateDir); !slices.Equal(before, after) {
		t.Fatalf("state dir changed: before %v after %v", before, after)
	}
}

func TestLeakscanCanaryRequiresMemoryAndDiskAndAlwaysKills(t *testing.T) {
	for name, tc := range map[string]struct {
		mem      bool
		disk     bool
		wantCode int
	}{
		"found":          {mem: true, disk: true, wantCode: 0},
		"missing memory": {disk: true, wantCode: 1},
		"missing disk":   {mem: true, wantCode: 1},
	} {
		t.Run(name, func(t *testing.T) {
			e := fake.New(fake.WithTemplates(images.Template(images.Default)))
			a, out, errOut, _ := newLeakscanApp(t, e)
			a.NewCanary = func() (string, error) { return "hibcanary_0123456789abcdef0123456789abcdef", nil }
			e = fake.New(
				fake.WithTemplates(images.Template(images.Default)),
				fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
					return engine.ExecResult{}, nil
				}),
				fake.WithPauseHook(func(_ context.Context, id string) error {
					var memData, diskData []byte
					if tc.mem {
						memData = []byte("mem " + "hibcanary_0123456789abcdef0123456789abcdef")
					}
					if tc.disk {
						diskData = []byte("disk " + "hibcanary_0123456789abcdef0123456789abcdef")
					}
					cfg, err := loadConfig(a.Getenv)
					if err != nil {
						t.Fatal(err)
					}
					writeLeakBuild(t, cfg.e2bStorage, leakBuildB, memData, diskData)
					return nil
				}),
			)
			a.NewEngine = func() (engine.Engine, error) { return e, nil }
			a.Query = oneBuildQuery(leakBuildB)

			code := a.Run(t.Context(), []string{"leakscan", "-canary"})
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d; stdout %q stderr %q", code, tc.wantCode, out.String(), errOut.String())
			}
			ms, err := e.List(t.Context(), engine.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(ms) != 0 {
				t.Fatalf("canary machine was not killed: %+v", ms)
			}
		})
	}
}

func TestLeakscanCanaryKillsWhenPlantingFails(t *testing.T) {
	e := fake.New(
		fake.WithTemplates(images.Template(images.Default)),
		fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
			return engine.ExecResult{ExitCode: 42, Stderr: []byte("nope")}, nil
		}),
	)
	a, out, errOut, _ := newLeakscanApp(t, e)
	a.NewCanary = func() (string, error) { return "hibcanary_0123456789abcdef0123456789abcdef", nil }

	if code := a.Run(t.Context(), []string{"leakscan", "-canary"}); code != 1 {
		t.Fatalf("code = %d, want 1; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	ms, err := e.List(t.Context(), engine.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 {
		t.Fatalf("canary machine was not killed: %+v", ms)
	}
}

func TestLeakscanCanaryKillIgnoresCanceledRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	e := fake.New(
		fake.WithTemplates(images.Template(images.Default)),
		fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
			cancel()
			return engine.ExecResult{ExitCode: 42, Stderr: []byte("interrupted")}, nil
		}),
	)
	a, out, errOut, _ := newLeakscanApp(t, e)
	a.NewCanary = func() (string, error) { return "hibcanary_0123456789abcdef0123456789abcdef", nil }

	if code := a.Run(ctx, []string{"leakscan", "-canary"}); code != 1 {
		t.Fatalf("code = %d, want 1; stdout %q stderr %q", code, out.String(), errOut.String())
	}
	ms, err := e.List(t.Context(), engine.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 {
		t.Fatalf("canary machine was not killed: %+v", ms)
	}
}

func newLeakscanApp(t *testing.T, e engine.Engine) (*App, *bytes.Buffer, *bytes.Buffer, config) {
	t.Helper()
	state := t.TempDir()
	storage := t.TempDir()
	vaultDir := t.TempDir()
	keys := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(keys, sshAuthorizedKey(t), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HIBD_CONFIG":          filepath.Join(t.TempDir(), "none.env"),
		"E2B_API_KEY":          "test",
		"HIBD_STATE_DIR":       state,
		"HIBD_AUTHORIZED_KEYS": keys,
		"HIBD_VAULT":           filepath.Join(vaultDir, "vault.json"),
		"HIBD_VAULT_KEY":       filepath.Join(vaultDir, "vault.key"),
		"HIBD_E2B_STORAGE":     storage,
		"HIBD_LINK_PORT":       "0",
	}
	var out, errOut bytes.Buffer
	a := &App{
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &errOut,
		Getenv:    func(k string) string { return env[k] },
		NewEngine: func() (engine.Engine, error) { return e, nil },
		ReadRoom:  func() (room.Host, error) { return roomy, nil },
		EUID:      func() int { return 0 },
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return a, &out, &errOut, cfg
}

func sshAuthorizedKey(t *testing.T) []byte {
	t.Helper()
	return ssh.MarshalAuthorizedKey(newKey(t))
}

func writeVault(t *testing.T, vaultPath, keyPath, name, value string) {
	t.Helper()
	if err := vault.Update(vaultPath, keyPath, func(f *vault.File, key vault.Key) error {
		_, err := f.Put(key, name, strings.ToUpper(name)+"_TOKEN", []string{"example.com"}, value)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func writeLeakBuild(t *testing.T, storage, build string, mem, disk []byte) {
	t.Helper()
	leakscantest.WriteBuild(t, storage, build, mem, disk)
}

func oneBuildQuery(build string) func(context.Context, string) ([][]string, error) {
	return func(_ context.Context, sql string) ([][]string, error) {
		if strings.HasPrefix(sql, "SELECT status_group") {
			return [][]string{{"ready"}}, nil
		}
		return [][]string{{build}}, nil
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func TestLeakscanLooksForRetiredCodexValuesUnderTheirNames(t *testing.T) {
	a, out, errOut, cfg := newLeakscanApp(t, fake.New())
	store := a.codexStore(cfg, nil)
	old := testCodexTokens()
	if _, err := store.CommitLogin(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	current := testCodexTokens()
	if _, err := store.CommitLogin(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-123456789")
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("mem "+old.RefreshToken), []byte("disk "+old.AccessToken))
	a.Query = oneBuildQuery(leakBuildA)

	code := a.Run(t.Context(), []string{"leakscan", "-json", "-image", "dev"})
	if code != 1 {
		t.Fatalf("code = %d, want hits exit 1; stderr %q", code, errOut)
	}
	wantNoCodexToken(t, old, out.Bytes(), errOut.Bytes())
	wantNoCodexToken(t, current, out.Bytes(), errOut.Bytes())
	var r hibapi.Response
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"codex-access", "codex-id", "codex-refresh", "github"}
	if r.LeakScan == nil || !slices.Equal(r.LeakScan.Secrets, wantNames) || r.LeakScan.SecretCount != 7 {
		t.Fatalf("leakScan = %+v, want secrets %q and 7 values: 4 in the vault and 3 retired", r.LeakScan, wantNames)
	}
	var hit []string
	for _, h := range r.LeakScan.Hits {
		if h.Where == "memory" || h.Where == "disk" {
			hit = append(hit, h.Secret+" "+h.Where)
		}
	}
	slices.Sort(hit)
	if want := []string{"codex-access disk", "codex-refresh memory"}; !slices.Equal(hit, want) {
		t.Errorf("hits = %q, want %q", hit, want)
	}

	out.Reset()
	if code := a.Run(t.Context(), []string{"leakscan", "-image", "dev"}); code != 1 {
		t.Fatalf("code = %d, want hits exit 1", code)
	}
	wantNoCodexToken(t, old, out.Bytes(), errOut.Bytes())
	if !strings.Contains(out.String(), "codex-refresh raw memory 4") {
		t.Errorf("stdout = %q, want the retired refresh token's hit under codex-refresh", out)
	}
}

func TestLeakscanListsTheNamesItCovered(t *testing.T) {
	a, out, errOut, cfg := newLeakscanApp(t, fake.New())
	writeLeakBuild(t, cfg.e2bStorage, leakBuildA, []byte("clean memory"), []byte("clean disk"))
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "openai", "secret-value-123456789")
	writeVault(t, cfg.vaultPath, cfg.vaultKeyPath, "github", "secret-value-987654321")
	a.Query = oneBuildQuery(leakBuildA)
	if code := a.Run(t.Context(), []string{"leakscan", "-json", "-image", "dev"}); code != 0 {
		t.Fatalf("code = %d, want clean exit 0; stderr %q", code, errOut)
	}
	var r hibapi.Response
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.LeakScan == nil || !slices.Equal(r.LeakScan.Secrets, []string{"github", "openai"}) || r.LeakScan.SecretCount != 2 {
		t.Errorf("leakScan = %+v, want secrets github and openai", r.LeakScan)
	}
}
