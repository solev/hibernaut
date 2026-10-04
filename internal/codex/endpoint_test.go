package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/egress"
)

var endpointPlaceholders = codex.PublicAuth{
	IDToken: "id-placeholder", AccessToken: "access-placeholder", RefreshToken: "refresh-placeholder", AccountID: "account",
}

type fakeRefresher struct {
	mu        sync.Mutex
	events    []string
	reasons   []codex.Reason
	publicErr error
	freshErr  error
	fresh     codex.PublicAuth
}

func (f *fakeRefresher) Public() (codex.PublicAuth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publicErr != nil {
		return codex.PublicAuth{}, f.publicErr
	}
	return endpointPlaceholders, nil
}

func (f *fakeRefresher) EnsureFresh(_ context.Context, reason codex.Reason) (codex.PublicAuth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "refresh")
	f.reasons = append(f.reasons, reason)
	if f.freshErr != nil {
		return codex.PublicAuth{}, f.freshErr
	}
	if f.fresh != (codex.PublicAuth{}) {
		return f.fresh, nil
	}
	return endpointPlaceholders, nil
}

func (f *fakeRefresher) audit(fail error) func(string) error {
	return func(secret string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, "audit "+secret)
		return fail
	}
}

func (f *fakeRefresher) log() ([]string, []codex.Reason) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...), append([]codex.Reason(nil), f.reasons...)
}

func jsonRefresh(token string) egress.EndpointRequest {
	body, _ := json.Marshal(map[string]string{"client_id": codex.ClientID, "grant_type": "refresh_token", "refresh_token": token})
	return egress.EndpointRequest{Header: http.Header{"Content-Type": {"application/json"}}, Body: body}
}

func formRefresh(token string) egress.EndpointRequest {
	body := url.Values{"client_id": {codex.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {token}}.Encode()
	return egress.EndpointRequest{Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}, Body: []byte(body)}
}

func handleToken(t *testing.T, ep *codex.Endpoint, r *fakeRefresher, req egress.EndpointRequest, auditErr error) egress.EndpointResponse {
	t.Helper()
	e := ep.Egress()
	if req.Audit == nil {
		req.Audit = r.audit(auditErr)
	}
	resp := e.Handle(context.Background(), req)
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v, want no-store JSON", resp.Header)
	}
	return resp
}

func decodeAnswer(t *testing.T, resp egress.EndpointResponse) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("answer isn't JSON: %v", err)
	}
	return got
}

func TestTokenEndpointMatchesCodexRefresh(t *testing.T) {
	e := codex.NewEndpoint(&fakeRefresher{}, nil).Egress()
	if e.Site != "auth.openai.com" || e.Method != http.MethodPost || e.Path != "/oauth/token" || e.Handle == nil {
		t.Fatalf("endpoint = %s %s%s, want POST auth.openai.com/oauth/token", e.Method, e.Site, e.Path)
	}
}

func TestTokenEndpointAuditsThenRefreshes(t *testing.T) {
	fresh := codex.PublicAuth{IDToken: "new-id", AccessToken: "new-access", RefreshToken: "new-refresh", AccountID: "account"}
	for name, req := range map[string]egress.EndpointRequest{
		"json": jsonRefresh(endpointPlaceholders.RefreshToken),
		"form": formRefresh(endpointPlaceholders.RefreshToken),
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRefresher{fresh: fresh}
			resp := handleToken(t, codex.NewEndpoint(r, nil), r, req, nil)
			if resp.Status != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.Status)
			}
			got := decodeAnswer(t, resp)
			want := map[string]any{"id_token": "new-id", "access_token": "new-access", "refresh_token": "new-refresh"}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("answer = %v, want %v", got, want)
			}
			events, reasons := r.log()
			if strings.Join(events, ", ") != "audit codex-refresh, refresh" {
				t.Fatalf("events = %v, want the audit before the refresh", events)
			}
			if reasons[0] != (codex.Reason{Kind: codex.Client}) {
				t.Fatalf("reason = %+v, want Client with no rejected fingerprint", reasons[0])
			}
		})
	}
}

func TestTokenEndpointPassesTheLatestRejectedAccessToken(t *testing.T) {
	r := &fakeRefresher{}
	ep := codex.NewEndpoint(r, nil)
	ep.Rejected(codex.AccessName, "first")
	ep.Rejected("github", "other-secret")
	ep.Rejected(codex.AccessName, "latest")
	ep.Rejected(codex.RefreshName, "scrub-only")
	handleToken(t, ep, r, jsonRefresh(endpointPlaceholders.RefreshToken), nil)
	if _, reasons := r.log(); reasons[0] != (codex.Reason{Kind: codex.Client, Rejected: "latest"}) {
		t.Fatalf("reason = %+v, want Client with the latest codex-access fingerprint", reasons[0])
	}
}

func TestTokenEndpointRejectedIsSafeForConcurrentUse(t *testing.T) {
	r := &fakeRefresher{}
	ep := codex.NewEndpoint(r, nil)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() { ep.Rejected(codex.AccessName, fmt.Sprint(i)) })
		wg.Go(func() {
			ep.Egress().Handle(context.Background(), egress.EndpointRequest{Body: jsonRefresh(endpointPlaceholders.RefreshToken).Body, Audit: r.audit(nil)})
		})
	}
	wg.Wait()
}

