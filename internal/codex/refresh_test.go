package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/egress/swap"
	"github.com/solev/hibernaut/internal/vault"
)

func tokenAnswer(tokens codex.Tokens) string {
	b, _ := json.Marshal(struct {
		Access  string `json:"access_token"`
		ID      string `json:"id_token"`
		Refresh string `json:"refresh_token"`
	}{tokens.AccessToken, tokens.IDToken, tokens.RefreshToken})
	return string(b)
}

func TestRefreshOutcomeHealthAndBackoff(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body, retry string
		status            int
		health            codex.Health
		want              error
		attempt           bool
		failures          int
		delay             time.Duration
	}{
		{name: "unauthorized", status: 401, body: `{"error":"unknown"}`, health: codex.HealthDead, want: codex.ErrDead},
		{name: "invalid grant", status: 400, body: `{"error":"invalid_grant"}`, health: codex.HealthDead, want: codex.ErrDead},
		{name: "expired", status: 400, body: `{"error":{"code":"refresh_token_expired"}}`, health: codex.HealthDead, want: codex.ErrDead},
		{name: "reused", status: 400, body: `{"error":{"code":"refresh_token_reused"}}`, health: codex.HealthDead, want: codex.ErrDead},
		{name: "invalidated", status: 400, body: `{"error":{"code":"REFRESH_TOKEN_INVALIDATED"}}`, health: codex.HealthDead, want: codex.ErrDead},
		{name: "top level code", status: 400, body: `{"code":"invalid_grant"}`, health: codex.HealthDead, want: codex.ErrDead},
		{name: "rate limited", status: 429, body: `{"error":"slow_down"}`, health: codex.HealthOK, want: codex.ErrUnavailable, failures: 1, delay: time.Minute},
		{name: "retry seconds", status: 429, retry: "180", health: codex.HealthOK, want: codex.ErrUnavailable, failures: 1, delay: 3 * time.Minute},
		{name: "retry date", status: 429, retry: now.Add(4 * time.Minute).Format(http.TimeFormat), health: codex.HealthOK, want: codex.ErrUnavailable, failures: 1, delay: 4 * time.Minute},
		{name: "retry capped", status: 429, retry: "999999999999", health: codex.HealthOK, want: codex.ErrUnavailable, failures: 1, delay: time.Hour},
		{name: "retry invalid", status: 429, retry: "-1", health: codex.HealthOK, want: codex.ErrUnavailable, failures: 1, delay: time.Minute},
		{name: "other client error", status: 403, body: `{"error":"access_denied"}`, health: codex.HealthOK, want: codex.ErrUnavailable, failures: 1, delay: time.Minute},
		{name: "server error", status: 503, body: `{"error":"unavailable"}`, health: codex.HealthStuck, want: codex.ErrStuck, attempt: true},
		{name: "malformed", status: 200, body: `{"access_token":`, health: codex.HealthStuck, want: codex.ErrStuck, attempt: true},
		{name: "empty", status: 200, body: `{}`, health: codex.HealthStuck, want: codex.ErrStuck, attempt: true},
		{name: "wrong field type", status: 200, body: `{"access_token":{"secret":"echo"}}`, health: codex.HealthStuck, want: codex.ErrStuck, attempt: true},
		{name: "oversized", status: 200, body: strings.Repeat("X", (1<<20)+1), health: codex.HealthStuck, want: codex.ErrStuck, attempt: true},
		{name: "no content", status: 204, health: codex.HealthStuck, want: codex.ErrStuck, attempt: true},
		{name: "redirect", status: 307, health: codex.HealthStuck, want: codex.ErrStuck, attempt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newStore(t, now)
			initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
			if _, err := s.CommitLogin(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Retry-After", tc.retry)
				if tc.status == 307 {
					w.Header().Set("Location", "/must-not-follow")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			var logs strings.Builder
			cfg.TokenURL, cfg.Client = server.URL, server.Client()
			cfg.Logf = func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) }
			s = codex.NewStore(cfg)
			_, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background})
			if !errors.Is(err, tc.want) {
				t.Fatal("refresh returned the wrong error class")
			}
			p, err := s.Load()
			if err != nil || p.Health != tc.health || (p.Attempt != nil) != tc.attempt || p.Failures != tc.failures {
				t.Fatal("refresh left the wrong health, attempt or failures")
			}
			if tc.delay != 0 && !p.RetryAt.Equal(now.Add(tc.delay)) {
				t.Fatal("refresh left the wrong retry time")
			}
			if tc.delay == 0 && !p.RetryAt.IsZero() {
				t.Fatal("dead or stuck refresh must not schedule a retry")
			}
			if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || count.Load() != 1 {
				t.Fatal("background retried a blocked refresh")
			}
			for _, value := range []string{initial.AccessToken, initial.IDToken, initial.RefreshToken} {
				if strings.Contains(logs.String(), value) || strings.Contains(p.Problem, value) {
					t.Fatal("refresh diagnostics exposed a token")
				}
			}
			if logs.Len() == 0 {
				t.Fatal("refresh outcome was not logged")
			}
			if tc.health == codex.HealthDead {
				if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Force}); !errors.Is(err, codex.ErrDead) || count.Load() != 1 {
					t.Fatal("force retried a dead login")
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRefreshTransportOutcomesNeverLeakTokens(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	for _, sent := range []bool{false, true} {
		t.Run(strconv.FormatBool(sent), func(t *testing.T) {
			s, cfg := newStore(t, now)
			initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
			if _, err := s.CommitLogin(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			cfg.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if sent {
					httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
				}
				return nil, errors.New("network failure echoed " + initial.RefreshToken)
			})}
			var logs strings.Builder
			cfg.Logf = func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) }
			s = codex.NewStore(cfg)
			_, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background})
			if err == nil || strings.Contains(err.Error(), initial.RefreshToken) || strings.Contains(logs.String(), initial.RefreshToken) {
				t.Fatal("transport diagnostics exposed a token or failed to report an error")
			}
			p, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if sent {
				if p.Health != codex.HealthStuck || p.Attempt == nil || !p.RetryAt.IsZero() {
					t.Fatal("an unanswered sent request must be stuck")
				}
			} else if p.Health != codex.HealthOK || p.Attempt != nil || p.Failures != 1 || !p.RetryAt.Equal(now.Add(time.Minute)) {
				t.Fatal("a request not sent must back off without changing health")
			}
		})
	}
}

