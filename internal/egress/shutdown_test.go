package egress_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/egress/swap"
)

func TestServeCancellationClosesOpenConnections(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "before client hello"
		if passthrough {
			name = "idle passthrough"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			accepted := make(chan struct{})
			dialed := make(chan struct{})
			upstream, remote := net.Pipe()
			defer remote.Close()
			defer upstream.Close()
			server := &egress.Server{
				Accept: func(net.Conn) error { close(accepted); return nil },
				OriginalDst: func(net.Conn) (netip.AddrPort, error) {
					return netip.MustParseAddrPort("93.184.216.34:443"), nil
				},
				Dial: func(context.Context, string, string) (net.Conn, error) {
					close(dialed)
					return upstream, nil
				},
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx, listener) }()
			client, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			select {
			case <-accepted:
			case <-time.After(time.Second):
				t.Fatal("connection was not accepted")
			}
			if passthrough {
				if _, err := client.Write([]byte("plain traffic")); err != nil {
					t.Fatal(err)
				}
				select {
				case <-dialed:
				case <-time.After(time.Second):
					t.Fatal("connection did not pass through")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Serve returned %v", err)
				}
			case <-time.After(time.Second):
				t.Error("Serve did not close open connections on cancellation")
				client.Close()
				remote.Close()
				<-done
			}
			_ = client.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := client.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("client connection stayed open: %v", err)
			}
		})
	}
}

func TestServeCancellationClosesActiveTLSRequests(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(t *testing.T) {
			dir := t.TempDir()
			certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
			if _, err := ca.Init(certPath, keyPath, "Hibernaut CA"); err != nil {
				t.Fatal(err)
			}
			authority, err := ca.Load(certPath, keyPath)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := authority.Leaf("example.com")
			if err != nil {
				t.Fatal(err)
			}
			upstreamCancelled := make(chan struct{})
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = io.WriteString(w, "started")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(upstreamCancelled)
			}))
			upstream.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}}
			upstream.StartTLS()
			defer upstream.Close()
			sw, err := swap.New([]swap.Secret{{Name: "token", Placeholder: "sk-placeholder", Value: "sk-real-value", Sites: []string{"example.com"}}})
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			dialer := &net.Dialer{Timeout: time.Second}
			server := &egress.Server{
				Swapper: func() *swap.Swapper { return sw }, Leaf: authority.Leaf,
				OriginalDst: func(net.Conn) (netip.AddrPort, error) {
					return netip.MustParseAddrPort("93.184.216.34:443"), nil
				},
				Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.DialContext(ctx, network, upstream.Listener.Addr().String())
				},
				RootCAs: authority.Pool(),
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx, listener) }()
			transport := &http.Transport{
				ForceAttemptHTTP2: h2, TLSClientConfig: &tls.Config{RootCAs: authority.Pool()},
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.DialContext(ctx, network, listener.Addr().String())
				},
			}
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get("https://example.com/stream")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if h2 && response.ProtoMajor != 2 || !h2 && response.ProtoMajor != 1 {
				t.Fatalf("unexpected protocol %s", response.Proto)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("TLS request prevented shutdown")
			}
			select {
			case <-upstreamCancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream request outlived shutdown")
			}
		})
	}
}
