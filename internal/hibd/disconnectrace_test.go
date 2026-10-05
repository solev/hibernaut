package hibd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/vault"
)

type refreshTransport func(*http.Request) (*http.Response, error)

// RoundTrip serves a synthetic refresh without network access.
func (f refreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDisconnectWaitsForRefreshAndCannotRestoreTokens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, out, errOut := newApp(t, fake.New())
		withSecretConfig(t, a)
		a.EUID = func() int { return 0 }
		at := time.Now()
		a.Now = func() time.Time { return at }
		initial := codextest.Tokens("acct-1234", "dev@example.com", "plus", at, at.Add(time.Hour))
		next := codextest.Tokens("acct-1234", "dev@example.com", "plus", at, at.Add(2*time.Hour))
		started, release := make(chan struct{}), make(chan struct{})
		a.CodexClient = &http.Client{Transport: refreshTransport(func(r *http.Request) (*http.Response, error) {
			close(started)
			<-release
			body, err := json.Marshal(map[string]string{
				"access_token": next.AccessToken, "refresh_token": next.RefreshToken, "id_token": next.IDToken,
			})
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
		})}
		store := codexStoreFor(a)
		if _, err := store.CommitLogin(t.Context(), initial); err != nil {
			t.Fatal(err)
		}
		refreshed := make(chan error, 1)
		go func() {
			_, err := store.EnsureFresh(t.Context(), codex.Reason{Kind: codex.Force})
			refreshed <- err
		}()
		<-started
		done := make(chan int, 1)
		go func() { done <- a.Run(t.Context(), []string{"disconnect", "codex", "-json"}) }()
		synctest.Wait()
		select {
		case code := <-done:
			t.Fatalf("disconnect ran through the refresh lock: exit %d, %s", code, errOut)
		default:
		}
		if secrets := loadVault(t, a); len(secrets) != 3 {
			t.Fatal("disconnect removed tokens before the refresh ended")
		}
		close(release)
		if err := <-refreshed; err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if code := <-done; code != 0 {
			t.Fatalf("disconnect exit %d, stdout %s, stderr %s", code, out, errOut)
		}
		if secrets := loadVault(t, a); len(secrets) != 0 {
			t.Fatalf("refresh put tokens back: %d secrets", len(secrets))
		}
		if _, err := store.Load(); !errors.Is(err, codex.ErrNotConnected) {
			t.Errorf("profile survived: %v", err)
		}
	})
}

func TestDaemonRefresherStaysSilentAfterDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _, _ := newApp(t, fake.New())
		withSecretConfig(t, a)
		a.EUID = func() int { return 0 }
		a.Now = time.Now
		a.DaemonTicks = make(chan time.Time)
		var log syncBuffer
		a.Stderr = &log
		tokens := codextest.Tokens("acct", "dev@example.com", "plus", time.Now(), time.Now().Add(24*time.Hour))
		if _, err := codexStoreFor(a).CommitLogin(t.Context(), tokens); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan int, 1)
		go func() { done <- a.Run(ctx, []string{"daemon"}) }()
		synctest.Wait()
		if !strings.Contains(log.String(), "codex: the login is ok") {
			t.Fatalf("refresher did not start: %s", log.String())
		}
		if code := a.Run(t.Context(), []string{"disconnect", "codex", "-json"}); code != 0 {
			t.Fatalf("disconnect exit %d: %s", code, log.String())
		}
		before := len(log.String())
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if after := log.String()[before:]; strings.Contains(after, "codex:") {
			t.Fatalf("disconnected refresher logged: %s", after)
		}
		select {
		case code := <-done:
			t.Fatalf("daemon stopped after disconnect: exit %d", code)
		default:
		}
		cancel()
		if code := <-done; code != 0 {
			t.Fatalf("daemon exit %d", code)
		}
	})
}

func TestDisconnectLockWaitIsBoundedByContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, out, errOut := newApp(t, fake.New())
		withSecretConfig(t, a)
		a.EUID = func() int { return 0 }
		if _, err := codexStoreFor(a).CommitLogin(t.Context(), codextest.Tokens("acct", "dev@example.com", "plus",
			time.Now(), time.Now().Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
		unlock, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), codex.LockFile, "refresh")
		if err != nil || !ok {
			t.Fatalf("refresh lock: %v, %v", ok, err)
		}
		defer unlock()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if code := a.Run(ctx, []string{"disconnect", "codex", "-json"}); code != 1 ||
			!strings.Contains(out.String(), "Finish the refresh") {
			t.Fatalf("exit %d, stdout %s, stderr %s", code, out, errOut)
		}
		if secrets := loadVault(t, a); len(secrets) != 3 {
			t.Fatal("cancelled disconnect changed the vault")
		}
	})
}

func TestDisconnectRefusesLoginInProgress(t *testing.T) {
	for _, provider := range []string{"codex", "github"} {
		t.Run(provider, func(t *testing.T) {
			a, out, errOut := newApp(t, fake.New())
			withSecretConfig(t, a)
			a.EUID = func() int { return 0 }
			unlock, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), provider+"-login.lock", "login")
			if err != nil || !ok {
				t.Fatalf("login lock: %v, %v", ok, err)
			}
			defer unlock()
			if code := a.Run(t.Context(), []string{"disconnect", provider, "-json"}); code != 1 ||
				!strings.Contains(out.String(), "login or disconnect is in progress") ||
				!strings.Contains(out.String(), "run hib disconnect "+provider+" again") {
				t.Fatalf("exit %d, stdout %s, stderr %s", code, out, errOut)
			}
		})
	}
}

func TestDisconnectFinishesAfterVaultWasRemoved(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	withSecretConfig(t, a)
	a.EUID = func() int { return 0 }
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codexStoreFor(a).CommitLogin(t.Context(), testCodexTokens()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.stateDir, codex.ProfileFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(path, "keep")
	if err := os.WriteFile(child, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := a.Run(t.Context(), []string{"disconnect", "codex", "-json"}); code != 1 ||
		!strings.Contains(out.String(), "again to finish") {
		t.Fatalf("exit %d, stdout %s, stderr %s", code, out, errOut)
	}
	f, err := vault.Load(cfg.vaultPath)
	if err != nil || len(f.Secrets()) != 0 {
		t.Fatalf("vault was not cleared before file removal: %v", err)
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"disconnect", "codex", "-json"}); code != 0 {
		t.Fatalf("retry exit %d, stdout %s, stderr %s", code, out, errOut)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retry did not remove the remaining file")
	}
}
