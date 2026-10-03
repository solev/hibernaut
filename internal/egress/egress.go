// Package egress serves the host HTTPS interception proxy.
package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/egress/swap"
)

const (
	peekTimeout          = 10 * time.Second
	maxClientHello       = 64 << 10
	readHeaderTimeout    = 10 * time.Second
	terminatedIdle       = 2 * time.Minute
	genericUpstreamError = "bad gateway\n"
)

// Server is the Hibernaut egress proxy.
type Server struct {
	Swapper     func() *swap.Swapper
	Leaf        func(host string) (*tls.Certificate, error)
	OriginalDst func(net.Conn) (netip.AddrPort, error)
	Accept      func(net.Conn) error
	Identify    func(net.Conn) (machine, machineID string, err error)
	Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
	RootCAs     *x509.CertPool
	Audit       func(audit.Entry) error
	Now         func() time.Time
	Logf        func(format string, args ...any)
}

// Serve accepts connections from l until ctx ends or l fails.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = l.Close()
		case <-done:
		}
	}()
	defer func() {
		cancel()
		close(done)
		_ = l.Close()
		wg.Wait()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return ctx.Err()
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()
	if s.Accept != nil {
		if err := s.Accept(conn); err != nil {
			s.logf("egress accept rejected %s: %v", conn.RemoteAddr(), err)
			return
		}
	}
	if s.OriginalDst == nil {
		s.logf("egress original destination seam is nil")
		return
	}
	dst, err := s.OriginalDst(conn)
	if err != nil {
		s.logf("egress original destination: %v", err)
		return
	}
	if !PublicDestination(dst.Addr()) {
		s.logf("egress refused non-public destination %s", dst)
		return
	}
	sni, peeked := peekSNI(conn)
	sni = normalizeHost(sni)
	sw := s.currentSwapper()
	if sni == "" || sw == nil || !sw.Bound(sni) {
		s.passthrough(ctx, conn, dst, peeked)
		return
	}
	s.terminate(ctx, conn, dst, sni, peeked)
}

// PublicDestination reports whether addr is routable public internet space for egress.
func PublicDestination(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return false
	}
	if addr.Is4() && addr.As4() == [4]byte{255, 255, 255, 255} {
		return false
	}
	for _, prefix := range deniedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var deniedPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"168.63.129.16/32", // Azure host services address.
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	"64:ff9b:1::/48",
	"100::/64",
	"2001:2::/48",
	"2001:db8::/32",
	"fc00::/7",
	"fe80::/10",
)

func mustPrefixes(values ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParsePrefix(value))
	}
	return out
}

func (s *Server) passthrough(ctx context.Context, client net.Conn, dst netip.AddrPort, peeked []byte) {
	upstream, err := s.dial(ctx, "tcp", dst.String())
	if err != nil {
		s.logf("egress passthrough dial %s: %v", dst, err)
		return
	}
	defer upstream.Close()
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()
	copyBoth(client, upstream, peeked)
}

func (s *Server) terminate(ctx context.Context, client net.Conn, dst netip.AddrPort, sni string, peeked []byte) {
	if s.Leaf == nil {
		s.logf("egress leaf seam is nil")
		return
	}
	cert, err := s.Leaf(sni)
	if err != nil {
		s.logf("egress leaf for %s: %v", sni, err)
		return
	}
	base := replayConn{Conn: client, reader: bytes.NewReader(peeked)}
	tlsConn := tls.Server(base, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"h2", "http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	transport := s.transport(dst, sni, []string{"h2", "http/1.1"}, true)
	defer transport.CloseIdleConnections()
	upgradeTransport := s.transport(dst, sni, []string{"http/1.1"}, false)
	defer upgradeTransport.CloseIdleConnections()
	identify := &connIdentity{conn: client, identify: s.Identify, logf: s.logf}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	// Long responses and request bodies must not be cut off by server-wide deadlines.
	server := &http.Server{
		Protocols:         protocols,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       terminatedIdle,
		ErrorLog:          log.New(logWriter{s.logf}, "", 0),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	defer server.Close()
	listener := &singleConnListener{conn: tlsConn, closec: make(chan struct{}), ctx: ctx}
	var hijacked atomic.Bool
	server.Handler = s.proxyHandler(sni, transport, upgradeTransport, identify, func() {
		if hijacked.Load() {
			_ = listener.Close()
		}
	})
	server.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateClosed:
			_ = listener.Close()
		case http.StateHijacked:
			hijacked.Store(true)
		}
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		s.logf("egress terminated connection %s: %v", sni, err)
	}
}

