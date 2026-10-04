package codex

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"sync"

	"github.com/solev/hibernaut/internal/egress"
)

// Refresher is what the token endpoint needs from a Store.
type Refresher interface {
	EnsureFresh(ctx context.Context, reason Reason) (PublicAuth, error)
	Public() (PublicAuth, error)
}

// Endpoint answers Codex's refresh requests in the proxy, so they never reach OpenAI.
type Endpoint struct {
	refresher Refresher
	logf      func(format string, args ...any)

	mu       sync.Mutex
	rejected string
}

// NewEndpoint returns an endpoint that refreshes through r.
func NewEndpoint(r Refresher, logf func(format string, args ...any)) *Endpoint {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Endpoint{refresher: r, logf: logf}
}

// Egress returns the endpoint for egress.Server.Endpoints.
func (e *Endpoint) Egress() egress.Endpoint {
	return egress.Endpoint{Site: TokenSite, Method: http.MethodPost, Path: TokenPath, Handle: e.handle}
}

// Rejected notes that a site answered 401 to a swapped secret. Give it to
// egress.Server.Rejected.
func (e *Endpoint) Rejected(secret, fingerprint string) {
	if secret != AccessName {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rejected = fingerprint
}

func (e *Endpoint) lastRejected() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rejected
}

const reconnectMessage = "The Codex login on the Hibernaut host can't be refreshed. Run hib connect codex on your laptop to log in again."

func (e *Endpoint) handle(ctx context.Context, req egress.EndpointRequest) egress.EndpointResponse {
	grant, token, err := parseTokenRequest(req)
	if err != nil {
		return endpointAnswer(http.StatusBadRequest, map[string]string{"error": "invalid_request"})
	}
	if grant != "refresh_token" {
		return accessDenied("only refresh_token grants are answered here")
	}
	current, err := e.refresher.Public()
	switch {
	case errors.Is(err, ErrNotConnected):
		return loginEnded()
	case err != nil:
		e.logf("codex token endpoint: %v", err)
		return tryLater()
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(current.RefreshToken)) != 1 {
		return accessDenied("the refresh token is not this host's Codex login")
	}
	if req.Audit == nil || req.Audit(RefreshName) != nil {
		return tryLater()
	}
	fresh, err := e.refresher.EnsureFresh(ctx, Reason{Kind: Client, Rejected: e.lastRejected()})
	switch {
	case err == nil:
		return endpointAnswer(http.StatusOK, map[string]string{
			"id_token": fresh.IDToken, "access_token": fresh.AccessToken, "refresh_token": fresh.RefreshToken,
		})
	case errors.Is(err, ErrDead), errors.Is(err, ErrStuck), errors.Is(err, ErrNotConnected):
		e.logf("codex token endpoint: %v", err)
		return loginEnded()
	default:
		e.logf("codex token endpoint: %v", err)
		return tryLater()
	}
}

func parseTokenRequest(req egress.EndpointRequest) (grant, token string, err error) {
	mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if mediaType == "application/x-www-form-urlencoded" {
		values, err := url.ParseQuery(string(req.Body))
		if err != nil {
			return "", "", err
		}
		grant, token = values.Get("grant_type"), values.Get("refresh_token")
	} else {
		var body struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return "", "", err
		}
		grant, token = body.GrantType, body.RefreshToken
	}
	if grant == "" {
		return "", "", errors.New("no grant_type")
	}
	return grant, token, nil
}

func loginEnded() egress.EndpointResponse {
	return endpointAnswer(http.StatusUnauthorized, map[string]map[string]string{
		"error": {"code": "refresh_token_invalidated", "message": reconnectMessage},
	})
}

func tryLater() egress.EndpointResponse {
	return endpointAnswer(http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
}

func accessDenied(description string) egress.EndpointResponse {
	return endpointAnswer(http.StatusForbidden, map[string]string{"error": "access_denied", "error_description": description})
}

func endpointAnswer(status int, body any) egress.EndpointResponse {
	data, _ := json.Marshal(body)
	return egress.EndpointResponse{
		Status: status,
		Header: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}},
		Body:   data,
	}
}
