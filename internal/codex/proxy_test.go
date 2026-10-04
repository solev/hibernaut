package codex_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/proxy"
)

// These tests run the real egress proxy, swap and vault, with the token endpoint in front of a
// real Store, against fake chatgpt.com and auth.openai.com. A small CONNECT proxy hands each
// tunnel to the egress server, the way the host's iptables hook hands it a machine's
// connection. It refuses every other host, so nothing reaches the internet.

// The public addresses the CONNECT front reports as each tunnel's original destination.
var fakeSites = map[string]netip.AddrPort{
	"chatgpt.com:443":     netip.MustParseAddrPort("104.18.32.47:443"),
	"auth.openai.com:443": netip.MustParseAddrPort("104.18.33.47:443"),
}

const (
	codexResponsesPath = "/backend-api/codex/responses"
	codexAccountsPath  = "/backend-api/wham/accounts/check"
)

type codexHarness struct {
	t           *testing.T
	store       *codex.Store
	tokens      codex.Tokens
	public      codex.PublicAuth
	tokenServer *codextest.TokenServer
	chat        *fakeChatGPT
	authHits    atomic.Int32
	front       *connectFront
	hibCA       *ca.CA
	hibCAPath   string

	mu     sync.Mutex
	audits []audit.Entry
	logs   bytes.Buffer
}

// newCodexHarness starts the proxy. refresher picks what the token endpoint refreshes
// through. Nil means the Store.
func newCodexHarness(t *testing.T, refresher func(*codex.Store) codex.Refresher) *codexHarness {
	t.Helper()
	h := &codexHarness{t: t}
	dir := t.TempDir()
	now := time.Now()
	h.tokens = codextest.Tokens("acct-1234", "dev@example.com", "plus", now, now.Add(10*24*time.Hour))
	h.tokenServer = codextest.NewTokenServer(t, h.tokens)
	cfg := codex.Config{
		StateDir: dir, VaultPath: filepath.Join(dir, "vault.json"), KeyPath: filepath.Join(dir, "vault.key"),
		TokenURL: h.tokenServer.URL, Client: h.tokenServer.Client, Now: time.Now, Logf: h.logf,
	}
	h.store = codex.NewStore(cfg)
	if _, err := h.store.CommitLogin(context.Background(), h.tokens); err != nil {
		t.Fatal(err)
	}
	public, err := h.store.Public()
	if err != nil {
		t.Fatal(err)
	}
	h.public = public

	h.hibCA = newHarnessCA(t, "hibernaut test CA")
	h.hibCAPath = filepath.Join(t.TempDir(), "hibernaut-ca.crt")
	if err := os.WriteFile(h.hibCAPath, h.hibCA.CertPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	siteCA := newHarnessCA(t, "site test CA")
	h.chat = &fakeChatGPT{
		account: h.tokens.AccountID, revoked: map[string]map[string]bool{},
		accepted: func() string { return h.tokenServer.Tokens().AccessToken },
	}
	upstreams := map[netip.AddrPort]string{
		fakeSites["chatgpt.com:443"]: startSite(t, siteCA, "chatgpt.com", h.chat),
		fakeSites["auth.openai.com:443"]: startSite(t, siteCA, "auth.openai.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.authHits.Add(1)
			http.Error(w, "the proxy forwarded a request to auth.openai.com", http.StatusTeapot)
		})),
	}

	var ep *codex.Endpoint
	if refresher != nil {
		ep = codex.NewEndpoint(refresher(h.store), h.logf)
	} else {
		ep = codex.NewEndpoint(h.store, h.logf)
	}
	source := &proxy.VaultSource{Path: cfg.VaultPath, KeyPath: cfg.KeyPath, Logf: h.logf}
	h.front = startConnectFront(t)
	server := &egress.Server{
		Swapper: source.Swapper,
		Leaf:    h.hibCA.Leaf,
		OriginalDst: func(c net.Conn) (netip.AddrPort, error) {
			tunnel, ok := c.(*tunnelConn)
			if !ok {
				return netip.AddrPort{}, errors.New("not a tunnel")
			}
			return tunnel.dst, nil
		},
		Identify: func(net.Conn) (string, string, error) { return "machine", "sbx-1", nil },
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			upstream, ok := upstreams[netip.MustParseAddrPort(addr)]
			if !ok {
				return nil, fmt.Errorf("the test refuses to dial %s", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, upstream)
		},
		RootCAs:   siteCA.Pool(),
		Audit:     h.audit,
		Now:       time.Now,
		Logf:      h.logf,
		Endpoints: []egress.Endpoint{ep.Egress()},
		Rejected:  ep.Rejected,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(ctx, h.front.tunnels)
	}()
	t.Cleanup(func() {
		cancel()
		h.front.close()
		<-done
	})
	return h
}

