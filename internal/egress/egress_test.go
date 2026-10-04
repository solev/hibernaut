package egress

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/egress/swap"
)

const (
	secretName  = "github"
	placeholder = "gho_hib_placeholder_secret"
	realValue   = "gho_real_proxy_secret"
)

var publicDst = netip.MustParseAddrPort("93.184.216.34:443")

func TestPublicDestination(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr string
		want bool
	}{
		{"93.184.216.34", true},
		{"2606:4700:4700::1111", true},
		{"10.1.2.3", false},
		{"100.64.0.1", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
		{"168.63.129.16", false},
		{"172.16.0.1", false},
		{"192.168.0.1", false},
		{"0.1.2.3", false},
		{"255.255.255.255", false},
		{"192.0.2.1", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"198.18.0.1", false},
		{"224.0.0.1", false},
		{"::1", false},
		{"fc00::1", false},
		{"fe80::1", false},
		{"2001:db8::1", false},
		{"::ffff:10.0.0.1", false},
		{"::ffff:93.184.216.34", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := PublicDestination(netip.MustParseAddr(tt.addr))
			if got != tt.want {
				t.Fatalf("PublicDestination(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestExpectContinueKeepsFinalStatusAndNoInternalHeaders(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, "nope")
	}))
	defer fx.close()
	tr := fx.transport(false)
	tr.ExpectContinueTimeout = 5 * time.Second
	req, err := http.NewRequest(http.MethodPost, fx.url("/x"), strings.NewReader(strings.Repeat("a", 2000)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Expect", "100-continue")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, headers %v", resp.StatusCode, resp.Header)
	}
	for name := range resp.Header {
		if strings.HasPrefix(name, "X-Hibernaut-") {
			t.Fatalf("internal header reached client: %s=%q", name, resp.Header.Values(name))
		}
	}
}

func TestBoundHTTP11SwapsAuditsAndScrubs(t *testing.T) {
	t.Parallel()
	upstreamSaw := make(chan *http.Request, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+realValue {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("token"); got != placeholder {
			t.Errorf("query token = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"token":"`+placeholder+`"}` {
			t.Errorf("body changed: %q", body)
		}
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding = %q, want Go transport gzip", r.Header.Get("Accept-Encoding"))
		}
		upstreamSaw <- r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"token":"%s"}`, realValue)
	})
	var entries []audit.Entry
	fx := newFixtureConfigured(t, "localhost", h, func(s *Server) {
		s.Audit = func(e audit.Entry) error {
			entries = append(entries, e)
			return nil
		}
	})
	defer fx.close()
	client := fx.client(false)
	body := strings.NewReader(`{"token":"` + placeholder + `"}`)
	req, err := http.NewRequest(http.MethodPost, fx.url("/api?token="+url.QueryEscape(placeholder)), body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+placeholder)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Proto != "HTTP/1.1" {
		t.Fatalf("resp.Proto = %s", resp.Proto)
	}
	data, _ := io.ReadAll(resp.Body)
	if resp.ContentLength != -1 || resp.Header.Get("Content-Length") != "" {
		t.Fatalf("scrubbed response kept Content-Length: %d %q body=%q headers=%v", resp.ContentLength, resp.Header.Get("Content-Length"), data, resp.Header)
	}
	if strings.Contains(string(data), realValue) || !strings.Contains(string(data), placeholder) {
		t.Fatalf("response was not scrubbed: %q", data)
	}
	select {
	case <-upstreamSaw:
	case <-time.After(time.Second):
		t.Fatal("upstream was not called")
	}
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Site != "localhost" || entry.Method != http.MethodPost || entry.Path != "/api" || entry.Machine != "machine" || entry.MachineID != "sbx" {
		t.Fatalf("bad audit entry: %+v", entry)
	}
	if len(entry.Secrets) != 1 || entry.Secrets[0] != secretName {
		t.Fatalf("bad audit secrets: %+v", entry.Secrets)
	}
}

func TestHTTP2EndToEnd(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	defer fx.close()
	resp, err := fx.client(true).Get(fx.url("/h2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Proto != "HTTP/2.0" {
		t.Fatalf("resp.Proto = %s, want HTTP/2.0", resp.Proto)
	}
}

func TestUpstreamUsesHTTP2WhenSiteOffersIt(t *testing.T) {
	t.Parallel()
	got := make(chan string, 1)
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Proto
		_, _ = io.WriteString(w, "ok")
	}))
	defer fx.close()
	resp, err := fx.client(false).Get(fx.url("/upstream-h2"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	select {
	case proto := <-got:
		if proto != "HTTP/2.0" {
			t.Fatalf("upstream proto = %s, want HTTP/2.0", proto)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream was not called")
	}
}

func TestHostSNIMismatchReturns421WithoutUpstream(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer fx.close()
	tr := fx.transport(false)
	tr.TLSClientConfig.ServerName = "localhost"
	client := &http.Client{Transport: tr}
	req, err := http.NewRequest(http.MethodGet, fx.url("/bad"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream was called")
	}
}

func TestPrivateDestinationRefusedWithoutDialing(t *testing.T) {
	t.Parallel()
	var dials atomic.Int32
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), func(s *Server) {
		s.OriginalDst = func(net.Conn) (netip.AddrPort, error) { return netip.MustParseAddrPort("10.0.0.1:443"), nil }
		s.Dial = func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("unexpected dial")
		}
	})
	defer fx.close()
	_, err := fx.client(false).Get(fx.url("/"))
	if err == nil {
		t.Fatal("request succeeded to private destination")
	}
	if dials.Load() != 0 {
		t.Fatalf("dialed private destination")
	}
}

func TestAcceptFilterRejection(t *testing.T) {
	t.Parallel()
	var dials atomic.Int32
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), func(s *Server) {
		s.Accept = func(net.Conn) error { return errors.New("reject") }
		s.Dial = func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("unexpected dial")
		}
	})
	defer fx.close()
	_, err := fx.client(false).Get(fx.url("/"))
	if err == nil {
		t.Fatal("request succeeded after accept rejection")
	}
	if dials.Load() != 0 {
		t.Fatalf("dialed after accept rejection")
	}
}

func TestUnboundSitePassthroughSeesSiteCertificate(t *testing.T) {
	t.Parallel()
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "site")
	}), func(s *Server) { s.Swapper = func() *swap.Swapper { return nil } })
	defer fx.close()
	resp, err := fx.clientWithRoots(fx.siteCA.Pool(), false).Get(fx.url("/passthrough"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		t.Fatal("no peer certificate")
	}
	issuer := resp.TLS.PeerCertificates[0].Issuer.CommonName
	if issuer != "site test CA" {
		t.Fatalf("issuer = %q, want site CA", issuer)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "site" {
		t.Fatalf("body = %q", data)
	}
}

func TestNonTLSPassthrough(t *testing.T) {
	t.Parallel()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		_, _ = io.WriteString(conn, "echo:"+line)
	}()
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), func(s *Server) {
		s.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upstream.Addr().String())
		}
	})
	defer fx.close()
	conn, err := net.Dial("tcp", fx.proxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "hello\n")
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo:hello\n" {
		t.Fatalf("got %q", got)
	}
	<-done
}

func TestStreamingResponseFlushesBeforeUpstreamFinishes(t *testing.T) {
	t.Parallel()
	clientSawFirst := make(chan struct{})
	upstreamFinished := make(chan struct{})
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		<-clientSawFirst
		_, _ = io.WriteString(w, "data: second\n\n")
		close(upstreamFinished)
	}))
	defer fx.close()
	resp, err := fx.client(false).Get(fx.url("/events"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "data: first\n" {
		t.Fatalf("first line = %q", line)
	}
	select {
	case <-upstreamFinished:
		t.Fatal("upstream finished before client observed first bytes")
	default:
	}
	close(clientSawFirst)
	select {
	case <-upstreamFinished:
	case <-time.After(time.Second):
		t.Fatal("upstream did not finish")
	}
}

func TestLargeResponseFlushesBeforeUpstreamFinishes(t *testing.T) {
	t.Parallel()
	clientSawFirst := make(chan struct{})
	upstreamFinished := make(chan struct{})
	first := strings.Repeat("a", 256<<10)
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, first)
		flusher.Flush()
		<-clientSawFirst
		_, _ = io.WriteString(w, strings.Repeat("b", 256<<10))
		close(upstreamFinished)
	}))
	defer fx.close()
	resp, err := fx.client(false).Get(fx.url("/large"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, len(first))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != first {
		t.Fatal("first large body chunk changed")
	}
	select {
	case <-upstreamFinished:
		t.Fatal("upstream finished before client observed first large body bytes")
	default:
	}
	close(clientSawFirst)
	select {
	case <-upstreamFinished:
	case <-time.After(time.Second):
		t.Fatal("upstream did not finish")
	}
}

func TestWebSocketThroughProxy(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+realValue {
			t.Errorf("upgrade Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("X-Echo-Authorization", r.Header.Get("Authorization"))
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read websocket: %v", err)
			return
		}
		if string(data) != placeholder {
			t.Errorf("upstream WebSocket frame = %q", data)
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(realValue+"|"+string(data)))
	}))
	defer fx.close()
	ctx := context.Background()
	headers := http.Header{"Authorization": {"Bearer " + placeholder}}
	conn, resp, err := websocket.Dial(ctx, strings.Replace(fx.url("/ws"), "https://", "wss://", 1), &websocket.DialOptions{HTTPHeader: headers, HTTPClient: fx.client(false)})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Echo-Authorization") != "Bearer "+placeholder {
		t.Fatalf("upgrade response header = %q", resp.Header.Get("X-Echo-Authorization"))
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := conn.Write(ctx, websocket.MessageText, []byte(placeholder)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != realValue+"|"+placeholder {
		t.Fatalf("websocket frame = %q", data)
	}
}

func TestWebSocketReleasesConnectionAfterClose(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, data)
	}))
	defer fx.close()
	ctx := context.Background()
	conn, resp, err := websocket.Dial(ctx, strings.Replace(fx.url("/ws"), "https://", "wss://", 1), &websocket.DialOptions{HTTPClient: fx.client(false)})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	_, _, _ = conn.Read(ctx)
	_ = conn.Close(websocket.StatusNormalClosure, "")
	_ = fx.proxy.Close()
	select {
	case <-fx.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve still waiting on a finished WebSocket connection")
	}
}

func TestInvalidUpstreamCertificateRefusedBeforeRequest(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	fx := newFixtureWithSiteHost(t, "wrong.localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer fx.close()
	req, _ := http.NewRequest(http.MethodGet, fx.url("/badcert"), nil)
	req.Header.Set("Authorization", "Bearer "+placeholder)
	resp, err := fx.client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream saw request before cert verification failed")
	}
}

func TestAuditLogNeverContainsRealValue(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log, err := audit.Open(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }), func(s *Server) { s.Audit = log.Write })
	defer fx.close()
	req, _ := http.NewRequest(http.MethodGet, fx.url("/audit?q="+placeholder), nil)
	req.Header.Set("Authorization", "Bearer "+placeholder)
	resp, err := fx.client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = %d %q, %v", resp.StatusCode, body, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), realValue) || strings.Contains(string(data), placeholder) {
		t.Fatalf("audit log contains a secret value: %q", data)
	}
	var entry audit.Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Machine != "machine" || entry.MachineID != "sbx" || entry.Site != "localhost" ||
		entry.Method != "GET" || entry.Path != "/audit" || len(entry.Secrets) != 1 || entry.Secrets[0] != "github" {
		t.Fatalf("audit entry = %+v", entry)
	}
}

func TestNonTextResponseIsByteIdentical(t *testing.T) {
	t.Parallel()
	want := []byte{0, 1, 2, 'x', ' ', 'y'}
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(want)
	}))
	defer fx.close()
	resp, err := fx.client(false).Get(fx.url("/binary"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != string(want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
}

func TestRemovedSecretStopsSwappingOnOpenConnection(t *testing.T) {
	t.Parallel()
	var got []string
	var revoked atomic.Bool
	empty, err := swap.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	full, err := swap.New([]swap.Secret{{Name: secretName, Placeholder: placeholder, Value: realValue, Sites: []string{"localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
	}), func(s *Server) {
		s.Swapper = func() *swap.Swapper {
			if revoked.Load() {
				return empty
			}
			return full
		}
	})
	defer fx.close()
	client := fx.client(false)
	for i := range 2 {
		req, err := http.NewRequest(http.MethodGet, fx.url("/"), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if i == 0 {
			revoked.Store(true)
		}
	}
	if len(got) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(got))
	}
	if got[0] != "Bearer "+realValue {
		t.Fatalf("before removal upstream saw %q", got[0])
	}
	if got[1] != "Bearer "+placeholder {
		t.Fatalf("after removal upstream saw %q", got[1])
	}
}

func TestUnboundSiteOnOpenConnectionStillScrubsResponses(t *testing.T) {
	t.Parallel()
	const otherPlaceholder, otherValue = "sk_hib_other_placeholder", "sk_real_other_secret"
	other := swap.Secret{Name: "other", Placeholder: otherPlaceholder, Value: otherValue, Sites: []string{"other.example"}}
	bound, err := swap.New([]swap.Secret{{Name: secretName, Placeholder: placeholder, Value: realValue, Sites: []string{"localhost"}}, other})
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := swap.New([]swap.Secret{other})
	if err != nil {
		t.Fatal(err)
	}
	var unbind atomic.Bool
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", otherValue)
		_, _ = io.WriteString(w, "token "+otherValue)
	}), func(s *Server) {
		s.Swapper = func() *swap.Swapper {
			if unbind.Load() {
				return unbound
			}
			return bound
		}
	})
	defer fx.close()
	// The client trusts only the Hibernaut CA, so the second request fails unless it reuses the
	// intercepted connection.
	client := fx.client(false)
	for i := range 2 {
		resp, err := client.Get(fx.url("/"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.Header.Get("X-Echo") != otherPlaceholder || string(data) != "token "+otherPlaceholder {
			t.Fatalf("response %d has header %q, body %q", i, resp.Header.Get("X-Echo"), data)
		}
		unbind.Store(true)
	}
}

func TestAuditEntriesIncludeMachineIdentityOnlyForSwaps(t *testing.T) {
	t.Parallel()
	entries := make(chan audit.Entry, 3)
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }), func(s *Server) {
		s.Identify = func(net.Conn) (string, string, error) {
			return "identified-machine", "identified-sbx", nil
		}
		s.Audit = func(e audit.Entry) error { entries <- e; return nil }
	})
	defer fx.close()
	client := fx.client(false)
	resp, err := client.Get(fx.url("/no-swap"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(data) != "ok" {
		t.Fatalf("no-swap response = %q, %v", data, err)
	}
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, fx.url(fmt.Sprintf("/swap-%d", i)), nil)
		req.Header.Set("Authorization", "Bearer "+placeholder)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	for i := range 2 {
		select {
		case e := <-entries:
			if e.Machine != "identified-machine" || e.MachineID != "identified-sbx" ||
				e.Site != "localhost" || e.Path != fmt.Sprintf("/swap-%d", i) || e.Method != "GET" ||
				len(e.Secrets) != 1 || e.Secrets[0] != "github" {
				t.Fatalf("audit = %+v", e)
			}
		default:
			t.Fatal("swap has no identified audit entry")
		}
	}
	select {
	case e := <-entries:
		t.Fatalf("unexpected audit entry: %+v", e)
	default:
	}
}

func TestAuditFailClosed(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls.Add(1) }), func(s *Server) {
		s.Audit = func(audit.Entry) error { return errors.New("disk full") }
	})
	defer fx.close()
	req, _ := http.NewRequest(http.MethodGet, fx.url("/fail"), nil)
	req.Header.Set("Authorization", "Bearer "+placeholder)
	resp, err := fx.client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream was called after audit failure")
	}
}

func TestGzipUpstreamResponseIsScrubbed(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding = %q, want Go transport gzip", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte("secret=" + realValue))
		_ = gz.Close()
	}))
	defer fx.close()
	resp, err := fx.client(false).Get(fx.url("/gzip"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(data), realValue) || !strings.Contains(string(data), placeholder) {
		t.Fatalf("gzip response not scrubbed: %q", data)
	}
}

func TestRealCurlClient(t *testing.T) {
	t.Parallel()
	requireBinary(t, "curl")
	got := make(chan string, 1)
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	defer fx.close()
	cmd := exec.Command("curl", "--silent", "--show-error", "--fail", "--cacert", fx.hibCertPath, "-H", "Authorization: Bearer "+placeholder, fx.url("/curl"))
	cmd.Env = append(os.Environ(), "HTTPS_PROXY=", "https_proxy=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("curl failed: %v: %s", err, out)
	}
	if auth := <-got; auth != "Bearer "+realValue {
		t.Fatalf("curl auth = %q", auth)
	}
}

func TestRealGitClient(t *testing.T) {
	t.Parallel()
	requireBinary(t, "git")
	got := make(chan string, 1)
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		got <- auth
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = io.WriteString(w, "001e# service=git-upload-pack\n00000000")
	}))
	defer fx.close()
	raw := "https://x-access-token:" + placeholder + "@" + strings.TrimPrefix(fx.url("/repo.git"), "https://")
	cmd := isolatedGit(t, []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.Getenv("TMPDIR"),
		"HTTPS_PROXY=", "https_proxy="},
		"-c", "http.sslCAInfo="+fx.hibCertPath, "ls-remote", raw)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git failed: %v: %s", err, out)
	}
	auth := <-got
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("git auth = %q", auth)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), realValue) {
		t.Fatalf("git basic auth was not swapped: %q", decoded)
	}
}

func TestRealNodeClient(t *testing.T) {
	t.Parallel()
	requireBinary(t, "node")
	got := make(chan string, 1)
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- r.Header.Get("Authorization") + "\n" + r.URL.Query().Get("token") + "\n" + string(body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer fx.close()
	script := `
const res = await fetch(process.env.URL + '?token=' + encodeURIComponent(process.env.PLACEHOLDER), {
  method: 'POST',
  headers: {'Content-Type': 'application/json', Authorization: 'Bearer ' + process.env.PLACEHOLDER},
  body: JSON.stringify({token: process.env.PLACEHOLDER}),
});
if (!res.ok) throw new Error('status ' + res.status);
`
	cmd := exec.Command("node", "--input-type=module", "--eval", script)
	cmd.Env = append(os.Environ(), "NODE_EXTRA_CA_CERTS="+fx.hibCertPath, "URL="+fx.url("/node"), "PLACEHOLDER="+placeholder, "HTTPS_PROXY=", "https_proxy=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node failed: %v: %s", err, out)
	}
	seen := <-got
	if seen != "Bearer "+realValue+"\n"+placeholder+"\n"+`{"token":"`+placeholder+`"}` {
		t.Fatalf("node Authorization or content = %q", seen)
	}
}

func TestRealGHClient(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "darwin" {
		t.Skip("gh on darwin uses the platform verifier and ignores SSL_CERT_FILE, so it cannot trust the test CA")
	}
	requireBinary(t, "gh")
	got := make(chan string, 1)
	fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Path + "\n" + r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"login":"hibernaut-test"}`)
	}))
	defer fx.close()
	_, port, err := net.SplitHostPort(fx.proxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	host := "localhost:" + port
	// gh's --hostname refuses a port, but GH_HOST doesn't.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "user")
	cmd.WaitDelay = time.Second
	home := t.TempDir()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.Getenv("TMPDIR"),
		"SSL_CERT_FILE=" + fx.hibCertPath, "GH_ENTERPRISE_TOKEN=" + placeholder, "GH_HOST=" + host,
		"HOME=" + home, "XDG_CONFIG_HOME=" + home, "GH_CONFIG_DIR=" + home,
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "HTTPS_PROXY=", "https_proxy="}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gh failed: %v: %s", err, out)
	}
	seen := <-got
	// gh sends its token with the "token" scheme.
	if want := "/api/v3/user\ntoken " + realValue; seen != want {
		t.Fatalf("gh request was not swapped: got %q, want %q", seen, want)
	}
}

