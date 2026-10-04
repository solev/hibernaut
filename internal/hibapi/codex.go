package hibapi

import "time"

// CodexHealth is whether the host's Codex login can still be refreshed.
type CodexHealth string

const (
	// CodexOK means the login works, and hibd keeps it fresh.
	CodexOK CodexHealth = "ok"
	// CodexDead means OpenAI refused the refresh token. Only hib connect codex fixes it.
	CodexDead CodexHealth = "dead"
	// CodexStuck means a refresh has an unknown outcome, so hibd won't refresh by itself again.
	// The access token works until it expires. hib connect codex fixes it.
	CodexStuck CodexHealth = "stuck"
)

// Codex is the host's Codex login, as hib shows it. It never carries a token.
type Codex struct {
	Email     string      `json:"email"`
	Plan      string      `json:"plan"`
	AccountID string      `json:"accountId"`
	Health    CodexHealth `json:"health"`
	// Problem says why the login is dead or stuck. It's safe to show.
	Problem string `json:"problem,omitempty"`
	// Expires is when the real access token expires.
	Expires time.Time `json:"expires"`
	// LastRefresh is when hibd last got new tokens, by connecting or by refreshing.
	LastRefresh time.Time `json:"lastRefresh"`
}
