package egress

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/solev/hibernaut/internal/egress/swap"
)

type rejection struct{ secret, fingerprint string }

type rejections struct {
	mu   sync.Mutex
	list []rejection
}

func (r *rejections) add(secret, fingerprint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, rejection{secret, fingerprint})
}

func (r *rejections) get() []rejection {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rejection(nil), r.list...)
}

func TestRejectedHearsAbout401ToSwappedSecrets(t *testing.T) {
	t.Parallel()
	const otherName, otherPlaceholder, otherValue = "other", "sk_hib_other_placeholder", "sk_real_other_secret"
	sw, err := swap.New([]swap.Secret{
		{Name: secretName, Placeholder: placeholder, Value: realValue, Sites: []string{"localhost"}},
		{Name: otherName, Placeholder: otherPlaceholder, Value: otherValue, Sites: []string{"localhost"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var heard rejections
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "denied "+realValue)
	}), func(s *Server) {
		s.Swapper = func() *swap.Swapper { return sw }
		s.Rejected = heard.add
	})
	defer fx.close()
	client := fx.client(true)
	do := func(auth string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, fx.url("/api"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || string(data) != "denied "+placeholder ||
			resp.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
			t.Fatalf("the 401 changed: status %d, header %q, body scrubbed %v", resp.StatusCode,
				resp.Header.Get("WWW-Authenticate"), string(data) == "denied "+placeholder)
		}
	}

	do("")
	do("Bearer not-a-placeholder")
	if got := heard.get(); len(got) != 0 {
		t.Fatalf("Rejected heard %d times about requests with nothing swapped", len(got))
	}

	do("Bearer " + placeholder)
	if got := heard.get(); len(got) != 1 || got[0] != (rejection{secretName, swap.Fingerprint(realValue)}) {
		t.Fatalf("Rejected heard %+v, want the github secret's fingerprint once", got)
	}

	do("Basic " + base64.StdEncoding.EncodeToString([]byte(otherPlaceholder+":"+placeholder)))
	got := heard.get()[1:]
	want := []rejection{{secretName, swap.Fingerprint(realValue)}, {otherName, swap.Fingerprint(otherValue)}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Rejected heard %+v, want each swapped secret once", got)
	}
}

func TestRejectedIgnoresOtherStatuses(t *testing.T) {
	t.Parallel()
	var status atomic.Int32
	var heard rejections
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}), func(s *Server) { s.Rejected = heard.add })
	defer fx.close()
	client := fx.client(false)
	for _, code := range []int{http.StatusOK, http.StatusForbidden, http.StatusProxyAuthRequired, http.StatusInternalServerError} {
		status.Store(int32(code))
		req, _ := http.NewRequest(http.MethodGet, fx.url("/"), nil)
		req.Header.Set("Authorization", "Bearer "+placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != code {
			t.Fatalf("status = %d, want %d", resp.StatusCode, code)
		}
	}
	if got := heard.get(); len(got) != 0 {
		t.Fatalf("Rejected heard %d times, want none", len(got))
	}
}

func TestRejectedGetsTheFingerprintOfTheValueSent(t *testing.T) {
	t.Parallel()
	before, err := swap.New([]swap.Secret{{Name: secretName, Placeholder: placeholder, Value: realValue, Sites: []string{"localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := swap.New([]swap.Secret{{Name: secretName, Placeholder: placeholder, Value: rotatedValue, Sites: []string{"localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	var rotated atomic.Bool
	var heard rejections
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rotated.Store(true)
		w.WriteHeader(http.StatusUnauthorized)
	}), func(s *Server) {
		s.Swapper = func() *swap.Swapper {
			if rotated.Load() {
				return after
			}
			return before
		}
		s.Rejected = heard.add
	})
	defer fx.close()
	req, _ := http.NewRequest(http.MethodGet, fx.url("/"), nil)
	req.Header.Set("Authorization", "Bearer "+placeholder)
	resp, err := fx.client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := heard.get(); len(got) != 1 || got[0] != (rejection{secretName, swap.Fingerprint(realValue)}) {
		t.Fatalf("Rejected heard %d times, with the sent value's fingerprint: %v", len(got),
			len(got) == 1 && got[0].fingerprint == swap.Fingerprint(realValue))
	}
}

func TestRejectedHearsAbout401ToWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	var heard rejections
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+realValue {
			t.Errorf("upgrade wasn't swapped")
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}), func(s *Server) { s.Rejected = heard.add })
	defer fx.close()
	headers := http.Header{"Authorization": {"Bearer " + placeholder}}
	conn, resp, err := websocket.Dial(context.Background(), strings.Replace(fx.url("/ws"), "https://", "wss://", 1),
		&websocket.DialOptions{HTTPHeader: headers, HTTPClient: fx.client(false)})
	if conn != nil {
		_ = conn.CloseNow()
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upgrade: %v, response %v", err, resp != nil)
	}
	if got := heard.get(); len(got) != 1 || got[0] != (rejection{secretName, swap.Fingerprint(realValue)}) {
		t.Fatalf("Rejected heard %d times, want once with the github secret's fingerprint", len(got))
	}
}
