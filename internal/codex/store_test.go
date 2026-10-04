package codex_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/egress/swap"
	"github.com/solev/hibernaut/internal/vault"
)

func newStore(t *testing.T, now time.Time) (*codex.Store, codex.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := codex.Config{
		StateDir: dir, VaultPath: filepath.Join(dir, "vault.json"), KeyPath: filepath.Join(dir, "vault.key"),
		Now: func() time.Time { return now },
	}
	return codex.NewStore(cfg), cfg
}

func TestStoreCommitsAndPublishesLogin(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	if _, err := s.Load(); !errors.Is(err, codex.ErrNotConnected) {
		t.Fatal("missing profile should mean not connected")
	}
	if _, err := s.Public(); !errors.Is(err, codex.ErrNotConnected) {
		t.Fatal("missing public login should mean not connected")
	}
	initial := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
	p, err := s.CommitLogin(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if p.Email != "person@example.com" || p.Plan != "plus" || p.AccountID != "account" ||
		!p.Expires.Equal(now.Add(time.Hour)) || !p.Published.Equal(now) || p.Health != codex.HealthOK {
		t.Fatal("profile does not describe the login")
	}
	public, err := s.Public()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{initial.IDToken, initial.AccessToken, initial.RefreshToken} {
		for _, name := range []string{codex.ProfileFile, "vault.json"} {
			b, err := os.ReadFile(filepath.Join(cfg.StateDir, name))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte(value)) {
				t.Fatal("host metadata exposed a real token")
			}
		}
	}
	if public.IDToken == initial.IDToken || public.AccessToken == initial.AccessToken ||
		public.RefreshToken == initial.RefreshToken || !strings.HasPrefix(public.RefreshToken, "rt.") {
		t.Fatal("public login must hold distinct, correctly shaped placeholders")
	}
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(public.AccessToken, ".")[1])
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil || claims["exp"] != float64(4102444800) {
		t.Fatal("placeholder JWT must expire in 2100")
	}
	if claims["https://api.openai.com/auth"].(map[string]any)["chatgpt_account_id"] != "account" {
		t.Fatal("placeholder JWT lost the account claim")
	}
	key, err := vault.LoadKey(cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	f, err := vault.Load(cfg.VaultPath)
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := f.Open(key)
	if err != nil || len(revealed) != 3 {
		t.Fatal("login was not sealed in the vault")
	}
	for _, secret := range revealed {
		if secret.Env != "" {
			t.Fatal("managed Codex secrets must not have environment variables")
		}
	}

	if err := os.Chmod(cfg.KeyPath, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Public(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.KeyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	renewed := codextest.Tokens("account", "person@example.com", "plus", now.Add(time.Hour), now.Add(2*time.Hour))
	if _, err := s.CommitLogin(context.Background(), renewed); err != nil {
		t.Fatal(err)
	}
	after, err := s.Public()
	if err != nil || after != public {
		t.Fatal("same-account renewal changed compatible placeholders")
	}
	old, err := s.Retired()
	if err != nil || len(old) != 3 {
		t.Fatal("replaced tokens must remain available to the leak scan")
	}
	for _, value := range old {
		expected := map[string]string{codex.AccessName: initial.AccessToken, codex.RefreshName: initial.RefreshToken, codex.IDName: initial.IDToken}
		if value.Value != expected[value.Name] {
			t.Fatal("retired value or original name changed")
		}
	}
}

func TestStoreRetirementExpiresAndDoesNotCreateAKey(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	if old, err := s.Retired(); err != nil || len(old) != 0 {
		t.Fatal("an empty retired store should need no key")
	}

	if _, err := os.Stat(cfg.KeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reading retirement created the vault key")
	}
	first := codextest.Tokens("first", "person@example.com", "plus", now, now.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := codextest.Tokens("second", "person@example.com", "plus", now, now.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	cfg.Now = func() time.Time { return now.Add(2 * time.Hour) }
	old, err := codex.NewStore(cfg).Retired()
	if err != nil || len(old) != 1 || old[0].Name != codex.RefreshName {
		t.Fatal("expired JWTs must leave the leak scan")
	}
	cfg.Now = func() time.Time { return now.Add(31 * 24 * time.Hour) }
	old, err = codex.NewStore(cfg).Retired()
	if err != nil || len(old) != 0 {
		t.Fatal("refresh tokens must expire from retirement after 30 days")
	}
}

func TestLoadInterpretsInterruptedRefreshWithoutWriting(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
	p, err := s.CommitLogin(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	p.Attempt = &codex.Attempt{Generation: swap.Fingerprint(initial.RefreshToken), Started: now.Add(time.Minute)}
	before, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StateDir, codex.ProfileFile)
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(cfg.StateDir, codex.LockFile), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	held, err := s.Load()
	if err != nil || held.Health != codex.HealthOK || held.Attempt == nil {
		t.Fatal("a held lock must mean refresh in progress")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CommitLogin(ctx, initial); !errors.Is(err, context.Canceled) {
		t.Fatal("waiting for the Codex lock must be cancelable")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	stuck, err := s.Load()
	if err != nil || stuck.Health != codex.HealthStuck || stuck.Attempt == nil {
		t.Fatal("an interrupted unpublished attempt must be stuck")
	}
	renewed := codextest.Tokens("account", "person@example.com", "plus", now.Add(time.Minute), now.Add(2*time.Hour))
	if _, err := s.CommitLogin(context.Background(), renewed); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	published, err := s.Load()
	if err != nil || published.Health != codex.HealthOK || published.Attempt != nil ||
		!published.Expires.Equal(now.Add(2*time.Hour)) || !published.LastRefresh.Equal(p.Attempt.Started) {
		t.Fatal("an interrupted published attempt must show the new login")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("Load must never rewrite an interrupted profile")
	}
}

func TestRetirementBoundsValuesAndUsesUniqueNames(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	for i := range 36 {
		tokens := codextest.Tokens("account", "person@example.com", "plus", now.Add(time.Duration(i)*time.Minute), now.Add(time.Hour))
		if _, err := s.CommitLogin(context.Background(), tokens); err != nil {
			t.Fatal(err)
		}
	}
	old, err := s.Retired()
	if err != nil || len(old) != 100 {
		t.Fatal("retirement must retain at most 100 values")
	}
	f, err := vault.Load(filepath.Join(cfg.StateDir, codex.RetiredFile))
	if err != nil {
		t.Fatal(err)
	}
	hasSuffix := false
	for _, secret := range f.Secrets() {
		hasSuffix = hasSuffix || strings.HasSuffix(secret.Name, "-2")
	}
	if !hasSuffix {
		t.Fatal("retired names in the same second must be made unique")
	}
}
