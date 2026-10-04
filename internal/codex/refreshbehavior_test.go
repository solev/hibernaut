package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/egress/swap"
	"github.com/solev/hibernaut/internal/vault"
)

func TestBackgroundRefreshesAtHalfLifetime(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	now := start
	_, cfg := newStore(t, now)
	cfg.Now = func() time.Time { return now }
	s := codex.NewStore(cfg)
	initial := codextest.Tokens("account", "person@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	server := codextest.NewTokenServer(t, initial)
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	now = start.Add(30*time.Minute - time.Nanosecond)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || server.Requests() != 0 {
		t.Fatal("background refreshed before half the lifetime")
	}
	now = start.Add(30 * time.Minute)
	server.Queue(200, tokenAnswer(codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))))
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || server.Requests() != 1 {
		t.Fatal("background did not refresh at half the lifetime")
	}
}

func TestClientRefreshFingerprintExpiryAndTenMinuteLimit(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	now := start
	_, cfg := newStore(t, now)
	cfg.Now = func() time.Time { return now }
	s := codex.NewStore(cfg)
	initial := codextest.Tokens("account", "person@example.com", "plus", start, start.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	server := codextest.NewTokenServer(t, initial)
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	stale := codex.Reason{Kind: codex.Client, Rejected: swap.Fingerprint("a previous token")}
	if _, err := s.EnsureFresh(context.Background(), stale); err != nil || server.Requests() != 0 {
		t.Fatal("client refreshed for a stale rejected token")
	}
	next := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(6*time.Minute))
	server.Queue(200, tokenAnswer(next))
	current := codex.Reason{Kind: codex.Client, Rejected: swap.Fingerprint(initial.AccessToken)}
	if _, err := s.EnsureFresh(context.Background(), current); err != nil || server.Requests() != 1 {
		t.Fatal("client did not refresh for the current rejected token")
	}
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client}); err != nil || server.Requests() != 1 {
		t.Fatal("client must return placeholders when no refresh is due")
	}
	now = start.Add(time.Minute)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client}); !errors.Is(err, codex.ErrUnavailable) || server.Requests() != 1 {
		t.Fatal("near-expiry client refresh must respect the ten-minute limit")
	}
	now = start.Add(10 * time.Minute)
	server.Queue(200, tokenAnswer(codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))))
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client}); err != nil || server.Requests() != 2 {
		t.Fatal("client did not refresh at the ten-minute boundary")
	}
	p, err := s.Load()
	if err != nil || !p.ClientRefreshAt.Equal(now) {
		t.Fatal("client refresh time was not recorded")
	}
}

func TestClientReturnsPlaceholdersWhenNoRefreshIsDueRegardlessOfHealth(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	for _, health := range []codex.Health{codex.HealthDead, codex.HealthStuck} {
		t.Run(string(health), func(t *testing.T) {
			s, cfg := newStore(t, now)
			initial := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
			p, err := s.CommitLogin(context.Background(), initial)
			if err != nil {
				t.Fatal(err)
			}
			p.Health = health
			b, _ := json.Marshal(p)
			if err := os.WriteFile(filepath.Join(cfg.StateDir, codex.ProfileFile), b, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client}); err != nil {
				t.Fatal("a client needing no refresh must get placeholders regardless of health")
			}
			want := codex.ErrDead
			if health == codex.HealthStuck {
				want = codex.ErrStuck
			}
			if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client, Rejected: swap.Fingerprint(initial.AccessToken)}); !errors.Is(err, want) {
				t.Fatal("a due client refresh must report the login's health")
			}
		})
	}
}