func newHarnessCA(t *testing.T, name string) *ca.CA {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if _, err := ca.Init(certPath, keyPath, name); err != nil {
		t.Fatal(err)
	}
	c, err := ca.Load(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// startSite serves h over TLS as host, with a certificate siteCA signs, and returns its address.
func startSite(t *testing.T, siteCA *ca.CA, host string, h http.Handler) string {
	t.Helper()
	cert, err := siteCA.Leaf(host)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(h)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func (h *codexHarness) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(&h.logs, format+"\n", args...)
}

func (h *codexHarness) audit(e audit.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audits = append(h.audits, e)
	return nil
}

// auditCount counts the audit entries for secret on site.
func (h *codexHarness) auditCount(site, secret string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.audits {
		if e.Site == site && len(e.Secrets) == 1 && e.Secrets[0] == secret && e.Machine == "machine" && e.MachineID == "sbx-1" {
			n++
		}
	}
	return n
}

// realTokens returns every real token the harness's login has had.
func (h *codexHarness) realTokens() []string {
	latest := h.tokenServer.Tokens()
	return []string{h.tokens.AccessToken, h.tokens.RefreshToken, h.tokens.IDToken, latest.AccessToken, latest.RefreshToken, latest.IDToken}
}

// wantNoRealToken fails if any of outputs holds a real token. It never prints one.
func (h *codexHarness) wantNoRealToken(what string, outputs ...[]byte) {
	h.t.Helper()
	for _, token := range h.realTokens() {
		for _, out := range outputs {
			if bytes.Contains(out, []byte(token)) {
				h.t.Errorf("%s holds a real codex token", what)
				return
			}
		}
	}
}

// client returns an HTTP client that goes through the CONNECT front and trusts the proxy's CA.
func (h *codexHarness) client() *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(h.hibCA.CertPEM())
	return &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyURL(&url.URL{Scheme: "http", Host: h.front.addr()}),
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}}
}

// connectFront is a CONNECT proxy that hands the tunnels to fake sites to the egress server.
type connectFront struct {
	listener net.Listener
	tunnels  *tunnelListener
	mu       sync.Mutex
	refused  map[string]int
}

func startConnectFront(t *testing.T) *connectFront {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &connectFront{
		listener: l, refused: map[string]int{},
		tunnels: &tunnelListener{conns: make(chan net.Conn), closed: make(chan struct{}), addr: l.Addr()},
	}
	go f.serve()
	return f
}

func (f *connectFront) addr() string { return f.listener.Addr().String() }

func (f *connectFront) close() {
	_ = f.listener.Close()
	_ = f.tunnels.Close()
}

func (f *connectFront) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *connectFront) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		_ = conn.Close()
		return
	}
	dst, ok := fakeSites[req.Host]
	if req.Method != http.MethodConnect || !ok {
		f.mu.Lock()
		f.refused[req.Method+" "+req.Host]++
		f.mu.Unlock()
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		_ = conn.Close()
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if !f.tunnels.push(&tunnelConn{Conn: conn, reader: reader, dst: dst}) {
		_ = conn.Close()
	}
}

func (f *connectFront) refusedHosts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.refused {
		out[k] = v
	}
	return out
}

// tunnelConn is a CONNECT tunnel. Its reader holds anything read past the CONNECT request.
type tunnelConn struct {
	net.Conn
	reader *bufio.Reader
	dst    netip.AddrPort
}

func (c *tunnelConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// tunnelListener gives the egress server the tunnels.
type tunnelListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	addr   net.Addr
}

