package hibapi

import (
	"fmt"
	"slices"
	"strings"
)

// GitHub is the GitHub account hibd connected. It never carries the token.
type GitHub struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	// Name is the profile's name, or the login if the profile has none.
	Name string `json:"name"`
	// Email is the account's noreply address, which machines commit as.
	Email string `json:"email"`
	// Source is how the token reached hibd: GitHubFromGH or GitHubFromToken.
	Source string `json:"source"`
	// Scopes are the token's OAuth scopes if ScopesKnown. GitHub doesn't report scopes for
	// fine-grained and app tokens.
	Scopes      []string `json:"scopes,omitempty"`
	ScopesKnown bool     `json:"scopesKnown,omitempty"`
}

const (
	// GitHubFromGH is hib connect github --from-gh: the laptop's gh login.
	GitHubFromGH = "gh"
	// GitHubFromToken is hib connect github --token: a token the user pasted.
	GitHubFromToken = "token"
)

// Reconnect is the command that connects GitHub again the way g was connected.
func (g GitHub) Reconnect() string {
	if g.Source == GitHubFromToken {
		return "hib connect github --token"
	}
	return "hib connect github --from-gh"
}

// Who is the login, then the name in parentheses when it differs.
func (g GitHub) Who() string {
	if g.Name == "" || g.Name == g.Login {
		return g.Login
	}
	return g.Login + " (" + g.Name + ")"
}

// ScopeWarning says what machines can't do because the token lacks the repo or workflow scope,
// and how to fix it. It's "" if the token has both, or if its scopes aren't known.
func (g GitHub) ScopeWarning() string {
	if !g.ScopesKnown {
		return ""
	}
	var missing []string
	for _, scope := range []string{"repo", "workflow"} {
		if !slices.Contains(g.Scopes, scope) {
			missing = append(missing, scope)
		}
	}
	var cant string
	switch {
	case len(missing) == 0:
		return ""
	case missing[0] == "repo":
		cant = "clone private repos or push"
	default:
		cant = "push changes under .github/workflows"
	}
	fix := fmt.Sprintf("run gh auth refresh -h github.com -s %s, then %s again", strings.Join(missing, ","), g.Reconnect())
	if g.Source == GitHubFromToken {
		noun := "scope"
		if len(missing) > 1 {
			noun = "scopes"
		}
		fix = fmt.Sprintf("make a classic token with the %s %s, then run %s again", strings.Join(missing, " and "), noun, g.Reconnect())
	}
	return fmt.Sprintf("the token has no %s scope, so machines can't %s. To fix it, %s.", strings.Join(missing, " or "), cant, fix)
}

// Status is the host's health, as hib status shows it.
type Status struct {
	// Version is hibd's release.
	Version string `json:"version"`
	E2B     Health `json:"e2b"`
	// GitHub is the connected account, or nil if GitHub isn't connected.
	GitHub *GitHub `json:"github,omitempty"`
	// Codex is the host's Codex login, or nil if Codex isn't connected. Dead or stuck isn't OK.
	Codex   *Codex         `json:"codex,omitempty"`
	Secrets []SecretHealth `json:"secrets"`
}

// OK reports whether s shows nothing broken: E2B is up, every secret is fine or has no check, and
// Codex, if connected, is ok.
func (s Status) OK() bool {
	if !s.E2B.OK {
		return false
	}
	for _, sec := range s.Secrets {
		if sec.State != SecretOK && sec.State != SecretUnchecked {
			return false
		}
	}
	return s.Codex == nil || s.Codex.Health == CodexOK
}

// Health is whether something works, and if not, why.
type Health struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// SecretState is what checking a secret against its site found.
type SecretState string

const (
	// SecretOK means the site accepted the secret.
	SecretOK SecretState = "ok"
	// SecretRevoked means the site refused the secret: it was revoked or has expired.
	SecretRevoked SecretState = "revoked"
	// SecretFailed means the check couldn't tell, for example because the site didn't answer.
	SecretFailed SecretState = "failed"
	// SecretUnchecked means hibd has no check for the secret's sites.
	SecretUnchecked SecretState = "unchecked"
)

// SecretHealth is one secret's check. It never carries the value.
type SecretHealth struct {
	Name   string      `json:"name"`
	Sites  []string    `json:"sites"`
	State  SecretState `json:"state"`
	Detail string      `json:"detail,omitempty"`
}