func isolatedGit(t *testing.T, env []string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	home := t.TempDir()
	config := []string{
		"-c", "credential.helper=",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "init.templateDir=",
	}
	cmd := exec.CommandContext(ctx, "git", append(config, args...)...)
	cmd.Dir = home
	cmd.WaitDelay = time.Second
	cmd.Env = append(append([]string{}, env...),
		"HOME="+home, "XDG_CONFIG_HOME="+home,
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS=", "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never",
		"GIT_ASKPASS=", "SSH_ASKPASS=")
	return cmd
}

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed", name)
	}
}

type fixture struct {
	proxy       net.Listener
	upstream    *httptest.Server
	server      *Server
	cancel      context.CancelFunc
	done        chan error
	hibCA       *ca.CA
	siteCA      *ca.CA
	hibCertPath string
}

func newFixture(t *testing.T, h http.Handler) *fixture {
	t.Helper()
	return newFixtureConfigured(t, "localhost", h, nil)
}

func newFixtureWithSiteHost(t *testing.T, siteHost string, h http.Handler) *fixture {
	t.Helper()
	return newFixtureConfigured(t, siteHost, h, nil)
}

func newFixtureConfigured(t *testing.T, siteHost string, h http.Handler, configure func(*Server)) *fixture {
	t.Helper()
	return newFixtureProtocols(t, siteHost, h, configure, true)
}