func (l *tunnelListener) push(c net.Conn) bool {
	select {
	case l.conns <- c:
		return true
	case <-l.closed:
		return false
	}
}

func (l *tunnelListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *tunnelListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *tunnelListener) Addr() net.Addr { return l.addr }

// fakeChatGPT is the least of chatgpt.com that codex exec needs. Codex first checks the
// account's workspace routing on codexAccountsPath. The model stream is on codexResponsesPath,
// as a WebSocket or, as a fallback, a POST answered with Server-Sent Events. Each turn answers
// OK. Both paths answer 401 to an unknown token, and to a token revoked on that path, every
// time. Every other path is 404, never 401, so Codex carries on without it.
type fakeChatGPT struct {
	account  string
	accepted func() string

	mu             sync.Mutex
	revoked        map[string]map[string]bool
	authorizations []string
	rejections     int
	streams        []string
	paths          map[string]int
}

// revoke makes the fake reject token on paths, or on both of its paths when none are given.
func (f *fakeChatGPT) revoke(token string, paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(paths) == 0 {
		paths = []string{codexAccountsPath, codexResponsesPath}
	}
	f.revoked[token] = map[string]bool{}
	for _, path := range paths {
		f.revoked[token][path] = true
	}
}

// allowed records r, and reports whether it holds the accepted access token.
func (f *fakeChatGPT) allowed(r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paths == nil {
		f.paths = map[string]int{}
	}
	f.paths[r.Method+" "+r.URL.Path]++
	auth := r.Header.Get("Authorization")
	if auth != "" {
		f.authorizations = append(f.authorizations, auth)
	}
	token, ok := strings.CutPrefix(auth, "Bearer ")
	return ok && token == f.accepted() && !f.revoked[token][r.URL.Path]
}

func (f *fakeChatGPT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowed := f.allowed(r)
	if r.URL.Path != codexResponsesPath && r.URL.Path != codexAccountsPath {
		http.NotFound(w, r)
		return
	}
	if !allowed {
		f.mu.Lock()
		f.rejections++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"unauthorized"}}`)
		return
	}
	switch {
	case r.URL.Path == codexAccountsPath:
		f.serveAccounts(w)
	case r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Upgrade"), "websocket"):
		f.serveWebSocket(w, r)
	case r.Method == http.MethodPost:
		f.serveEvents(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveAccounts says the account has no workspace routing of its own, so Codex keeps
// chatgpt.com.
func (f *fakeChatGPT) serveAccounts(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]string{{
		"id": f.account, "workspace_backend_origin": "NO_CONSTRAINT", "account_routing_override": "NO_CONSTRAINT",
	}}})
}

func (f *fakeChatGPT) stream(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams = append(f.streams, kind)
}

// turnEvents are the events of a turn that answers OK. A warm-up request (generate false)
// gets no output.
func turnEvents(generate bool) [][2]string {
	events := [][2]string{{"response.created", `{"type":"response.created","response":{"id":"resp-1"}}`}}
	if generate {
		events = append(events, [2]string{"response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","id":"msg-1","content":[{"type":"output_text","text":"OK"}]}}`})
	}
	return append(events, [2]string{"response.completed", `{"type":"response.completed","response":{"id":"resp-1"}}`})
}

func (f *fakeChatGPT) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	f.stream("websocket")
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	// Codex's first request holds the whole prompt and its tools, well over the default limit.
	conn.SetReadLimit(16 << 20)
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var request struct {
			Type     string `json:"type"`
			Generate *bool  `json:"generate"`
		}
		if json.Unmarshal(data, &request) != nil || request.Type != "response.create" {
			continue
		}
		for _, event := range turnEvents(request.Generate == nil || *request.Generate) {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(event[1])); err != nil {
				return
			}
		}
	}
}

func (f *fakeChatGPT) serveEvents(w http.ResponseWriter, r *http.Request) {
	f.stream("sse")
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range turnEvents(true) {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event[0], event[1])
	}
}

