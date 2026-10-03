package vault_test

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/vault"
)

func TestPutKeepsJWTPlaceholderAcrossRenewals(t *testing.T) {
	for _, claim := range []string{
		"exp", "iat", "nbf", "jti", "auth_time", "nonce", "at_hash", "c_hash", "s_hash", "sid",
	} {
		t.Run(claim, func(t *testing.T) {
			key := newKey(t)
			var f vault.File
			claims := codexClaims()
			claims[claim] = "first"
			if slices.Contains([]string{"exp", "iat", "nbf", "auth_time"}, claim) {
				claims[claim] = int64(1791018000)
			}
			value := makeJWT(t, "first-key", claims, "a")
			first, err := f.Put(key, "codex", "CODEX_TOKEN", []string{"chatgpt.com"}, value)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "vault.json")
			if err := f.Save(path); err != nil {
				t.Fatal(err)
			}
			loaded, err := vault.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			claims[claim] = "renewed"
			if slices.Contains([]string{"exp", "iat", "nbf", "auth_time"}, claim) {
				claims[claim] = int64(1791021600)
			}
			renewed := makeJWT(t, "renewed-key", claims, "b")
			second, err := loaded.Put(key, "codex", "CODEX_TOKEN", []string{"chatgpt.com"}, renewed)
			if err != nil {
				t.Fatal(err)
			}
			if second.Placeholder != first.Placeholder {
				t.Fatalf("renewing %s invalidated the machine's placeholder", claim)
			}
			if !vault.Compatible(first.Placeholder, renewed) {
				t.Fatal("the held placeholder cannot stand for the renewed JWT")
			}
			secrets, err := loaded.Open(key)
			if err != nil || len(secrets) != 1 || secrets[0].Value != renewed || secrets[0].Placeholder != first.Placeholder {
				t.Fatalf("Open after renewal = %#v, %v, want the renewed token with the held placeholder", secrets, err)
			}
		})
	}
}

