package codex_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
)

func TestAuthFileMatchesSerdeUnicodeEscaping(t *testing.T) {
	p := codex.PublicAuth{
		IDToken: "id", AccessToken: `\u2028`, RefreshToken: "refresh",
		AccountID: "account\u2028line\u2029tail", LastRefresh: time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC),
	}
	b, err := codex.RenderAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("\"account_id\": \"account\u2028line\u2029tail\"")) ||
		!bytes.Contains(b, []byte(`"access_token": "\\u2028"`)) {
		t.Fatal("auth file does not match serde_json's Unicode escaping")
	}
}
