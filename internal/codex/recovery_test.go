package codex_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/egress/swap"
)

func TestRecoveryRestoresPublishedWhenOtherIdentityClaimsChanged(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, cfg := newStore(t, now)
	initial := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StateDir, codex.ProfileFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pending map[string]any
	if err := json.Unmarshal(before, &pending); err != nil {
		t.Fatal(err)
	}
	started := now.Add(30 * time.Minute)
	pending["attempt"] = codex.Attempt{Generation: swap.Fingerprint(initial.RefreshToken), Started: started}
	before, _ = json.Marshal(pending)
	renewed := codextest.Tokens("account", "person@example.com", "plus", started, started.Add(time.Hour))
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(renewed.AccessToken, ".")[1])
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	claims["scope"] = "a new scope"
	renewed.AccessToken = jwt(claims)
	if _, err := s.CommitLogin(context.Background(), renewed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := s.Load()
	if err != nil || p.Attempt != nil || !p.Published.Equal(started) {
		t.Fatal("recovery lost a placeholder change outside the profile's public claims")
	}
}
