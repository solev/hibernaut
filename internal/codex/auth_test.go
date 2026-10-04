package codex_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
)

func TestAuthFileMatchesCodexSerialization(t *testing.T) {
	p := codex.PublicAuth{IDToken: "id", AccessToken: "access", RefreshToken: "refresh", AccountID: "<account>", LastRefresh: time.Date(2026, 10, 4, 8, 0, 0, 120000000, time.UTC)}
	want := `{
  "auth_mode": "chatgpt",
  "OPENAI_API_KEY": null,
  "tokens": {
    "id_token": "id",
    "access_token": "access",
    "refresh_token": "refresh",
    "account_id": "<account>"
  },
  "last_refresh": "2026-10-04T08:00:00.120Z"
}`
	got, err := codex.RenderAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatal("login file does not match Codex's pretty JSON layout")
	}
}

func TestParseAuthFileRequiresChatGPTLogin(t *testing.T) {
	id := jwt(map[string]any{"email": "person@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	access := jwt(map[string]any{"exp": int64(1791100000), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account"}})
	valid := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"` + id + `","access_token":"` + access + `","refresh_token":"rt.` + strings.Repeat("Az", 24) + `","account_id":"account"}}`
	got, err := codex.ParseAuthFile([]byte(valid))
	if err != nil || got.IDToken != id || got.AccessToken != access || got.AccountID != "account" {
		t.Fatal("valid ChatGPT login was not parsed")
	}
	for _, input := range []string{
		`{`, `{}`, `{"auth_mode":"apikey","OPENAI_API_KEY":"secret"}`,
		strings.Replace(valid, `"chatgpt"`, `"chatgptAuthTokens"`, 1),
		strings.Replace(valid, `"account_id":"account"`, `"account_id":null`, 1),
		strings.Replace(valid, `"account_id":"account"`, `"account_id":"other"`, 1),
		strings.Replace(valid, `"id_token":"`+id+`"`, `"id_token":""`, 1),
		strings.Replace(valid, `"access_token":"`+access+`"`, `"access_token":"broken"`, 1),
	} {
		_, err := codex.ParseAuthFile([]byte(input))
		if err == nil {
			t.Fatal("invalid login was accepted")
		}
		for _, token := range []string{id, access, got.RefreshToken} {
			if strings.Contains(err.Error(), token) {
				t.Fatal("parse error exposed a token")
			}
		}
	}
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + "." + strings.Repeat("Z", 48)
}
