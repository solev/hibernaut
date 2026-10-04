package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/termsafe"
)

type authTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

type authFile struct {
	Mode        string      `json:"auth_mode"`
	APIKey      *string     `json:"OPENAI_API_KEY"`
	Tokens      *authTokens `json:"tokens"`
	LastRefresh string      `json:"last_refresh,omitempty"`
}

func renderAuthFile(p PublicAuth) ([]byte, error) {
	t := p.LastRefresh.UTC()
	precision := 0
	if ns := t.Nanosecond(); ns != 0 {
		switch {
		case ns%1000000 == 0:
			precision = 3
		case ns%1000 == 0:
			precision = 6
		default:
			precision = 9
		}
	}
	layout := "2006-01-02T15:04:05"
	if precision != 0 {
		layout += "." + strings.Repeat("0", precision)
	}
	layout += "Z"
	f := authFile{
		Mode: "chatgpt", Tokens: &authTokens{p.IDToken, p.AccessToken, p.RefreshToken, p.AccountID},
		LastRefresh: t.Format(layout),
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(f); err != nil {
		return nil, errors.New("rendering codex login file")
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func parseAuthFile(data []byte) (Tokens, error) {
	var f authFile
	if len(data) > 1<<20 || json.Unmarshal(data, &f) != nil || f.Mode != "chatgpt" || f.APIKey != nil || f.Tokens == nil {
		return Tokens{}, errors.New("invalid codex login file: run hib connect codex")
	}
	t := Tokens{IDToken: f.Tokens.IDToken, AccessToken: f.Tokens.AccessToken, RefreshToken: f.Tokens.RefreshToken, AccountID: f.Tokens.AccountID}
	if _, err := tokenProfile(t); err != nil {
		return Tokens{}, err
	}
	return t, nil
}

type claims struct {
	Email   string `json:"email"`
	Expires int64  `json:"exp"`
	Issued  int64  `json:"iat"`
	Profile struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth struct {
		Plan    string `json:"chatgpt_plan_type"`
		Account string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

func decodeClaims(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return claims{}, errors.New("invalid codex token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var c claims
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return claims{}, errors.New("invalid codex token claims")
	}
	return c, nil
}

func tokenProfile(t Tokens) (Profile, error) {
	for _, value := range []string{t.IDToken, t.AccessToken, t.RefreshToken} {
		if len(value) == 0 || len(value) > 64<<10 {
			return Profile{}, errors.New("incomplete codex login: run hib connect codex")
		}
		for _, c := range []byte(value) {
			if c < 0x21 || c > 0x7e {
				return Profile{}, errors.New("invalid codex token: run hib connect codex")
			}
		}
	}
	id, err := decodeClaims(t.IDToken)
	if err != nil {
		return Profile{}, err
	}
	access, err := decodeClaims(t.AccessToken)
	if err != nil {
		return Profile{}, err
	}
	if t.AccountID == "" || access.Auth.Account != t.AccountID || access.Expires <= 0 {
		return Profile{}, errors.New("invalid codex account or expiry: run hib connect codex")
	}
	email := id.Email
	if email == "" {
		email = id.Profile.Email
	}
	for _, metadata := range []string{email, id.Auth.Plan, t.AccountID} {
		for _, token := range []string{t.IDToken, t.AccessToken, t.RefreshToken} {
			if strings.Contains(metadata, token) {
				return Profile{}, errors.New("invalid codex profile: run hib connect codex")
			}
		}
	}
	return Profile{
		Email: termsafe.String(email), Plan: termsafe.String(id.Auth.Plan), AccountID: t.AccountID,
		Expires: time.Unix(access.Expires, 0).UTC(), Health: HealthOK,
	}, nil
}