func (s *Server) proxyHandler(sni string, transport, upgradeTransport *http.Transport, identify *connIdentity, afterServeHTTP func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer afterServeHTTP()
		if normalizeHost(r.Host) != sni {
			http.Error(w, "misdirected request\n", http.StatusMisdirectedRequest)
			return
		}
		// A connection stays intercepted after its site loses its last binding, so responses are
		// scrubbed even when nothing is swapped.
		active := s.currentSwapper()
		if active != nil && active.Bound(sni) {
			names, err := active.Request(r, sni)
			if err != nil {
				s.logf("egress swap %s %s: %v", r.Method, r.URL.EscapedPath(), err)
				http.Error(w, genericUpstreamError, http.StatusBadGateway)
				return
			}
			if len(names) > 0 {
				machine, machineID := identify.get()
				entry := audit.Entry{
					Time:      s.now(),
					Machine:   machine,
					MachineID: machineID,
					Site:      sni,
					Method:    r.Method,
					Path:      r.URL.EscapedPath(),
					Secrets:   names,
				}
				if s.Audit != nil {
					if err := s.Audit(entry); err != nil {
						s.logf("egress audit %s %s: %v", r.Method, r.URL.EscapedPath(), err)
						http.Error(w, genericUpstreamError, http.StatusBadGateway)
						return
					}
				}
			}
		}
		t := transport
		if isUpgrade(r) {
			t = upgradeTransport
		}
		s.reverseProxy(sni, active, t).ServeHTTP(w, r)
	})
}

func (s *Server) reverseProxy(sni string, sw *swap.Swapper, transport *http.Transport) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = sni
			// ReverseProxy drops query parameters it can't parse. Nothing is swapped in the URL,
			// so the site gets the query exactly as the machine sent it.
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = sni
			pr.Out.Header.Del("Accept-Encoding")
		},
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if sw == nil {
				return nil
			}
			for _, encoding := range resp.Header.Values("Content-Encoding") {
				for token := range strings.SplitSeq(encoding, ",") {
					token = strings.TrimSpace(token)
					if token != "" && !strings.EqualFold(token, "identity") {
						return errors.New("upstream response has an uninspectable content encoding")
					}
				}
			}
			sw.ScrubHeader(resp.Header)
			if resp.StatusCode == http.StatusSwitchingProtocols {
				return nil
			}
			if resp.Body != nil {
				resp.Body = &trailerScrubBody{
					ReadCloser: sw.ScrubBody(resp.Body),
					scrub:      func() { sw.ScrubHeader(resp.Trailer) },
				}
				resp.ContentLength = -1
				resp.Header.Del("Content-Length")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.logf("egress upstream %s %s: %v", r.Method, r.URL.EscapedPath(), err)
			http.Error(w, genericUpstreamError, http.StatusBadGateway)
		},
	}
}

type trailerScrubBody struct {
	io.ReadCloser
	scrub func()
	once  sync.Once
}

func (b *trailerScrubBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.once.Do(b.scrub)
	}
	return n, err
}

func (b *trailerScrubBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.scrub)
	return err
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || headerHasToken(r.Header.Get("Connection"), "upgrade")
}

