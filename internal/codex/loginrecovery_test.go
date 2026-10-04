package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

func failLoginProfileSave(t *testing.T, s *codex.Store, cfg codex.Config, tokens codex.Tokens) []byte {
	t.Helper()
	retiredPath := filepath.Join(cfg.StateDir, codex.RetiredFile)
	retired, err := vault.Load(retiredPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := retired.Save(retiredPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(retiredPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(retiredPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(retiredPath, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.CommitLogin(context.Background(), tokens)
		done <- err
	}()
	pipe, err := os.OpenFile(retiredPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	profilePath := filepath.Join(cfg.StateDir, codex.ProfileFile)
	pending, err := os.ReadFile(profilePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if pending != nil {
		if err := os.Rename(profilePath, profilePath+".pending"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(profilePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := pipe.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := pipe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "publishing codex profile") {
		t.Fatal("login did not fail at the final profile publication")
	}
	if err := os.Remove(profilePath); err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		if err := os.Rename(profilePath+".pending", profilePath); err != nil {
			t.Fatal(err)
		}
	}
	return pending
}

func TestCommitLoginRecoversAccountSwitchAfterProfileSaveFails(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	now := start
	_, cfg := newStore(t, start)
	cfg.Now = func() time.Time { return now }
	s := codex.NewStore(cfg)
	initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	server := codextest.NewTokenServer(t, initial)
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	server.Queue(429, `{}`)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client, Rejected: swap.Fingerprint(initial.AccessToken)}); !errors.Is(err, codex.ErrUnavailable) {
		t.Fatal("could not prepare the old login's backoff and client throttle")
	}
	server.Queue(503, `{}`)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Force}); !errors.Is(err, codex.ErrStuck) {
		t.Fatal("could not prepare the old login's stuck refresh")
	}
	now = start.Add(10 * time.Minute)
	next := codextest.Tokens("second", "second@example.com", "pro", now, now.Add(time.Hour))
	pending := failLoginProfileSave(t, s, cfg, next)
	p, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p.AccountID != "second" || p.Email != "second@example.com" || p.Plan != "pro" ||
		!p.Expires.Equal(now.Add(time.Hour)) || !p.ConnectedAt.Equal(now) ||
		!p.LastRefresh.Equal(now) || !p.Published.Equal(now) || p.Health != codex.HealthOK ||
		p.Attempt != nil || p.Problem != "" || p.Failures != 0 || !p.RetryAt.IsZero() ||
		!p.ClientRefreshAt.IsZero() {
		t.Fatal("Load did not recover the new account's clean profile")
	}
	public, err := s.Public()
	if err != nil || public.AccountID != "second" || !public.LastRefresh.Equal(now) {
		t.Fatal("Public combined the new placeholders with the old profile")
	}
	after, err := os.ReadFile(filepath.Join(cfg.StateDir, codex.ProfileFile))
	if err != nil || !bytes.Equal(after, pending) {
		t.Fatal("reading the interrupted login rewrote its profile")
	}
	server = codextest.NewTokenServer(t, next)
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	if got, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || got != public {
		t.Fatal("background did not preserve the recovered login")
	}
	if server.Requests() != 0 {
		t.Fatal("background unnecessarily refreshed the recovered login")
	}
	after, err = os.ReadFile(filepath.Join(cfg.StateDir, codex.ProfileFile))
	if err != nil || bytes.Equal(after, pending) {
		t.Fatal("the next lock holder did not record the recovery")
	}
	now = now.Add(time.Minute)
	reconnected := codextest.Tokens("second", "second@example.com", "pro", now, now.Add(time.Hour))
	p, err = s.CommitLogin(context.Background(), reconnected)
	if err != nil || !p.ConnectedAt.Equal(now) || p.Attempt != nil {
		t.Fatal("the next login could not replace the recovered account")
	}
	if got, err := s.Public(); err != nil || got != public {
		t.Fatal("same-account reconnect changed compatible placeholders or their publication time")
	}
}

func TestPublicRecoversPublishedLoginWhileLockIsHeld(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	now := start
	_, cfg := newStore(t, start)
	cfg.Now = func() time.Time { return now }
	s := codex.NewStore(cfg)
	initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	now = start.Add(10 * time.Minute)
	next := codextest.Tokens("second", "second@example.com", "pro", now, now.Add(time.Hour))
	pending := failLoginProfileSave(t, s, cfg, next)
	lock, err := os.OpenFile(filepath.Join(cfg.StateDir, codex.LockFile), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	inProgress, err := s.Load()
	if err != nil || inProgress.AccountID != "first" || inProgress.Attempt == nil {
		t.Fatal("Load did not leave an active attempt in progress")
	}
	public, err := s.Public()
	if err != nil || public.AccountID != "second" || !public.LastRefresh.Equal(now) {
		t.Fatal("Public combined generations while the publisher held the lock")
	}
	data, err := codex.RenderAuthFile(public)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := codex.ParseAuthFile(data); err != nil || parsed.AccountID != "second" {
		t.Fatal("the public login file's account did not match its token claims")
	}
	after, err := os.ReadFile(filepath.Join(cfg.StateDir, codex.ProfileFile))
	if err != nil || !bytes.Equal(after, pending) {
		t.Fatal("Public rewrote an active attempt")
	}
}

func TestCommitLoginRecoversSameAccountPublicationTime(t *testing.T) {
	for _, plan := range []string{"plus", "pro"} {
		t.Run(plan, func(t *testing.T) {
			start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			now := start
			_, cfg := newStore(t, start)
			cfg.Now = func() time.Time { return now }
			s := codex.NewStore(cfg)
			initial := codextest.Tokens("account", "person@example.com", "plus", start, start.Add(time.Hour))
			if _, err := s.CommitLogin(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			before, err := s.Public()
			if err != nil {
				t.Fatal(err)
			}
			now = start.Add(10 * time.Minute)
			next := codextest.Tokens("account", "person@example.com", plan, now, now.Add(time.Hour))
			failLoginProfileSave(t, s, cfg, next)
			published := start
			if plan == "pro" {
				published = now
			}
			p, err := s.Load()
			if err != nil || p.Plan != plan || !p.ConnectedAt.Equal(now) ||
				!p.LastRefresh.Equal(now) || !p.Published.Equal(published) || p.Attempt != nil {
				t.Fatal("same-account recovery lost the connection or placeholder publication time")
			}
			after, err := s.Public()
			if err != nil || !after.LastRefresh.Equal(published) || (after == before) != (plan == "plus") {
				t.Fatal("same-account recovery did not follow placeholder compatibility")
			}
			now = now.Add(time.Minute)
			p, err = s.CommitLogin(context.Background(), next)
			if err != nil || !p.ConnectedAt.Equal(now) || !p.Published.Equal(published) || p.Attempt != nil {
				t.Fatal("the next login did not recover before replacing the pending login")
			}
			if got, err := s.Public(); err != nil || got != after {
				t.Fatal("the next login changed the recovered placeholders")
			}
		})
	}
}

func TestEnsureFreshUsesRecoveredLoginForDueRefresh(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	now := start
	_, cfg := newStore(t, start)
	cfg.Now = func() time.Time { return now }
	s := codex.NewStore(cfg)
	initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	now = start.Add(10 * time.Minute)
	next := codextest.Tokens("second", "second@example.com", "pro", now.Add(-time.Hour), now.Add(time.Minute))
	failLoginProfileSave(t, s, cfg, next)
	server := codextest.NewTokenServer(t, next)
	renewed := codextest.Tokens("second", "second@example.com", "pro", now, now.Add(time.Hour))
	server.Queue(200, tokenAnswer(renewed))
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	public, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background})
	if err != nil || server.Requests() != 1 || public.AccountID != "second" {
		t.Fatal("background did not refresh the recovered account with its own tokens")
	}
	p, err := s.Load()
	if err != nil || p.Health != codex.HealthOK || p.Attempt != nil ||
		!p.ConnectedAt.Equal(now) || !p.Expires.Equal(now.Add(time.Hour)) {
		t.Fatal("refresh did not preserve the recovered connection and new expiry")
	}
}

func TestFirstConnectProfileFailureStaysNotConnected(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, start)
	tokens := codextest.Tokens("account", "person@example.com", "plus", start, start.Add(time.Hour))
	if pending := failLoginProfileSave(t, s, cfg, tokens); pending != nil {
		t.Fatal("a first connect recorded a profile before publishing")
	}
	if _, err := s.Load(); !errors.Is(err, codex.ErrNotConnected) {
		t.Fatal("an interrupted first connect must remain not connected")
	}
	if _, err := s.Public(); !errors.Is(err, codex.ErrNotConnected) {
		t.Fatal("an interrupted first connect exposed a partial public login")
	}
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrNotConnected) {
		t.Fatal("an interrupted first connect must not start a refresh")
	}
	p, err := s.CommitLogin(context.Background(), tokens)
	if err != nil || p.AccountID != "account" || !p.Published.Equal(start) || p.Attempt != nil {
		t.Fatal("a later first connect could not complete the login")
	}
}

func TestCommitLoginProcessHelper(t *testing.T) {
	if os.Getenv("HIB_CODEX_LOGIN_HELPER") != "1" {
		return
	}
	dir := os.Getenv("HIB_CODEX_LOGIN_DIR")
	now := time.Date(2026, 10, 4, 8, 10, 0, 0, time.UTC)
	s := codex.NewStore(codex.Config{
		StateDir: dir, VaultPath: filepath.Join(dir, "vault.json"), KeyPath: filepath.Join(dir, "vault.key"),
		Now: func() time.Time { return now },
	})
	tokens := codextest.Tokens("second", "second@example.com", "pro", now, now.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), tokens); err != nil {
		t.Fatal("helper login failed")
	}
}

