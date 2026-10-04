package proxy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/proxy"
	"github.com/solev/hibernaut/internal/vault"
)

func TestVaultReload(t *testing.T) {
	dir := t.TempDir()
	path, keyPath := filepath.Join(dir, "vault.json"), filepath.Join(dir, "key")
	var logs bytes.Buffer
	now := time.Unix(0, 0)
	source := &proxy.VaultSource{
		Path: path, KeyPath: keyPath, Now: func() time.Time { return now },
		Logf: func(format string, args ...any) { fmt.Fprintf(&logs, format+"\n", args...) },
	}
	if source.Swapper() != nil {
		t.Fatal("missing vault and key must pass through")
	}
	key, err := vault.CreateKey(keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	put := func(value string) string {
		t.Helper()
		s, err := v.Put(key, "token", "TOKEN", []string{"example.com"}, value)
		if err != nil {
			t.Fatal(err)
		}
		if err := v.Save(path); err != nil {
			t.Fatal(err)
		}
		return s.Placeholder
	}
	placeholder := put("sk-never-log-original")
	check := func(want string) {
		t.Helper()
		sw := source.Swapper()
		if sw == nil {
			t.Fatal("no swapper for a valid vault")
		}
		req := httptest.NewRequest("GET", "https://example.com/", nil)
		req.Header.Set("Authorization", "Bearer "+placeholder)
		if _, err := sw.Request(req, "example.com"); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer "+want {
			t.Fatal("request did not use the current secret")
		}
	}
	check("sk-never-log-original")
	put("sk-never-log-rotatedx")
	check("sk-never-log-rotatedx")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if source.Swapper() != nil {
		t.Fatal("deleted vault kept a secret active")
	}
	put("sk-never-log-rotatedx")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if source.Swapper() != nil {
		t.Fatal("missing key kept a secret active")
	}
	replacementKey, err := vault.CreateKey(keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source.Swapper() != nil {
		t.Fatal("wrong key kept a secret active")
	}
	if lines := strings.Count(logs.String(), "\n"); lines != 1 {
		t.Fatalf("decrypt error was not logged once: %s", &logs)
	}
	key = replacementKey
	v, _ = vault.Load(filepath.Join(dir, "absent"))
	placeholder = put("sk-never-log-recovered")
	check("sk-never-log-recovered")
	if err := os.WriteFile(path, []byte(`{"v":1,"secrets":[{"name":"sk-never-log-injected"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if source.Swapper() != nil {
			t.Fatal("corrupt vault must pass through")
		}
	}
	if strings.Count(logs.String(), "\n") != 1 {
		t.Fatalf("errors were not rate limited: %s", &logs)
	}
	now = now.Add(time.Minute)
	if err := os.WriteFile(path, []byte("sk-never-log-bad-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if source.Swapper() != nil || strings.Count(logs.String(), "\n") != 2 {
		t.Fatalf("changed corrupt vault was not retried: %s", &logs)
	}
	if strings.Contains(logs.String(), "sk-never-log") {
		t.Fatal("a value reached the logs")
	}
}

func TestVaultKeyAppearsAndConcurrentReload(t *testing.T) {
	dir := t.TempDir()
	path, keyPath := filepath.Join(dir, "vault"), filepath.Join(dir, "key")
	key, err := vault.CreateKey(keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.Load(path)
	if _, err := v.Put(key, "token", "TOKEN", []string{"example.com"}, "sk-concurrent-value"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(keyPath, keyPath+".saved"); err != nil {
		t.Fatal(err)
	}
	source := &proxy.VaultSource{Path: path, KeyPath: keyPath}
	if source.Swapper() != nil {
		t.Fatal("missing key must pass through")
	}
	if err := os.Rename(keyPath+".saved", keyPath); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			sw := source.Swapper()
			if sw == nil || !sw.Bound("example.com") {
				t.Error("appearing key did not activate the vault")
			}
		})
	}
	wg.Wait()
}

func TestVaultReloadsSameSizeAndMtimeReplacement(t *testing.T) {
	dir := t.TempDir()
	path, keyPath := filepath.Join(dir, "vault"), filepath.Join(dir, "key")
	key, err := vault.CreateKey(keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.Load(path)
	s, err := v.Put(key, "token", "TOKEN", []string{"example.com"}, "sk-first-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Save(path); err != nil {
		t.Fatal(err)
	}
	source := &proxy.VaultSource{Path: path, KeyPath: keyPath}
	if source.Swapper() == nil {
		t.Fatal("initial load failed")
	}
	info, _ := os.Stat(path)
	if _, err := v.Put(key, "token", "TOKEN", []string{"example.com"}, "sk-nextx-value"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "https://example.com/", nil)
	req.Header.Set("Authorization", s.Placeholder)
	sw := source.Swapper()
	if sw == nil {
		t.Fatal("replacement load failed")
	}
	if _, err := sw.Request(req, "example.com"); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "sk-nextx-value" {
		t.Fatal("inode replacement was not detected")
	}
}

func TestVaultSourcePassesThroughAfterAFailedReload(t *testing.T) {
	cases := []struct {
		name, stage  string
		needsNonRoot bool
		retries      bool
		breakFiles   func(t *testing.T, f *reloadFixture) (fix func())
	}{
		{name: "state directory replaced by a file", stage: "stat", retries: true, breakFiles: replaceStateDir},
		{name: "vault unreadable", stage: "open vault", needsNonRoot: true, retries: true,
			breakFiles: func(t *testing.T, f *reloadFixture) func() { return chmod(t, f.path, 0, 0o644) }},
		{name: "key unreadable", stage: "open key", needsNonRoot: true, retries: true,
			breakFiles: func(t *testing.T, f *reloadFixture) func() { return chmod(t, f.keyPath, 0, 0o600) }},
		{name: "key readable by others", stage: "load key",
			breakFiles: func(t *testing.T, f *reloadFixture) func() { return chmod(t, f.keyPath, 0o644, 0o600) }},
		{name: "key truncated", stage: "load key", breakFiles: truncateKey},
		{name: "vault holds a secret twice", stage: "compile swapper", breakFiles: duplicateSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsNonRoot && os.Geteuid() == 0 {
				t.Skip("root opens files whatever their mode")
			}
			f := newReloadFixture(t)
			fix := tc.breakFiles(t, f)
			if f.swap(t) != "" {
				t.Fatal("a failed reload kept the last good swapper")
			}
			next := "until the files change"
			if tc.retries {
				next = "and trying again on the next request"
			}
			line := fmt.Sprintf("proxy: cannot load secrets (%s). Passing placeholders through %s\n", tc.stage, next)
			if got := f.logs.String(); got != line {
				t.Fatalf("logs = %q, want %q", got, line)
			}
			f.now = f.now.Add(time.Minute)
			if f.swap(t) != "" {
				t.Fatal("a broken vault or key activated secrets")
			}
			want := 1
			if tc.retries {
				want = 2
			}
			if got := strings.Count(f.logs.String(), line); got != want {
				t.Fatalf("logged the failure %d times a minute later, want %d: %q", got, want, f.logs.String())
			}
			fix()
			f.save(t, "sk-reload-fixed-value")
			if got := f.swap(t); got != "sk-reload-fixed-value" {
				t.Fatal("secrets did not come back after the files changed")
			}
			if strings.Contains(f.logs.String(), "sk-reload") || strings.Contains(f.logs.String(), "reload-token") {
				t.Fatal("a value or name reached the logs")
			}
		})
	}
}

// A failure to stat or open the files says nothing about what's in them, so the next call reads
// them again even though they didn't change.
func TestVaultSourceRecoversFromATransientFailure(t *testing.T) {
	cases := []struct {
		name         string
		needsNonRoot bool
		breakFiles   func(t *testing.T, f *reloadFixture) (fix func())
	}{
		{name: "state directory replaced by a file", breakFiles: replaceStateDir},
		{name: "vault unreadable", needsNonRoot: true,
			breakFiles: func(t *testing.T, f *reloadFixture) func() { return chmod(t, f.path, 0, 0o644) }},
		{name: "key unreadable", needsNonRoot: true,
			breakFiles: func(t *testing.T, f *reloadFixture) func() { return chmod(t, f.keyPath, 0, 0o600) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsNonRoot && os.Geteuid() == 0 {
				t.Skip("root opens files whatever their mode")
			}
			f := newReloadFixture(t)
			fix := tc.breakFiles(t, f)
			if f.swap(t) != "" {
				t.Fatal("a failed reload kept the last good swapper")
			}
			fix()
			if got := f.swap(t); got != "sk-reload-first-value" {
				t.Fatalf("secrets = %q after restoring the same files, want them back", got)
			}
		})
	}
}

func TestVaultSourcePassesThroughAnEmptyVault(t *testing.T) {
	f := newReloadFixture(t)
	if err := f.vault.Remove("reload-token"); err != nil {
		t.Fatal(err)
	}
	if err := f.vault.Save(f.path); err != nil {
		t.Fatal(err)
	}
	if f.source.Swapper() != nil {
		t.Fatal("an empty vault made a swapper")
	}
	if f.logs.Len() != 0 {
		t.Fatalf("an empty vault logged a failure: %q", f.logs.String())
	}
}

// reloadFixture is a vault and key that a VaultSource loaded once, in a directory a test can break.
type reloadFixture struct {
	path, keyPath string
	key           vault.Key
	vault         *vault.File
	placeholder   string
	source        *proxy.VaultSource
	now           time.Time
	logs          bytes.Buffer
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &reloadFixture{path: filepath.Join(dir, "vault.json"), keyPath: filepath.Join(dir, "key"), now: time.Unix(0, 0)}
	key, err := vault.CreateKey(f.keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.key, f.vault = key, new(vault.File)
	f.source = &proxy.VaultSource{
		Path: f.path, KeyPath: f.keyPath, Now: func() time.Time { return f.now },
		Logf: func(format string, args ...any) { fmt.Fprintf(&f.logs, format+"\n", args...) },
	}
	f.save(t, "sk-reload-first-value")
	if got := f.swap(t); got != "sk-reload-first-value" {
		t.Fatal("the first load did not swap the placeholder")
	}
	return f
}

func (f *reloadFixture) save(t *testing.T, value string) {
	t.Helper()
	s, err := f.vault.Put(f.key, "reload-token", "RELOAD_TOKEN", []string{"example.com"}, value)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.vault.Save(f.path); err != nil {
		t.Fatal(err)
	}
	f.placeholder = s.Placeholder
}

// swap returns what the current swapper puts in place of the placeholder, or "" when it passes
// placeholders through.
func (f *reloadFixture) swap(t *testing.T) string {
	t.Helper()
	sw := f.source.Swapper()
	if sw == nil {
		return ""
	}
	req := httptest.NewRequest("GET", "https://example.com/", nil)
	req.Header.Set("Authorization", "Bearer "+f.placeholder)
	if _, err := sw.Request(req, "example.com"); err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
}

func replaceStateDir(t *testing.T, f *reloadFixture) func() {
	t.Helper()
	dir := filepath.Dir(f.path)
	if err := os.Rename(dir, dir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(dir+".saved", dir); err != nil {
			t.Fatal(err)
		}
	}
}

func chmod(t *testing.T, path string, broken, fixed os.FileMode) func() {
	t.Helper()
	if err := os.Chmod(path, broken); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chmod(path, fixed); err != nil {
			t.Fatal(err)
		}
	}
}

func truncateKey(t *testing.T, f *reloadFixture) func() {
	t.Helper()
	key, err := os.ReadFile(f.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(f.keyPath, int64(len(key)-1)); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.WriteFile(f.keyPath, key, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// duplicateSecret writes the vault with its one entry twice. Each copy decrypts, but the proxy
// cannot swap two secrets with the same name and placeholder.
func duplicateSecret(t *testing.T, f *reloadFixture) func() {
	t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]json.RawMessage
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	var secrets []json.RawMessage
	if err := json.Unmarshal(disk["secrets"], &secrets); err != nil || len(secrets) != 1 {
		t.Fatalf("vault does not hold exactly one secret: %v", err)
	}
	if disk["secrets"], err = json.Marshal(append(secrets, secrets[0])); err != nil {
		t.Fatal(err)
	}
	if data, err = json.Marshal(disk); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return func() {}
}
