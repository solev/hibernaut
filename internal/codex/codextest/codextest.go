// Package codextest supplies synthetic ChatGPT logins and a local token server. It never calls
// OpenAI and never reads an existing Codex login.
package codextest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
)

// Tokens makes fresh synthetic tokens with Codex's claim paths, a fake JWT signature and an
// rt. refresh token. Every call produces distinct values.
func Tokens(account, email, plan string, iat, exp time.Time) codex.Tokens {
	auth := map[string]any{
		"chatgpt_account_id": account, "chatgpt_plan_type": plan,
		"chatgpt_user_id": "test-user", "chatgpt_account_is_fedramp": false,
	}
	makeJWT := func(kind string) string {
		b, _ := json.Marshal(map[string]any{
			"iat": iat.Unix(), "exp": exp.Unix(), "jti": random(),
			"token_use": kind, "https://api.openai.com/auth": auth,
			"https://api.openai.com/profile": map[string]string{"email": email},
		})
		return "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9." + base64.RawURLEncoding.EncodeToString(b) + "." + random()
	}
	return codex.Tokens{IDToken: makeJWT("id"), AccessToken: makeJWT("access"), RefreshToken: "rt." + random(), AccountID: account}
}

func random() string {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		panic("making synthetic codex tokens")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthFile returns the auth.json Codex writes for tokens. It uses a fixed last_refresh to make
// fixtures repeatable. The returned bytes contain synthetic values, not placeholders.
func AuthFile(tokens codex.Tokens) []byte {
	b, err := codex.RenderAuthFile(codex.PublicAuth{
		IDToken: tokens.IDToken, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		AccountID: tokens.AccountID, LastRefresh: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		panic("making synthetic codex login file")
	}
	return b
}

type answer struct {
	status int
	body   string
}

// TokenServer is an HTTP httptest server that rotates synthetic tokens for one account. Set
// Config.TokenURL to URL and Config.Client to Client. No TLS trust setup is needed.
type TokenServer struct {
	URL    string
	Client *http.Client
	server *httptest.Server
	mu     sync.Mutex
	tokens codex.Tokens
	count  int
	next   []answer
}

// NewTokenServer starts a fake token endpoint for initial. Its default answer issues new
// tokens for the same account with a one-hour lifetime. t cleans up the server.
func NewTokenServer(t testing.TB, initial codex.Tokens) *TokenServer {
	t.Helper()
	s := &TokenServer{tokens: initial}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL, s.Client = s.server.URL, s.server.Client()
	t.Cleanup(s.server.Close)
	return s
}

// Queue supplies the next status and raw JSON body. Queued answers are consumed in order.
// Once exhausted, the server goes back to rotating tokens.
func (s *TokenServer) Queue(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = append(s.next, answer{status, body})
}

// Requests returns how many refresh requests the server has received.
func (s *TokenServer) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Tokens returns the latest tokens the default answer issued.
func (s *TokenServer) Tokens() codex.Tokens {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens
}

func (s *TokenServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	w.Header().Set("Content-Type", "application/json")
	var request struct {
		ClientID     string `json:"client_id"`
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request) != nil ||
		request.ClientID != codex.ClientID || request.GrantType != "refresh_token" || request.RefreshToken != s.tokens.RefreshToken {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	if len(s.next) != 0 {
		a := s.next[0]
		s.next = s.next[1:]
		if a.status == http.StatusOK {
			var tokens struct {
				Access  string `json:"access_token"`
				ID      string `json:"id_token"`
				Refresh string `json:"refresh_token"`
			}
			if json.Unmarshal([]byte(a.body), &tokens) == nil && tokens.Access != "" {
				s.tokens.AccessToken = tokens.Access
				if tokens.ID != "" {
					s.tokens.IDToken = tokens.ID
				}
				if tokens.Refresh != "" {
					s.tokens.RefreshToken = tokens.Refresh
				}
			}
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
		return
	}
	var payload struct {
		Profile struct {
			Email string `json:"email"`
		} `json:"https://api.openai.com/profile"`
		Auth struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	// These tokens were made by Tokens, so the envelope is always valid.
	parts, _ := base64.RawURLEncoding.DecodeString(strings.Split(s.tokens.IDToken, ".")[1])
	_ = json.Unmarshal(parts, &payload)
	now := time.Now()
	s.tokens = Tokens(s.tokens.AccountID, payload.Profile.Email, payload.Auth.Plan, now, now.Add(time.Hour))
	_ = json.NewEncoder(w).Encode(struct {
		ID      string `json:"id_token"`
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}{s.tokens.IDToken, s.tokens.AccessToken, s.tokens.RefreshToken})
}