func TestJWTPlaceholderKeepsIdentityClaimsWithAFutureExpiry(t *testing.T) {
	value := makeJWT(t, "first-key", codexClaims(), "a")
	placeholder, err := vault.Placeholder(value, seededRand())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(placeholder, ".")
	if len(parts) != 3 {
		t.Fatalf("placeholder is not a JWT: %q", placeholder)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Expires       int64  `json:"exp"`
		Auth          struct {
			Account string `json:"chatgpt_account_id"`
			Plan    string `json:"chatgpt_plan_type"`
			User    string `json:"chatgpt_user_id"`
			Fedramp bool   `json:"chatgpt_account_is_fedramp"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Expires != 4102444800 {
		t.Fatalf("placeholder expiry = %d, want 2100-01-01", claims.Expires)
	}
	if claims.Subject != "user-one" || claims.Email != "user@example.com" || !claims.EmailVerified ||
		claims.Auth.Account != "account-one" || claims.Auth.Plan != "plus" ||
		claims.Auth.User != "user-one" || claims.Auth.Fedramp {
		t.Fatalf("placeholder changed the exposed identity claims: %+v", claims)
	}
	if parts[2] == strings.Repeat("a", 43) || !vault.Compatible(placeholder, value) {
		t.Fatal("placeholder did not replace the signature while retaining the credential identity")
	}
}

func TestPutReplacesPlaceholderWhenTheCredentialTypeChanges(t *testing.T) {
	for _, tc := range []struct {
		name, previous, renewed string
	}{
		{
			name: "known prefix", previous: "gho_" + strings.Repeat("a", 40),
			renewed: "ghp_" + strings.Repeat("b", 40),
		},
		{
			name: "shorter generic prefix", previous: "token-long-" + strings.Repeat("a", 40),
			renewed: "token-0000-" + strings.Repeat("B", 40),
		},
		{
			name: "JWT becomes opaque", previous: makeJWT(t, "first-key", codexClaims(), "a"),
			renewed: "at-" + strings.Repeat("b", 40),
		},
		{
			name: "opaque becomes JWT", previous: "at-" + strings.Repeat("a", 40),
			renewed: makeJWT(t, "renewed-key", codexClaims(), "b"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := newKey(t)
			var f vault.File
			first, err := f.Put(key, "credential", "TOKEN", []string{"example.com"}, tc.previous)
			if err != nil {
				t.Fatal(err)
			}
			second, err := f.Put(key, "credential", "TOKEN", []string{"example.com"}, tc.renewed)
			if err != nil {
				t.Fatal(err)
			}
			if second.Placeholder == first.Placeholder {
				t.Fatal("changing credential type kept the old placeholder")
			}
			if !vault.Compatible(second.Placeholder, tc.renewed) {
				t.Fatal("the replacement placeholder cannot stand for the new credential type")
			}
			secrets, err := f.Open(key)
			if err != nil || len(secrets) != 1 || secrets[0].Value != tc.renewed {
				t.Fatalf("Open after changing credential type = %#v, %v", secrets, err)
			}
		})
	}
}

func TestPutReplacesJWTPlaceholderWhenExposedIdentityChanges(t *testing.T) {
	for _, tc := range []struct {
		namespace, claim string
		renewed          any
	}{
		{claim: "sub", renewed: "user-two"},
		{claim: "email", renewed: "other@example.com"},
		{claim: "email_verified", renewed: false},
		{claim: "iss", renewed: "https://other.example.com"},
		{claim: "aud", renewed: "another-client"},
		{claim: "roles", renewed: []string{"admin"}},
		{namespace: "https://api.openai.com/auth", claim: "chatgpt_account_id", renewed: "account-two"},
		{namespace: "https://api.openai.com/auth", claim: "chatgpt_plan_type", renewed: "pro"},
		{namespace: "https://api.openai.com/auth", claim: "chatgpt_user_id", renewed: "user-two"},
		{namespace: "https://api.openai.com/auth", claim: "chatgpt_account_is_fedramp", renewed: true},
	} {
		t.Run(tc.claim, func(t *testing.T) {
			key := newKey(t)
			var f vault.File
			claims := codexClaims()
			first, err := f.Put(key, "codex", "CODEX_TOKEN", []string{"chatgpt.com"},
				makeJWT(t, "first-key", claims, "a"))
			if err != nil {
				t.Fatal(err)
			}
			target := claims
			if tc.namespace != "" {
				target = claims[tc.namespace].(map[string]any)
			}
			target[tc.claim] = tc.renewed
			renewed := makeJWT(t, "first-key", claims, "b")
			second, err := f.Put(key, "codex", "CODEX_TOKEN", []string{"chatgpt.com"}, renewed)
			if err != nil {
				t.Fatal(err)
			}
			if second.Placeholder == first.Placeholder || vault.Compatible(first.Placeholder, renewed) {
				t.Fatalf("changing %s kept the old exposed identity", tc.claim)
			}
			if !vault.Compatible(second.Placeholder, renewed) {
				t.Fatal("replacement placeholder does not carry the renewed identity")
			}
			secrets, err := f.Open(key)
			if err != nil || len(secrets) != 1 || secrets[0].Value != renewed || secrets[0].Placeholder != second.Placeholder {
				t.Fatalf("Open after an identity change = %#v, %v", secrets, err)
			}
		})
	}
}

func TestCompatibleUsesDecodedJWTIdentityClaims(t *testing.T) {
	previous := makeJWT(t, "old-key", map[string]any{
		"sub": "user-one", "account": int64(9007199254740992), "exp": int64(1791018000),
	}, "a")
	placeholder, err := vault.Placeholder(previous, seededRand())
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"kid":"new-key","alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		`{ "exp": 1791021600, "account": 9007199254740992, "sub": "user-one" }`))
	renewed := header + "." + payload + "." + strings.Repeat("b", 43)
	if !vault.Compatible(placeholder, renewed) {
		t.Fatal("claim order, whitespace or signing-key rotation changed the credential identity")
	}
	otherAccount := makeJWT(t, "new-key", map[string]any{
		"sub": "user-one", "account": int64(9007199254740993), "exp": int64(1791021600),
	}, "b")
	if vault.Compatible(placeholder, otherAccount) {
		t.Fatal("different numeric account IDs became the same credential identity")
	}
}

func TestPutKeepsOpaquePlaceholderWhenOnlyLengthChanges(t *testing.T) {
	key := newKey(t)
	var f vault.File
	first, err := f.Put(key, "github", "GH_TOKEN", []string{"github.com"}, "gho_"+strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{24, 80} {
		renewed := "gho_" + strings.Repeat("b", size)
		next, err := f.Put(key, "github", "GH_TOKEN", []string{"github.com"}, renewed)
		if err != nil {
			t.Fatal(err)
		}
		if next.Placeholder != first.Placeholder || len(next.Placeholder) != 44 {
			t.Fatal("a same-prefix renewal changed the placeholder or its original length")
		}
		secrets, err := f.Open(key)
		if err != nil || len(secrets) != 1 || secrets[0].Value != renewed {
			t.Fatalf("Open after a length change = %#v, %v", secrets, err)
		}
	}
}

func codexClaims() map[string]any {
	return map[string]any{
		"sub": "user-one", "email": "user@example.com", "email_verified": true,
		"exp": int64(1791018000),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-one", "chatgpt_plan_type": "plus",
			"chatgpt_user_id": "user-one", "chatgpt_account_is_fedramp": false,
		},
	}
}

func makeJWT(t *testing.T, keyID string, claims map[string]any, signature string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": keyID})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." + strings.Repeat(signature, 43)
}