func TestCommitLoginCrashBeforePublicationKeepsOldLogin(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, start)
	initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	initialServer := codextest.NewTokenServer(t, initial)
	initialServer.Queue(429, `{}`)
	cfg.TokenURL, cfg.Client = initialServer.URL, initialServer.Client
	s = codex.NewStore(cfg)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client, Rejected: swap.Fingerprint(initial.AccessToken)}); !errors.Is(err, codex.ErrUnavailable) {
		t.Fatal("could not prepare the old login's retry and client-refresh state")
	}
	before, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	public, err := s.Public()
	if err != nil {
		t.Fatal(err)
	}
	retiredPath := filepath.Join(cfg.StateDir, codex.RetiredFile)
	if err := syscall.Mkfifo(retiredPath, 0o600); err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestCommitLoginProcessHelper$")
	cmd.Env = []string{"HIB_CODEX_LOGIN_HELPER=1", "HIB_CODEX_LOGIN_DIR=" + cfg.StateDir}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	pipe, err := os.OpenFile(retiredPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	active, err := s.Load()
	if err != nil || active.AccountID != "first" || active.Attempt == nil || !active.Attempt.Login {
		t.Fatal("the active login was not recorded before publication")
	}
	if got, err := s.Public(); err != nil || got != public {
		t.Fatal("an unpublished active login changed the public login")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := pipe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(retiredPath); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(cfg.StateDir, codex.ProfileFile)
	pending, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Load()
	if err != nil || p != before {
		t.Fatal("an unpublished login did not preserve the old profile exactly")
	}
	after, err := os.ReadFile(profilePath)
	if err != nil || !bytes.Equal(after, pending) {
		t.Fatal("Load rewrote the unpublished login attempt")
	}
	server := codextest.NewTokenServer(t, initial)
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	if got, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || got != public || server.Requests() != 0 {
		t.Fatal("background treated an unpublished login as a stuck refresh")
	}
	after, err = os.ReadFile(profilePath)
	if err != nil || bytes.Equal(after, pending) {
		t.Fatal("background did not record the unpublished login's recovery")
	}
}

func TestPublicRetriesWhenLoginChangesBetweenReads(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	now := start
	_, cfg := newStore(t, start)
	cfg.Now = func() time.Time { return now }
	s := codex.NewStore(cfg)
	initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cfg.VaultPath, cfg.VaultPath+".before"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(cfg.VaultPath, 0o600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		public codex.PublicAuth
		err    error
	}
	done := make(chan result, 1)
	go func() {
		public, err := s.Public()
		done <- result{public, err}
	}()
	pipe, err := os.OpenFile(cfg.VaultPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	if err := os.Remove(cfg.VaultPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cfg.VaultPath+".before", cfg.VaultPath); err != nil {
		t.Fatal(err)
	}
	now = start.Add(10 * time.Minute)
	next := codextest.Tokens("second", "second@example.com", "pro", now, now.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfg.VaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipe.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := pipe.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.public.AccountID != "second" || !got.public.LastRefresh.Equal(now) {
		t.Fatal("Public returned an old profile with a new vault snapshot")
	}
}

func TestCommitLoginRecoversWhenVaultHadNoCodexTokens(t *testing.T) {
	for _, account := range []string{"first", "second"} {
		t.Run(account, func(t *testing.T) {
			start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			now := start
			_, cfg := newStore(t, start)
			cfg.Now = func() time.Time { return now }
			s := codex.NewStore(cfg)
			initial := codextest.Tokens("first", "person@example.com", "plus", start, start.Add(time.Hour))
			if _, err := s.CommitLogin(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(cfg.VaultPath); err != nil {
				t.Fatal(err)
			}
			now = start.Add(10 * time.Minute)
			next := codextest.Tokens(account, "person@example.com", "plus", now, now.Add(time.Hour))
			failLoginProfileSave(t, s, cfg, next)
			p, err := s.Load()
			if err != nil || p.AccountID != account || p.Attempt != nil ||
				!p.Expires.Equal(now.Add(time.Hour)) || !p.Published.Equal(now) {
				t.Fatal("a login with no old vault tokens did not recover its new placeholders' publication")
			}
			if public, err := s.Public(); err != nil || public.AccountID != account || !public.LastRefresh.Equal(now) {
				t.Fatal("a login with no old vault tokens exposed the old profile")
			}
		})
	}
}

func TestPublicNeedsKeyOnlyForPendingPublication(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, start)
	initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cfg.KeyPath, cfg.KeyPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Public(); err != nil {
		t.Fatal("a completed profile needed the vault key")
	}
	if err := os.Rename(cfg.KeyPath+".saved", cfg.KeyPath); err != nil {
		t.Fatal(err)
	}
	next := codextest.Tokens("second", "second@example.com", "pro", start, start.Add(time.Hour))
	failLoginProfileSave(t, s, cfg, next)
	if err := os.Rename(cfg.KeyPath, cfg.KeyPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Public(); err == nil || !strings.Contains(err.Error(), "restore the vault key") {
		t.Fatal("a pending publication did not report its missing vault key")
	}
	if err := os.Rename(cfg.KeyPath+".saved", cfg.KeyPath); err != nil {
		t.Fatal(err)
	}
	if public, err := s.Public(); err != nil || public.AccountID != "second" {
		t.Fatal("restoring the vault key did not recover the pending publication")
	}
}

func TestEnsureFreshRecordsUnpublishedLoginWithoutOldTokens(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, start)
	initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
	before, err := s.CommitLogin(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.VaultPath); err != nil {
		t.Fatal(err)
	}
	retiredPath := filepath.Join(cfg.StateDir, codex.RetiredFile)
	if err := os.Mkdir(retiredPath, 0o700); err != nil {
		t.Fatal(err)
	}
	next := codextest.Tokens("second", "second@example.com", "pro", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), next); err == nil {
		t.Fatal("login unexpectedly published through a broken retirement file")
	}
	if err := os.Remove(retiredPath); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(cfg.StateDir, codex.ProfileFile)
	pending, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Load()
	if err != nil || p != before {
		t.Fatal("an unpublished login without old tokens did not preserve its profile")
	}
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrNotConnected) {
		t.Fatal("background did not report the missing Codex tokens")
	}
	after, err := os.ReadFile(profilePath)
	if err != nil || bytes.Equal(after, pending) {
		t.Fatal("background did not clear the unpublished attempt before reporting missing tokens")
	}
}

func TestPublicRefusesUnmarkedAccountSwitchMismatch(t *testing.T) {
	for _, tc := range []struct {
		name, account string
		fingerprint   bool
	}{
		{"changed account with fingerprint", "second", true},
		{"changed account without fingerprint", "second", false},
		{"same account with changed placeholders", "first", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			s, cfg := newStore(t, start)
			initial := codextest.Tokens("first", "first@example.com", "plus", start, start.Add(time.Hour))
			if _, err := s.CommitLogin(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.StateDir, codex.ProfileFile)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			next := codextest.Tokens(tc.account, "second@example.com", "pro", start, start.Add(time.Hour))
			if _, err := s.CommitLogin(context.Background(), next); err != nil {
				t.Fatal(err)
			}
			if !tc.fingerprint {
				var record map[string]any
				if err := json.Unmarshal(before, &record); err != nil {
					t.Fatal(err)
				}
				delete(record, "placeholders")
				before, err = json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, before, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(cfg.KeyPath, cfg.KeyPath+".saved"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Public(); err == nil || !strings.Contains(err.Error(), "run hib connect codex") {
				t.Fatal("Public did not refuse an unmarked profile and vault mismatch")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatal("Public rewrote an unmarked profile mismatch")
			}
		})
	}
}