func (f *fakeChatGPT) seen() (authorizations []string, rejections int, streams []string, paths map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths = map[string]int{}
	for k, v := range f.paths {
		paths[k] = v
	}
	return append([]string(nil), f.authorizations...), f.rejections, append([]string(nil), f.streams...), paths
}

// codexRun is a run of the pinned codex through the harness, in its own HOME.
type codexRun struct {
	bin, home string
	env       []string
}

// newCodexRun finds the pinned codex and gives it a HOME with the placeholder login and the
// image's config.toml. It skips the test without it, unless HIB_REQUIRE_CODEX=1.
func (h *codexHarness) newCodexRun() *codexRun {
	h.t.Helper()
	home := h.t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		h.t.Fatal(err)
	}
	login, err := codex.RenderAuthFile(h.public)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), login, 0o600); err != nil {
		h.t.Fatal(err)
	}
	config := `approval_policy = "never"
sandbox_mode = "danger-full-access"
check_for_update_on_startup = false
cli_auth_credentials_store = "file"

[analytics]
enabled = false
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		h.t.Fatal(err)
	}
	run := &codexRun{home: home, env: []string{
		"HOME=" + home, "CODEX_HOME=" + codexHome, "PATH=/usr/bin:/bin", "LANG=C.UTF-8",
		"HTTPS_PROXY=http://" + h.front.addr(), "CODEX_CA_CERTIFICATE=" + h.hibCAPath,
	}}
	required := os.Getenv("HIB_REQUIRE_CODEX") == "1"
	run.bin = os.Getenv("HIB_CODEX")
	if run.bin == "" {
		run.bin, _ = exec.LookPath("codex")
	}
	if run.bin == "" {
		if required {
			h.t.Fatal("the pinned codex is required: set HIB_CODEX")
		}
		h.t.Skip("the pinned codex is unavailable: set HIB_CODEX")
	}
	out, _, err := run.run(10*time.Second, "--version")
	if err != nil || strings.TrimSpace(string(out)) != "codex-cli "+codex.Version {
		if required || os.Getenv("HIB_CODEX") != "" {
			h.t.Fatalf("HIB_CODEX must be codex-cli %s", codex.Version)
		}
		h.t.Skip("the codex on PATH isn't the pinned release: set HIB_CODEX")
	}
	return run
}

func (r *codexRun) run(timeout time.Duration, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = r.env
	cmd.Dir = r.home
	cmd.Stdin = nil
	cmd.WaitDelay = 5 * time.Second
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// files returns the contents of every file under the run's HOME.
func (r *codexRun) files(t *testing.T) []byte {
	t.Helper()
	var all bytes.Buffer
	err := filepath.WalkDir(r.home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		all.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return all.Bytes()
}

// exec runs codex exec and checks that it printed OK and that no real token reached it.
func (h *codexHarness) exec(run *codexRun) {
	h.t.Helper()
	start := time.Now()
	stdout, stderr, err := run.run(90*time.Second, "exec", "--skip-git-repo-check", "Reply with exactly OK.")
	h.t.Logf("codex exec took %v", time.Since(start).Round(time.Millisecond))
	h.wantNoRealToken("codex's output", stdout, stderr)
	h.wantNoRealToken("codex's HOME", run.files(h.t))
	if err != nil || strings.TrimSpace(string(stdout)) != "OK" {
		h.report()
		h.t.Fatalf("codex exec: %v\nstdout: %s\nstderr: %s", err, stdout, h.redact(stderr))
	}
}

// redact replaces the real tokens in b, so a failure can print Codex's output.
func (h *codexHarness) redact(b []byte) []byte {
	for _, token := range h.realTokens() {
		b = bytes.ReplaceAll(b, []byte(token), []byte("[real token]"))
	}
	return b
}

// wantOnlyRealAccessTokens checks that every Authorization fake chatgpt.com saw holds a real
// access token from tokens.
func (h *codexHarness) wantOnlyRealAccessTokens(authorizations []string, tokens ...string) {
	h.t.Helper()
	if len(authorizations) == 0 {
		h.t.Fatal("chatgpt.com saw no requests")
	}
	for _, auth := range authorizations {
		ok := false
		for _, token := range tokens {
			ok = ok || auth == "Bearer "+token
		}
		if !ok {
			h.t.Fatalf("chatgpt.com saw an Authorization that isn't a real access token (placeholder: %v)",
				strings.Contains(auth, h.public.AccessToken))
		}
	}
}

func (h *codexHarness) report() {
	h.t.Helper()
	_, rejections, streams, paths := h.chat.seen()
	h.t.Logf("chatgpt.com: %d rejections, streams %v, paths %v", rejections, streams, paths)
	h.t.Logf("the CONNECT front refused %v", h.front.refusedHosts())
}

func TestCodexExecThroughTheProxy(t *testing.T) {
	h := newCodexHarness(t, nil)
	run := h.newCodexRun()
	h.exec(run)
	h.report()
	authorizations, rejections, _, _ := h.chat.seen()
	h.wantOnlyRealAccessTokens(authorizations, h.tokens.AccessToken)
	if rejections != 0 || h.tokenServer.Requests() != 0 || h.authHits.Load() != 0 {
		t.Fatalf("rejections %d, refreshes %d, forwarded to auth.openai.com %d, want none",
			rejections, h.tokenServer.Requests(), h.authHits.Load())
	}
	if h.auditCount("chatgpt.com", codex.AccessName) == 0 {
		t.Fatal("the swapped requests weren't audited")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.wantNoRealToken("the proxy's logs", h.logs.Bytes())
}

func TestCodexRefreshesThroughTheTokenEndpoint(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		testCodexRefresh(t)
	})
	t.Run("rejected by the model stream", func(t *testing.T) {
		testCodexRefresh(t, codexResponsesPath)
	})
}

// testCodexRefresh runs codex exec with its access token revoked on paths of fake chatgpt.com,
// or on all of them when none are given.
func testCodexRefresh(t *testing.T, paths ...string) {
	h := newCodexHarness(t, nil)
	run := h.newCodexRun()
	h.chat.revoke(h.tokens.AccessToken, paths...)
	h.exec(run)
	h.report()
	if got := h.tokenServer.Requests(); got != 1 {
		t.Fatalf("the token server saw %d refreshes, want 1", got)
	}
	if h.authHits.Load() != 0 {
		t.Fatal("the proxy forwarded a request to auth.openai.com")
	}
	fresh := h.tokenServer.Tokens()
	if fresh.AccessToken == h.tokens.AccessToken {
		t.Fatal("the refresh didn't rotate the access token")
	}
	authorizations, rejections, _, _ := h.chat.seen()
	h.wantOnlyRealAccessTokens(authorizations, h.tokens.AccessToken, fresh.AccessToken)
	if rejections == 0 || authorizations[len(authorizations)-1] != "Bearer "+fresh.AccessToken {
		t.Fatalf("rejections %d, and the last request used the new token %v", rejections,
			authorizations[len(authorizations)-1] == "Bearer "+fresh.AccessToken)
	}
	if got := h.auditCount(codex.TokenSite, codex.RefreshName); got == 0 {
		t.Fatal("the refresh request wasn't audited")
	}
	public, err := h.store.Public()
	if err != nil || public.AccessToken != h.public.AccessToken || public.RefreshToken != h.public.RefreshToken {
		t.Fatal("the placeholders changed")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.wantNoRealToken("the proxy's logs", h.logs.Bytes())
}

func TestConcurrentRefreshesMakeOneUpstreamRefresh(t *testing.T) {
	h := newCodexHarness(t, nil)
	client := h.client()
	h.chat.revoke(h.tokens.AccessToken)
	req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com"+codexResponsesPath, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+h.public.AccessToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: status %d, want 401", resp.StatusCode)
	}
	body, _ := json.Marshal(map[string]string{"client_id": codex.ClientID, "grant_type": "refresh_token", "refresh_token": h.public.RefreshToken})
	var wg sync.WaitGroup
	answers := make([][]byte, 10)
	statuses := make([]int, 10)
	for i := range answers {
		wg.Go(func() {
			resp, err := client.Post("https://"+codex.TokenSite+codex.TokenPath, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			statuses[i] = resp.StatusCode
			answers[i], _ = io.ReadAll(resp.Body)
		})
	}
	wg.Wait()
	for i, answer := range answers {
		var got struct {
			ID      string `json:"id_token"`
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		}
		if err := json.Unmarshal(answer, &got); err != nil || statuses[i] != http.StatusOK ||
			got.ID != h.public.IDToken || got.Access != h.public.AccessToken || got.Refresh != h.public.RefreshToken {
			t.Fatalf("client %d: status %d, placeholders %v", i, statuses[i], got.Access == h.public.AccessToken)
		}
	}
	h.wantNoRealToken("the answers", answers...)
	if got := h.tokenServer.Requests(); got != 1 {
		t.Fatalf("the token server saw %d refreshes, want 1", got)
	}
	if h.authHits.Load() != 0 {
		t.Fatal("the proxy forwarded a request to auth.openai.com")
	}
	if got := h.auditCount(codex.TokenSite, codex.RefreshName); got != 10 {
		t.Fatalf("%d refresh requests audited, want 10", got)
	}
}

// leakyRefresher is the Store, except that EnsureFresh fails with err, or else answers with the
// real tokens, as if it leaked them.
type leakyRefresher struct {
	*codex.Store
	real codex.Tokens

	mu  sync.Mutex
	err error
}

func (l *leakyRefresher) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err = err
}

func (l *leakyRefresher) EnsureFresh(context.Context, codex.Reason) (codex.PublicAuth, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return codex.PublicAuth{}, l.err
	}
	return codex.PublicAuth{IDToken: l.real.IDToken, AccessToken: l.real.AccessToken, RefreshToken: l.real.RefreshToken, AccountID: l.real.AccountID}, nil
}

func TestTokenEndpointAnswersThroughTheProxy(t *testing.T) {
	var leaky *leakyRefresher
	h := newCodexHarness(t, func(store *codex.Store) codex.Refresher {
		leaky = &leakyRefresher{Store: store}
		return leaky
	})
	leaky.real = h.tokens
	refresh := func(token string) string {
		body, _ := json.Marshal(map[string]string{"client_id": codex.ClientID, "grant_type": "refresh_token", "refresh_token": token})
		return string(body)
	}
	for _, test := range []struct {
		name, body, contentType string
		err                     error
		status                  int
		want                    string
	}{
		{"refresh", refresh(h.public.RefreshToken), "application/json", nil, http.StatusOK, h.public.AccessToken},
		{"form refresh", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {h.public.RefreshToken}}.Encode(),
			"application/x-www-form-urlencoded", nil, http.StatusOK, h.public.RefreshToken},
		{"dead login", refresh(h.public.RefreshToken), "application/json", codex.ErrDead, http.StatusUnauthorized, "refresh_token_invalidated"},
		{"unavailable", refresh(h.public.RefreshToken), "application/json", codex.ErrUnavailable, http.StatusServiceUnavailable, "temporarily_unavailable"},
		{"other token", refresh("rt.someone-elses"), "application/json", nil, http.StatusForbidden, "access_denied"},
		{"other grant", `{"grant_type":"authorization_code"}`, "application/json", nil, http.StatusForbidden, "access_denied"},
		{"bad body", "{", "application/json", nil, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			leaky.fail(test.err)
			resp, err := h.client().Post("https://"+codex.TokenSite+codex.TokenPath+"?x=1", test.contentType, strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			h.wantNoRealToken("the answer", body)
			if resp.StatusCode != test.status || resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(string(body), test.want) {
				t.Fatalf("status %d, Cache-Control %q, holds the expected value %v; want %d no-store", resp.StatusCode,
					resp.Header.Get("Cache-Control"), strings.Contains(string(body), test.want), test.status)
			}
		})
	}
	if h.authHits.Load() != 0 {
		t.Fatal("the proxy forwarded a request to auth.openai.com")
	}
	if got := h.auditCount(codex.TokenSite, codex.RefreshName); got != 4 {
		t.Fatalf("%d codex-refresh audit entries, want 4, one for each refresh with the current placeholder", got)
	}
	if h.tokenServer.Requests() != 0 {
		t.Fatal("the Store refreshed")
	}
}