func newFixtureProtocols(t *testing.T, siteHost string, h http.Handler, configure func(*Server), upstreamHTTP2 bool) *fixture {
	t.Helper()
	hibCA := newTestCA(t, "hibernaut test CA")
	siteCA := newTestCA(t, "site test CA")
	up := httptest.NewUnstartedServer(h)
	cert, err := siteCA.Leaf(siteHost)
	if err != nil {
		t.Fatal(err)
	}
	protos := []string{"http/1.1"}
	if upstreamHTTP2 {
		protos = []string{"h2", "http/1.1"}
	}
	up.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}, NextProtos: protos, MinVersion: tls.VersionTLS12}
	up.EnableHTTP2 = upstreamHTTP2
	up.StartTLS()
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sw, err := swap.New([]swap.Secret{{Name: secretName, Placeholder: placeholder, Value: realValue, Sites: []string{"localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &Server{
		Swapper:     func() *swap.Swapper { return sw },
		Leaf:        hibCA.Leaf,
		OriginalDst: func(net.Conn) (netip.AddrPort, error) { return publicDst, nil },
		Identify:    func(net.Conn) (string, string, error) { return "machine", "sbx", nil },
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, up.Listener.Addr().String())
		},
		RootCAs: siteCA.Pool(),
		Audit:   func(audit.Entry) error { return nil },
		Now:     func() time.Time { return time.Unix(123, 0).UTC() },
		Logf:    func(string, ...any) {},
	}
	if configure != nil {
		configure(server)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, proxy) }()
	certPath := filepath.Join(t.TempDir(), "hibernaut-ca.crt")
	if err := os.WriteFile(certPath, hibCA.CertPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	return &fixture{proxy: proxy, upstream: up, server: server, cancel: cancel, done: done, hibCA: hibCA, siteCA: siteCA, hibCertPath: certPath}
}

func (f *fixture) close() {
	f.cancel()
	_ = f.proxy.Close()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
	}
	f.upstream.Close()
}

func (f *fixture) proxyAddr() string { return f.proxy.Addr().String() }

func (f *fixture) url(path string) string {
	_, port, err := net.SplitHostPort(f.proxyAddr())
	if err != nil {
		panic(err)
	}
	return "https://localhost:" + port + path
}

func (f *fixture) client(h2 bool) *http.Client { return f.clientWithRoots(f.hibCA.Pool(), h2) }

func (f *fixture) clientWithRoots(roots *x509.CertPool, h2 bool) *http.Client {
	return &http.Client{Transport: f.transportWithRoots(roots, h2)}
}

func (f *fixture) transport(h2 bool) *http.Transport { return f.transportWithRoots(f.hibCA.Pool(), h2) }

func (f *fixture) transportWithRoots(roots *x509.CertPool, h2 bool) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		},
		ForceAttemptHTTP2: h2,
	}
}

func newTestCA(t *testing.T, name string) *ca.CA {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if _, err := ca.Init(certPath, keyPath, name); err != nil {
		t.Fatal(err)
	}
	c, err := ca.Load(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
