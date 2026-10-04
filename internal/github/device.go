package github

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	// OAuthBase is GitHub's site, where the device flow asks for a code and polls for the token.
	OAuthBase = "https://github.com"
	// ClientID is the client ID of the Hibernaut OAuth App, which the device flow logs in to. It
	// isn't secret. It's "" until the app exists, and until then the device flow can't start.
	ClientID = ""
)

// DeviceScopes returns the scopes the device flow asks for: repo to clone and push, read:org for
// gh's org commands, and workflow to push changes under .github/workflows. Each call returns a
// new slice.
func DeviceScopes() []string {
	return []string{"repo", "read:org", "workflow"}
}

var (
	// ErrNoClientID means the device flow has no OAuth App client ID, so it can't start.
	ErrNoClientID = errors.New("no GitHub OAuth App client ID")
	// ErrCodeExpired means the code expired before the user approved it.
	ErrCodeExpired = errors.New("the GitHub code expired")
	// ErrAccessDenied means the user denied the login on GitHub.
	ErrAccessDenied = errors.New("the GitHub login was denied")
	// ErrAppRefused means GitHub refused the OAuth App: its Device Flow is off, or its client ID
	// is wrong.
	ErrAppRefused = errors.New("GitHub refused the OAuth App")
	// ErrExpiringToken means GitHub gave a token that expires. hibd can't refresh tokens, so it
	// refuses them.
	ErrExpiringToken = errors.New("GitHub gave a token that expires")
)

// DevicePrompt is what the user needs to approve a device login: the code to enter at
// hibapi.GitHubDeviceURL, and how long it lasts.
type DevicePrompt struct {
	UserCode  string
	ExpiresIn time.Duration
}

// DeviceFlow logs in to an OAuth App with GitHub's device flow. Its zero value has no client ID,
// so its Login fails with ErrNoClientID.
type DeviceFlow struct {
	// Client is the HTTP client. If it's nil, Login uses NewClient(). Login never follows a
	// redirect, even if Client does.
	Client *http.Client
	// Base is GitHub's site. If it's "", Login uses OAuthBase.
	Base string
	// ClientID is the OAuth App's client ID. If it's "", Login fails with ErrNoClientID. It never
	// falls back to the ClientID constant.
	ClientID string
	// Now is the clock. If it's nil, Login uses time.Now.
	Now func() time.Time
	// Sleep waits for d, or until ctx is done, when it returns ctx's error. If it's nil, Login
	// uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Login runs the device flow for DeviceScopes. It asks GitHub for a code, calls prompt once with
// it, then polls until the user approves or denies it, the code expires, or ctx is done. It
// returns a token that passes CheckToken. Its errors wrap ErrNoClientID, ErrCodeExpired,
// ErrAccessDenied, ErrAppRefused, ErrExpiringToken or ctx's error where they apply, and never
// quote the device code or the token.
func (f *DeviceFlow) Login(ctx context.Context, prompt func(DevicePrompt)) (string, error) {
	return "", errors.New("the device flow isn't built yet")
}