func TestBackoffDoublesCapsAndForceCanRetryStuck(t *testing.T) {
	start := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	now := start
	_, cfg := newStore(t, now)
	cfg.Now = func() time.Time { return now }
	s := codex.NewStore(cfg)
	initial := codextest.Tokens("account", "person@example.com", "plus", start.Add(-time.Hour), start.Add(time.Minute))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	server := codextest.NewTokenServer(t, initial)
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	for i, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour} {
		server.Queue(429, `{}`)
		if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrUnavailable) {
			t.Fatal("rate limiting should back off")
		}
		p, err := s.Load()
		if err != nil || p.Failures != i+1 || !p.RetryAt.Equal(now.Add(delay)) {
			t.Fatal("backoff did not double or stop at an hour")
		}
		if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || server.Requests() != i+1 {
			t.Fatal("background ignored the retry time")
		}
		now = p.RetryAt
	}
	server.Queue(503, `{}`)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrStuck) {
		t.Fatal("a server error should be stuck")
	}
	server.Queue(200, tokenAnswer(codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))))
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Force}); err != nil {
		t.Fatal("force could not recover a stuck refresh")
	}
	p, err := s.Load()
	if err != nil || p.Health != codex.HealthOK || p.Attempt != nil || p.Failures != 0 || !p.RetryAt.IsZero() {
		t.Fatal("force did not clear the prior failure state")
	}
}

func TestRefreshMissingOptionalTokensKeepsOldValues(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	next := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
	server := codextest.NewTokenServer(t, initial)
	b, _ := json.Marshal(map[string]string{"access_token": next.AccessToken})
	server.Queue(200, string(b))
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil {
		t.Fatal(err)
	}
	f, err := vault.Load(cfg.VaultPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadKey(cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	values, err := f.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if (v.Name == codex.IDName && v.Value != initial.IDToken) ||
			(v.Name == codex.RefreshName && v.Value != initial.RefreshToken) {
			t.Fatal("a missing optional token must keep the old value")
		}
	}
	old, err := s.Retired()
	if err != nil || len(old) != 1 || old[0].Name != codex.AccessName {
		t.Fatal("only replaced values should be retired")
	}
}

func TestRefreshRefusesAnotherAccount(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	before, err := s.Public()
	if err != nil {
		t.Fatal(err)
	}
	server := codextest.NewTokenServer(t, initial)
	server.Queue(200, tokenAnswer(codextest.Tokens("another", "person@example.com", "plus", now, now.Add(time.Hour))))
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrStuck) {
		t.Fatal("an answer for another account must be stuck")
	}
	after, err := s.Public()
	if err != nil || after != before {
		t.Fatal("an answer for another account changed the public login")
	}
	old, err := s.Retired()
	if err != nil || len(old) != 0 {
		t.Fatal("an answer for another account must not publish or retire values")
	}
}

func TestRefreshPublishingFinishesAfterCallerCancels(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	next := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tokenAnswer(next)))}, nil
	})}
	s = codex.NewStore(cfg)
	if _, err := s.EnsureFresh(ctx, codex.Reason{Kind: codex.Background}); err != nil {
		t.Fatal("caller cancellation prevented publishing a received answer")
	}
	p, err := s.Load()
	if err != nil || p.Health != codex.HealthOK || p.Attempt != nil || !p.Expires.Equal(now.Add(time.Hour)) {
		t.Fatal("publishing did not finish after caller cancellation")
	}
}

func TestRefreshLostAnswerAndCrashNeverRetryByThemselves(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(refreshInterruptionName(crash), func(t *testing.T) {
			now := time.Now().UTC()
			s, cfg := newStore(t, now)
			initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
			if _, err := s.CommitLogin(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			received := make(chan struct{})
			release := make(chan struct{})
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				close(received)
				if crash {
					<-release
				}
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = connection.Close()
				}
			}))
			defer server.Close()
			cfg.TokenURL, cfg.Client = server.URL, server.Client()
			s = codex.NewStore(cfg)
			if crash {
				cmd := helper(t, cfg)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				<-received
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
				close(release)
			} else if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrStuck) {
				t.Fatal("a lost answer must be stuck")
			}
			p, err := s.Load()
			if err != nil || p.Health != codex.HealthStuck || p.Attempt == nil {
				t.Fatal("an interrupted refresh must remain stuck")
			}
			if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || count.Load() != 1 {
				t.Fatal("background retried an interrupted refresh")
			}
		})
	}
}

func refreshInterruptionName(crash bool) string {
	if crash {
		return "crash"
	}
	return "lost answer"
}
