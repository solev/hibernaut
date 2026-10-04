package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/egress/swap"
)

const (
	endpointPath = "/oauth/token"
	rotatedValue = "gho_real_rotated_secret"
)

// endpointRecorder is an endpoint handler that keeps what it got.
type endpointRecorder struct {
	mu       sync.Mutex
	requests []EndpointRequest
	answer   func(ctx context.Context, r EndpointRequest) EndpointResponse
}

func (e *endpointRecorder) handle(ctx context.Context, r EndpointRequest) EndpointResponse {
	e.mu.Lock()
	e.requests = append(e.requests, r)
	e.mu.Unlock()
	if e.answer != nil {
		return e.answer(ctx, r)
	}
	return EndpointResponse{Status: http.StatusOK, Body: []byte("handled")}
}

func (e *endpointRecorder) calls() []EndpointRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]EndpointRequest(nil), e.requests...)
}

func newEndpointFixture(t *testing.T, rec *endpointRecorder, upstreamCalls *atomic.Int32, configure func(*Server)) *fixture {
	t.Helper()
	return newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(w, "forwarded")
	}), func(s *Server) {
		s.Endpoints = []Endpoint{{Site: "LOCALHOST.", Method: http.MethodPost, Path: endpointPath, Handle: rec.handle}}
		if configure != nil {
			configure(s)
		}
	})
}

func TestEndpointAnswersItselfAndScrubs(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		name := "HTTP/1.1"
		if h2 {
			name = "HTTP/2"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var upstreamCalls atomic.Int32
			rec := &endpointRecorder{answer: func(context.Context, EndpointRequest) EndpointResponse {
				return EndpointResponse{
					Status: http.StatusTeapot,
					Header: http.Header{"Content-Type": {"application/json"}, "X-Echo": {realValue}, "Cache-Control": {"max-age=60"}},
					Body:   []byte(`{"token":"` + realValue + `"}`),
				}
			}}
			fx := newEndpointFixture(t, rec, &upstreamCalls, nil)
			defer fx.close()
			req, err := http.NewRequest(http.MethodPost, fx.url(endpointPath+"?q=1"), strings.NewReader(`{"refresh_token":"`+placeholder+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+placeholder)
			req.Header.Set("Content-Type", "application/json")
			resp, err := fx.client(h2).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if wantProto := map[bool]string{false: "HTTP/1.1", true: "HTTP/2.0"}[h2]; resp.Proto != wantProto {
				t.Fatalf("proto = %s, want %s", resp.Proto, wantProto)
			}
			if resp.StatusCode != http.StatusTeapot || string(data) != `{"token":"`+placeholder+`"}` ||
				resp.Header.Get("X-Echo") != placeholder || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("answer = %d, scrubbed body %v, scrubbed header %v", resp.StatusCode,
					string(data) == `{"token":"`+placeholder+`"}`, resp.Header.Get("X-Echo") == placeholder)
			}
			if got := resp.Header.Values("Cache-Control"); len(got) != 1 || got[0] != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if upstreamCalls.Load() != 0 {
				t.Fatal("an endpoint request was forwarded")
			}
			calls := rec.calls()
			if len(calls) != 1 {
				t.Fatalf("handler calls = %d, want 1", len(calls))
			}
			if string(calls[0].Body) != `{"refresh_token":"`+placeholder+`"}` ||
				calls[0].Header.Get("Authorization") != "Bearer "+placeholder ||
				calls[0].Header.Get("Content-Type") != "application/json" {
				t.Fatal("the handler didn't get the request as the machine sent it, with placeholders")
			}
		})
	}
}

