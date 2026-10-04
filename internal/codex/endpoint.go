package codex

import (
	"context"
	"net/http"

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
}

// NewEndpoint returns an endpoint that refreshes through r.
func NewEndpoint(r Refresher, logf func(format string, args ...any)) *Endpoint {
	return &Endpoint{refresher: r, logf: logf}
}

// Egress returns the endpoint for egress.Server.Endpoints.
func (e *Endpoint) Egress() egress.Endpoint {
	return egress.Endpoint{
		Site: TokenSite, Method: http.MethodPost, Path: TokenPath,
		Handle: func(context.Context, egress.EndpointRequest) egress.EndpointResponse {
			return egress.EndpointResponse{Status: http.StatusServiceUnavailable}
		},
	}
}

// Rejected notes that a site answered 401 to a swapped secret. Give it to
// egress.Server.Rejected.
func (e *Endpoint) Rejected(secret, fingerprint string) {}