func headerHasToken(value, token string) bool {
	for part := range strings.SplitSeq(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func (s *Server) transport(dst netip.AddrPort, sni string, nextProtos []string, forceHTTP2 bool) *http.Transport {
	return &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: forceHTTP2,
		TLSClientConfig:   &tls.Config{ServerName: sni, RootCAs: s.RootCAs, NextProtos: nextProtos, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return s.dial(ctx, network, dst.String())
		},
	}
}

func (s *Server) currentSwapper() *swap.Swapper {
	if s.Swapper == nil {
		return nil
	}
	return s.Swapper()
}

func (s *Server) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if s.Dial != nil {
		return s.Dial(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

type logWriter struct {
	logf func(string, ...any)
}

func (w logWriter) Write(p []byte) (int, error) {
	if w.logf != nil {
		w.logf("%s", strings.TrimSpace(string(p)))
	}
	return len(p), nil
}

type connIdentity struct {
	once     sync.Once
	conn     net.Conn
	identify func(net.Conn) (string, string, error)
	logf     func(string, ...any)
	machine  string
	id       string
}

func (i *connIdentity) get() (string, string) {
	i.once.Do(func() {
		if i.identify == nil {
			return
		}
		machine, id, err := i.identify(i.conn)
		if err != nil {
			i.logf("egress identify: %v", err)
			return
		}
		i.machine, i.id = machine, id
	})
	return i.machine, i.id
}

type replayConn struct {
	net.Conn
	reader *bytes.Reader
}

func (c replayConn) Read(p []byte) (int, error) {
	if c.reader.Len() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}

type singleConnListener struct {
	conn      net.Conn
	once      sync.Once
	closeOnce sync.Once
	closec    chan struct{}
	ctx       context.Context
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.conn })
	if conn != nil {
		return conn, nil
	}
	select {
	case <-l.closec:
	case <-l.ctx.Done():
	}
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	l.closeOnce.Do(func() { close(l.closec) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

func copyBoth(a, b net.Conn, first []byte) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, io.MultiReader(bytes.NewReader(first), a))
		closeWrite(b)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	wg.Wait()
}

func closeWrite(conn net.Conn) {
	if c, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
		return
	}
	_ = conn.Close()
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func peekSNI(conn net.Conn) (string, []byte) {
	_ = conn.SetReadDeadline(time.Now().Add(peekTimeout))
	defer conn.SetReadDeadline(time.Time{})
	reader := bufio.NewReaderSize(conn, maxClientHello)
	var peeked []byte
	header, err := reader.Peek(5)
	if err != nil {
		return "", drainBuffered(reader, nil)
	}
	if header[0] != 0x16 || header[1] < 3 {
		return "", drainBuffered(reader, nil)
	}
	length := int(header[3])<<8 | int(header[4])
	if length <= 0 || length+5 > maxClientHello {
		return "", drainBuffered(reader, nil)
	}
	record, err := reader.Peek(length + 5)
	peeked = drainBuffered(reader, nil)
	if err != nil {
		return "", peeked
	}
	return parseClientHelloSNI(record[5:]), peeked
}

func drainBuffered(reader *bufio.Reader, dst []byte) []byte {
	for reader.Buffered() > 0 {
		chunk, _ := reader.Peek(reader.Buffered())
		dst = append(dst, chunk...)
		_, _ = reader.Discard(len(chunk))
	}
	return dst
}

func parseClientHelloSNI(data []byte) string {
	if len(data) < 4 || data[0] != 0x01 {
		return ""
	}
	n := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if n > len(data)-4 {
		return ""
	}
	p := data[4 : 4+n]
	if len(p) < 34 {
		return ""
	}
	p = p[34:]
	if len(p) < 1 || len(p) < 1+int(p[0]) {
		return ""
	}
	p = p[1+int(p[0]):]
	if len(p) < 2 {
		return ""
	}
	cipherLen := int(p[0])<<8 | int(p[1])
	if len(p) < 2+cipherLen+1 {
		return ""
	}
	p = p[2+cipherLen:]
	compressionLen := int(p[0])
	if len(p) < 1+compressionLen+2 {
		return ""
	}
	p = p[1+compressionLen:]
	extLen := int(p[0])<<8 | int(p[1])
	if len(p) < 2+extLen {
		return ""
	}
	p = p[2 : 2+extLen]
	for len(p) >= 4 {
		extType := int(p[0])<<8 | int(p[1])
		n := int(p[2])<<8 | int(p[3])
		if len(p) < 4+n {
			return ""
		}
		body := p[4 : 4+n]
		if extType == 0 {
			return parseSNIExtension(body)
		}
		p = p[4+n:]
	}
	return ""
}

func parseSNIExtension(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	listLen := int(data[0])<<8 | int(data[1])
	if len(data) < 2+listLen {
		return ""
	}
	p := data[2 : 2+listLen]
	for len(p) >= 3 {
		nameType := p[0]
		n := int(p[1])<<8 | int(p[2])
		if len(p) < 3+n {
			return ""
		}
		if nameType == 0 {
			return string(p[3 : 3+n])
		}
		p = p[3+n:]
	}
	return ""
}

func (l *singleConnListener) SetDeadline(time.Time) error { return nil }

var _ net.Listener = (*singleConnListener)(nil)
