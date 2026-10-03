package proxy_test

import (
	"bytes"
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
