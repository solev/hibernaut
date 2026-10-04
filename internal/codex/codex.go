// Package codex keeps the host's Codex login on a ChatGPT plan. The real tokens live in the
// vault, the profile beside it, and machines get a login file with placeholders. Only a Store
// changes the login, and only a Store talks to OpenAI.
package codex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

// Version is the Codex release the image and the host install. images/dev/versions pins the
// same one.
const Version = "0.160.0"

const (
	// AccessName is the secret holding the access token. It's swapped on AccessSites.
	AccessName = "codex-access"
	// RefreshName is the secret holding the refresh token. It's scrub-only.
	RefreshName = "codex-refresh"
	// IDName is the secret holding the ID token. It's scrub-only.
	IDName = "codex-id"
	// NamePrefix starts every secret this package manages. hib secret refuses such names.
	NamePrefix = "codex-"

	// TokenSite and TokenPath are where Codex refreshes, and where hibd does.
	TokenSite = "auth.openai.com"
	TokenPath = "/oauth/token"
	// TokenURL is OpenAI's token endpoint.
	TokenURL = "https://" + TokenSite + TokenPath
	// ClientID is Codex's public OAuth client ID.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// ProfileFile, RetiredFile and LockFile are in hibd's state directory.
	ProfileFile = "codex.json"
	RetiredFile = "codex-retired.json"
	LockFile    = "codex.lock"
)

// AccessSites are where the access token is swapped.
func AccessSites() []string { return []string{"chatgpt.com"} }

// AuthSites are the refresh and ID tokens' sites. Those secrets are scrub-only, so binding them
// intercepts the site for the token endpoint without swapping anything there.
func AuthSites() []string { return []string{TokenSite} }

// Managed reports whether name is a secret this package owns.
func Managed(name string) bool { return strings.HasPrefix(name, NamePrefix) }

// ScrubOnly reports whether the named secret must never be swapped into requests.
func ScrubOnly(name string) bool { return name == RefreshName || name == IDName }

// Health is whether the login can still be refreshed.
type Health = hibapi.CodexHealth

// The login's health.
const (
	HealthOK    = hibapi.CodexOK
	HealthDead  = hibapi.CodexDead
	HealthStuck = hibapi.CodexStuck
)

var (
	// ErrNotConnected means Codex isn't connected.
	ErrNotConnected = errors.New("codex is not connected")
	// ErrDead means OpenAI refused the refresh token.
	ErrDead = errors.New("the codex login has ended")
	// ErrStuck means a refresh has an unknown outcome, so hibd won't refresh by itself.
	ErrStuck = errors.New("a codex refresh has an unknown outcome")
	// ErrUnavailable means a refresh is needed but can't happen now. Try again later.
	ErrUnavailable = errors.New("codex can't refresh right now")
)

// Tokens are a login's real tokens. They never print their values.
type Tokens struct {
	IDToken, AccessToken, RefreshToken, AccountID string
}

// String returns a redacted marker.
func (Tokens) String() string { return "[redacted codex tokens]" }

// GoString returns a redacted marker.
func (t Tokens) GoString() string { return t.String() }

// Format prints a redacted marker.
func (t Tokens) Format(s fmt.State, verb rune) { fmt.Fprint(s, t.String()) }

// MarshalJSON always refuses, so tokens never reach a file or a reply by mistake.
func (Tokens) MarshalJSON() ([]byte, error) {
	return nil, errors.New("codex tokens cannot be marshaled")
}

// PublicAuth is what machines get: placeholders for the three tokens, and the real account ID.
type PublicAuth struct {
	IDToken, AccessToken, RefreshToken string
	AccountID                          string
	// LastRefresh is the login file's last_refresh: the profile's Published.
	LastRefresh time.Time
}

// RenderAuthFile returns the login file machines get, byte for byte in the layout Codex writes.
func RenderAuthFile(p PublicAuth) ([]byte, error) { return renderAuthFile(p) }

// ParseAuthFile reads the tokens from a login file codex login wrote.
func ParseAuthFile(data []byte) (Tokens, error) { return parseAuthFile(data) }

