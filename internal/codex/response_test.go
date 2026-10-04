package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
)

func TestRefreshEmptyOptionalTokenIsNotAMissingToken(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	for _, field := range []string{"id_token", "refresh_token"} {
		t.Run(field, func(t *testing.T) {
			s, cfg := newStore(t, now)
			initial := codextest.Tokens("account", "person@example.com", "plus", now.Add(-time.Hour), now.Add(time.Minute))
			if _, err := s.CommitLogin(context.Background(), initial); err != nil {
				t.Fatal(err)
			}
			next := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
			body, _ := json.Marshal(map[string]string{"access_token": next.AccessToken, field: ""})
			server := codextest.NewTokenServer(t, initial)
			server.Queue(200, string(body))
			cfg.TokenURL, cfg.Client = server.URL, server.Client
			if _, err := codex.NewStore(cfg).EnsureFresh(context.Background(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrStuck) {
				t.Fatal("an explicitly empty token must be stuck, not silently keep a possibly consumed token")
			}
		})
	}
}

func TestLoginRejectsUnrepresentableExpiryBeforeChangingTheVault(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, _ := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	before, err := s.Public()
	if err != nil {
		t.Fatal(err)
	}
	invalid := codextest.Tokens("another-account", "another@example.com", "plus", now, now.Add(time.Hour))
	invalid.AccessToken = jwt(map[string]any{
		"exp": int64(1 << 62), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": invalid.AccountID},
	})
	if _, err := s.CommitLogin(context.Background(), invalid); err == nil {
		t.Fatal("a login with an unrepresentable expiry must be refused")
	}
	after, err := s.Public()
	if err != nil || after != before {
		t.Fatal("invalid login changed the vault before validating the profile")
	}
}