func TestEndpointMatchesExactSiteMethodAndPath(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	rec := &endpointRecorder{}
	fx := newEndpointFixture(t, rec, &upstreamCalls, func(s *Server) {
		s.Endpoints = append(s.Endpoints, Endpoint{Site: "other.example", Method: http.MethodPost, Path: "/other", Handle: rec.handle})
	})
	defer fx.close()
	client := fx.client(false)
	for _, tc := range []struct {
		method, path string
		handled      bool
	}{
		{http.MethodPost, endpointPath, true},
		{http.MethodPost, endpointPath + "?grant_type=x", true},
		{http.MethodGet, endpointPath, false},
		{http.MethodPut, endpointPath, false},
		{http.MethodPost, endpointPath + "/", false},
		{http.MethodPost, "/OAUTH/token", false},
		{http.MethodPost, "/other", false},
	} {
		before := len(rec.calls())
		req, err := http.NewRequest(tc.method, fx.url(tc.path), strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		handled := len(rec.calls()) > before
		if handled != tc.handled || (string(data) == "handled") != tc.handled {
			t.Errorf("%s %s: handled = %v, body %q, want handled %v", tc.method, tc.path, handled, data, tc.handled)
		}
	}
	if got := upstreamCalls.Load(); got != 5 {
		t.Fatalf("upstream calls = %d, want 5", got)
	}
}

func TestEndpointBodyLimits(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP/1.1", true: "HTTP/2"}[h2], func(t *testing.T) {
			t.Parallel()
			var upstreamCalls atomic.Int32
			rec := &endpointRecorder{}
			fx := newEndpointFixture(t, rec, &upstreamCalls, func(s *Server) { s.endpointReadTimeout = 200 * time.Millisecond })
			defer fx.close()

			post := func(body io.Reader) *http.Response {
				t.Helper()
				req, err := http.NewRequest(http.MethodPost, fx.url(endpointPath), body)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := fx.client(h2).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				return resp
			}
			if resp := post(strings.NewReader(strings.Repeat("a", MaxEndpointBody))); resp.StatusCode != http.StatusOK {
				t.Fatalf("a body of exactly MaxEndpointBody: status %d", resp.StatusCode)
			}
			if got := rec.calls(); len(got) != 1 || len(got[0].Body) != MaxEndpointBody {
				t.Fatal("the handler didn't get the whole body")
			}

			resp := post(strings.NewReader(strings.Repeat("a", MaxEndpointBody+1)))
			if resp.StatusCode != http.StatusRequestEntityTooLarge || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("too large: status %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
			}

			slow, writer := io.Pipe()
			defer writer.Close()
			go func() { _, _ = writer.Write([]byte(`{"grant_type":`)) }()
			resp = post(slow)
			if resp.StatusCode != http.StatusRequestTimeout || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("too slow: status %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
			}
			if len(rec.calls()) != 1 || upstreamCalls.Load() != 0 {
				t.Fatalf("handler calls = %d, upstream calls = %d, want 1 and 0", len(rec.calls()), upstreamCalls.Load())
			}
		})
	}
}

func TestEndpointReadTimeoutDoesNotLimitTheHandler(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	rec := &endpointRecorder{answer: func(ctx context.Context, r EndpointRequest) EndpointResponse {
		select {
		case <-ctx.Done():
			return EndpointResponse{Status: http.StatusGatewayTimeout}
		case <-time.After(300 * time.Millisecond):
		}
		return EndpointResponse{Status: http.StatusOK, Body: []byte("slow handler")}
	}}
	fx := newEndpointFixture(t, rec, &upstreamCalls, func(s *Server) { s.endpointReadTimeout = 100 * time.Millisecond })
	defer fx.close()
	for _, h2 := range []bool{false, true} {
		client := fx.client(h2)
		for range 2 {
			resp, err := client.Post(fx.url(endpointPath), "text/plain", strings.NewReader("x"))
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(data) != "slow handler" || (resp.ProtoMajor == 2) != h2 {
				t.Fatalf("HTTP/%d: status %d, body %q", resp.ProtoMajor, resp.StatusCode, data)
			}
		}
	}
}