func TestTokenEndpointFailsClosedWhenTheAuditFails(t *testing.T) {
	r := &fakeRefresher{}
	resp := handleToken(t, codex.NewEndpoint(r, nil), r, jsonRefresh(endpointPlaceholders.RefreshToken), errors.New("disk full"))
	if resp.Status != http.StatusServiceUnavailable || fmt.Sprint(decodeAnswer(t, resp)) != "map[error:temporarily_unavailable]" {
		t.Fatalf("status = %d, body %s, want 503 temporarily_unavailable", resp.Status, resp.Body)
	}
	if events, _ := r.log(); strings.Join(events, ", ") != "audit codex-refresh" {
		t.Fatalf("events = %v, want no refresh after a failed audit", events)
	}
}

func TestTokenEndpointRefreshErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{codex.ErrDead, http.StatusUnauthorized},
		{codex.ErrStuck, http.StatusUnauthorized},
		{codex.ErrNotConnected, http.StatusUnauthorized},
		{fmt.Errorf("refreshing: %w", codex.ErrDead), http.StatusUnauthorized},
		{codex.ErrUnavailable, http.StatusServiceUnavailable},
		{errors.New("reading codex vault"), http.StatusServiceUnavailable},
	} {
		t.Run(test.err.Error(), func(t *testing.T) {
			r := &fakeRefresher{freshErr: test.err}
			resp := handleToken(t, codex.NewEndpoint(r, nil), r, jsonRefresh(endpointPlaceholders.RefreshToken), nil)
			assertTokenError(t, resp, test.status)
			if events, _ := r.log(); strings.Join(events, ", ") != "audit codex-refresh, refresh" {
				t.Fatalf("events = %v, want the audit before the refresh", events)
			}
		})
	}
}

func assertTokenError(t *testing.T, resp egress.EndpointResponse, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("status = %d, want %d", resp.Status, status)
	}
	got := decodeAnswer(t, resp)
	switch status {
	case http.StatusUnauthorized:
		var answer struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(resp.Body, &answer); err != nil || answer.Error.Code != "refresh_token_invalidated" ||
			!strings.Contains(answer.Error.Message, "hib connect codex") {
			t.Fatalf("answer = %s, want refresh_token_invalidated that says to run hib connect codex", resp.Body)
		}
	case http.StatusServiceUnavailable:
		if fmt.Sprint(got) != "map[error:temporarily_unavailable]" {
			t.Fatalf("answer = %s, want temporarily_unavailable", resp.Body)
		}
	case http.StatusBadRequest:
		if fmt.Sprint(got) != "map[error:invalid_request]" {
			t.Fatalf("answer = %s, want invalid_request", resp.Body)
		}
	case http.StatusForbidden:
		if got["error"] != "access_denied" || got["error_description"] == "" {
			t.Fatalf("answer = %s, want access_denied with a description", resp.Body)
		}
	default:
		t.Fatalf("no check for status %d", status)
	}
}

func TestTokenEndpointRefusesBadRequests(t *testing.T) {
	jsonBody := func(body string) egress.EndpointRequest {
		return egress.EndpointRequest{Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(body)}
	}
	formBody := func(body string) egress.EndpointRequest {
		return egress.EndpointRequest{Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, Body: []byte(body)}
	}
	for name, test := range map[string]struct {
		req    egress.EndpointRequest
		status int
	}{
		"empty":                     {jsonBody(""), http.StatusBadRequest},
		"not json":                  {jsonBody("grant_type=refresh_token"), http.StatusBadRequest},
		"json array":                {jsonBody(`["refresh_token"]`), http.StatusBadRequest},
		"json grant not a string":   {jsonBody(`{"grant_type":1}`), http.StatusBadRequest},
		"json token not a string":   {jsonBody(`{"grant_type":"refresh_token","refresh_token":{}}`), http.StatusBadRequest},
		"json trailing data":        {jsonBody(`{"grant_type":"refresh_token"} {}`), http.StatusBadRequest},
		"json no grant":             {jsonBody(`{"refresh_token":"refresh-placeholder"}`), http.StatusBadRequest},
		"form no grant":             {formBody("refresh_token=refresh-placeholder"), http.StatusBadRequest},
		"form bad escape":           {formBody("grant_type=%zz"), http.StatusBadRequest},
		"other grant":               {jsonBody(`{"grant_type":"authorization_code","code":"x"}`), http.StatusForbidden},
		"form other grant":          {formBody("grant_type=client_credentials"), http.StatusForbidden},
		"no refresh token":          {jsonBody(`{"grant_type":"refresh_token"}`), http.StatusForbidden},
		"other refresh token":       {jsonRefresh("rt.someone-elses-token"), http.StatusForbidden},
		"another codex placeholder": {jsonRefresh(endpointPlaceholders.AccessToken), http.StatusForbidden},
		"form other refresh token":  {formRefresh("refresh-placeholder-old"), http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRefresher{}
			assertTokenError(t, handleToken(t, codex.NewEndpoint(r, nil), r, test.req, nil), test.status)
			if events, _ := r.log(); len(events) != 0 {
				t.Fatalf("events = %v, want no audit and no refresh", events)
			}
		})
	}
}

func TestTokenEndpointWhenNotConnected(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{codex.ErrNotConnected, http.StatusUnauthorized},
		{errors.New("reading codex placeholders"), http.StatusServiceUnavailable},
	} {
		r := &fakeRefresher{publicErr: test.err}
		assertTokenError(t, handleToken(t, codex.NewEndpoint(r, nil), r, jsonRefresh(endpointPlaceholders.RefreshToken), nil), test.status)
		if events, _ := r.log(); len(events) != 0 {
			t.Fatalf("events = %v, want no audit and no refresh", events)
		}
	}
}