// Profile is the login as hibd keeps it in codex.json. It never holds a token.
type Profile struct {
	Email       string    `json:"email"`
	Plan        string    `json:"plan"`
	AccountID   string    `json:"accountId"`
	ConnectedAt time.Time `json:"connectedAt"`
	// Expires is when the real access token expires.
	Expires time.Time `json:"expires"`
	// LastRefresh is when hibd last got new tokens, by connecting or by refreshing.
	LastRefresh time.Time `json:"lastRefresh"`
	// Published is when a placeholder last changed. It's the login file's last_refresh.
	Published time.Time `json:"published"`
	Health    Health    `json:"health"`
	// Problem says why the login is dead or stuck. It's safe to show.
	Problem string `json:"problem,omitempty"`
	// Attempt is a refresh or login in progress. It's written before sending or publishing.
	Attempt *Attempt `json:"attempt,omitempty"`
	// RetryAt is when Background may try again after a transient failure.
	RetryAt  time.Time `json:"retryAt,omitzero"`
	Failures int       `json:"failures,omitempty"`
	// ClientRefreshAt is when a machine's refresh request last started a real refresh.
	ClientRefreshAt time.Time `json:"clientRefreshAt,omitzero"`
}

// Attempt is a refresh or replacement login that started.
type Attempt struct {
	// Generation is the fingerprint of the refresh token being sent or replaced.
	Generation string    `json:"generation"`
	Started    time.Time `json:"started"`
	// Login means a device login is replacing the profile, not refreshing it.
	Login bool `json:"login,omitempty"`
}

// API returns the login as hib shows it.
func (p Profile) API() hibapi.Codex {
	return hibapi.Codex{
		Email: p.Email, Plan: p.Plan, AccountID: p.AccountID, Health: p.Health, Problem: p.Problem,
		Expires: p.Expires, LastRefresh: p.LastRefresh,
	}
}

// ReasonKind is why EnsureFresh might refresh.
type ReasonKind int

const (
	// Background is the daemon's timer.
	Background ReasonKind = iota
	// Client is a machine's refresh request, answered by the proxy.
	Client
	// Force is hibd codex refresh -force.
	Force
)

// Reason is why EnsureFresh was called.
type Reason struct {
	Kind ReasonKind
	// Rejected is, for Client, the fingerprint (swap.Fingerprint) of the access token a site
	// last answered 401 to, or "".
	Rejected string
}

// Config is what a Store needs.
type Config struct {
	// StateDir holds ProfileFile, RetiredFile and LockFile.
	StateDir string
	// VaultPath and KeyPath are the vault and its key.
	VaultPath, KeyPath string
	// TokenURL and Client are for tests only. Empty means TokenURL, and nil means a client
	// with a 30 second timeout that refuses redirects.
	TokenURL string
	Client   *http.Client
	Now      func() time.Time
	Logf     func(format string, args ...any)
}

// Store keeps the login. Its methods may run at once in many goroutines and processes.
type Store struct {
	cfg Config
}

// NewStore returns a Store for cfg.
func NewStore(cfg Config) *Store {
	if cfg.TokenURL == "" {
		cfg.TokenURL = TokenURL
	}
	client := http.Client{Timeout: 30 * time.Second}
	if cfg.Client != nil {
		client = *cfg.Client
	}
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.Client = &client
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Store{cfg: cfg}
}

// Load returns the profile, or ErrNotConnected. It never waits for the lock and never writes.
// An interrupted refresh shows as published or stuck. An unpublished login keeps the old profile.
func (s *Store) Load() (Profile, error) { return s.load() }

// Public returns what the login file holds, or ErrNotConnected. It needs no lock.
func (s *Store) Public() (PublicAuth, error) { return s.public() }

// EnsureFresh refreshes the real login if reason calls for it, then returns Public.
func (s *Store) EnsureFresh(ctx context.Context, reason Reason) (PublicAuth, error) {
	return s.ensureFresh(ctx, reason)
}

// CommitLogin replaces the login with a new device login's tokens. It may be another account.
func (s *Store) CommitLogin(ctx context.Context, t Tokens) (Profile, error) {
	return s.commitLogin(ctx, t)
}

// OldValue is a replaced real value the leak scan still looks for. It never prints its value.
type OldValue struct {
	// Name is the secret the value was in.
	Name  string
	Value string
}

// String names the secret and redacts the value.
func (o OldValue) String() string { return "[redacted old value of " + o.Name + "]" }

// GoString names the secret and redacts the value.
func (o OldValue) GoString() string { return o.String() }

// Format names the secret and redacts the value.
func (o OldValue) Format(s fmt.State, verb rune) { fmt.Fprint(s, o.String()) }

// MarshalJSON always refuses, so old values never reach a file or a reply by mistake.
func (OldValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("codex old values cannot be marshaled")
}

// Retired returns the replaced values kept for the leak scan.
func (s *Store) Retired() ([]OldValue, error) { return s.retired() }