func TestRefreshConcurrentGoroutinesMakeOneRequest(t *testing.T) {
	now := time.Now().UTC()
	s, cfg := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	server := codextest.NewTokenServer(t, initial)
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	start := make(chan struct{})
	errs := make(chan error, 24)
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			<-start
			_, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Client, Rejected: swap.Fingerprint(initial.AccessToken)})
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if server.Requests() != 1 {
		t.Fatal("concurrent clients caused more than one upstream refresh")
	}
}

func TestRefreshProcessHelper(t *testing.T) {
	if os.Getenv("HIB_CODEX_STORE_HELPER") != "1" {
		return
	}
	dir := os.Getenv("HIB_CODEX_STORE_DIR")
	s := codex.NewStore(codex.Config{
		StateDir: dir, VaultPath: filepath.Join(dir, "vault.json"), KeyPath: filepath.Join(dir, "vault.key"),
		TokenURL: os.Getenv("HIB_CODEX_STORE_URL"),
	})
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil {
		t.Fatal("helper refresh failed")
	}
}

func helper(t *testing.T, cfg codex.Config) *exec.Cmd {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestRefreshProcessHelper$")
	cmd.Env = append(os.Environ(), "HIB_CODEX_STORE_HELPER=1", "HIB_CODEX_STORE_DIR="+cfg.StateDir, "HIB_CODEX_STORE_URL="+cfg.TokenURL)
	return cmd
}

func TestRefreshConcurrentProcessesMakeOneRequest(t *testing.T) {
	now := time.Now().UTC()
	s, cfg := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	server := codextest.NewTokenServer(t, initial)
	cfg.TokenURL = server.URL
	var commands []*exec.Cmd
	for range 8 {
		cmd := helper(t, cfg)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal("helper process failed")
		}
	}
	if server.Requests() != 1 {
		t.Fatal("concurrent processes caused more than one upstream refresh")
	}
}
func TestRefreshRotatesTokensAndKeepsPlaceholders(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
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
	cfg.TokenURL, cfg.Client = server.URL, server.Client
	s = codex.NewStore(cfg)
	after, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background})
	if err != nil {
		t.Fatal(err)
	}
	if after != before || server.Requests() != 1 {
		t.Fatal("refresh should rotate once without changing compatible placeholders")
	}
	p, err := s.Load()
	if err != nil || p.Health != codex.HealthOK || p.Attempt != nil || p.Failures != 0 || !p.RetryAt.IsZero() {
		t.Fatal("successful refresh did not leave a healthy profile")
	}
	f, err := vault.Load(cfg.VaultPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadKey(cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := f.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	rotated := server.Tokens()
	expected := map[string]string{codex.AccessName: rotated.AccessToken, codex.IDName: rotated.IDToken, codex.RefreshName: rotated.RefreshToken}
	for _, value := range revealed {
		if value.Value != expected[value.Name] {
			t.Fatal("refresh did not seal the newly issued tokens")
		}
	}
	old, err := s.Retired()
	if err != nil || len(old) != 3 {
		t.Fatal("refresh did not retire all replaced tokens")
	}
	if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); err != nil || server.Requests() != 1 {
		t.Fatal("background refreshed a token before half its lifetime")
	}
}

func TestRefreshNotConnected(t *testing.T) {
	s, _ := newStore(t, time.Now())
	for _, reason := range []codex.ReasonKind{codex.Background, codex.Client, codex.Force} {
		if _, err := s.EnsureFresh(context.Background(), codex.Reason{Kind: reason}); !errors.Is(err, codex.ErrNotConnected) {
			t.Fatal("refreshing without a login should mean not connected")
		}
	}
}