func TestEndpointAuditHasTheMachineAndFailsClosed(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	var mu sync.Mutex
	var entries []audit.Entry
	var failAudit atomic.Bool
	auditErr := errors.New("disk full")
	rec := &endpointRecorder{answer: func(_ context.Context, r EndpointRequest) EndpointResponse {
		if err := r.Audit("codex-refresh"); err != nil {
			if !errors.Is(err, auditErr) {
				t.Errorf("Audit error = %v, want the audit log's", err)
			}
			return EndpointResponse{Status: http.StatusServiceUnavailable}
		}
		return EndpointResponse{Status: http.StatusOK}
	}}
	fx := newEndpointFixture(t, rec, &upstreamCalls, func(s *Server) {
		s.Audit = func(e audit.Entry) error {
			if failAudit.Load() {
				return auditErr
			}
			mu.Lock()
			defer mu.Unlock()
			entries = append(entries, e)
			return nil
		}
	})
	defer fx.close()
	client := fx.client(false)
	resp, err := client.Post(fx.url(endpointPath+"?refresh_token="+placeholder), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	mu.Lock()
	got := append([]audit.Entry(nil), entries...)
	mu.Unlock()
	want := audit.Entry{
		Time: time.Unix(123, 0).UTC(), Machine: "machine", MachineID: "sbx", Site: "localhost",
		Method: http.MethodPost, Path: endpointPath, Secrets: []string{"codex-refresh"},
	}
	if len(got) != 1 || got[0].Time != want.Time || got[0].Machine != want.Machine || got[0].MachineID != want.MachineID ||
		got[0].Site != want.Site || got[0].Method != want.Method || got[0].Path != want.Path ||
		len(got[0].Secrets) != 1 || got[0].Secrets[0] != "codex-refresh" {
		t.Fatalf("audit entries = %+v, want one like %+v", got, want)
	}

	failAudit.Store(true)
	resp, err = client.Post(fx.url(endpointPath), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("failed audit: status %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("an endpoint request was forwarded")
	}
}

func TestEndpointAnswerIsScrubbedWithTheSecretsAfterTheHandler(t *testing.T) {
	t.Parallel()
	before, err := swap.New([]swap.Secret{{Name: secretName, Placeholder: placeholder, Value: realValue, Sites: []string{"localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := swap.New([]swap.Secret{{Name: secretName, Placeholder: placeholder, Value: rotatedValue, Sites: []string{"localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	var rotated, gone atomic.Bool
	var upstreamCalls atomic.Int32
	rec := &endpointRecorder{answer: func(context.Context, EndpointRequest) EndpointResponse {
		rotated.Store(true)
		return EndpointResponse{Status: http.StatusOK, Header: http.Header{"X-Old": {realValue}}, Body: []byte("new " + rotatedValue)}
	}}
	fx := newEndpointFixture(t, rec, &upstreamCalls, func(s *Server) {
		s.Swapper = func() *swap.Swapper {
			switch {
			case gone.Load():
				return nil
			case rotated.Load():
				return after
			}
			return before
		}
	})
	defer fx.close()
	client := fx.client(false)
	resp, err := client.Post(fx.url(endpointPath), "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(data) != "new "+placeholder {
		t.Fatalf("status %d, body scrubbed with the new value: %v", resp.StatusCode, string(data) == "new "+placeholder)
	}

	// With no secrets to scrub with, the proxy answers 503 rather than send the handler's answer.
	gone.Store(true)
	resp, err = client.Post(fx.url(endpointPath), "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Cache-Control") != "no-store" ||
		strings.Contains(string(data), "rotated") || resp.Header.Get("X-Old") != "" {
		t.Fatalf("no secrets: status %d, Cache-Control %q, handler's answer sent %v", resp.StatusCode,
			resp.Header.Get("Cache-Control"), strings.Contains(string(data), "rotated") || resp.Header.Get("X-Old") != "")
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("an endpoint request was forwarded")
	}
}
