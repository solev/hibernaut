package hibd

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/egress/hook"
	"github.com/solev/hibernaut/internal/egress/swap"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestCAInit(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"HIBD_CONFIG":  filepath.Join(dir, "absent.env"),
		"HIBD_CA_CERT": filepath.Join(dir, "ca.crt"),
		"HIBD_CA_KEY":  filepath.Join(dir, "ca.key"),
	}
	var out, logs bytes.Buffer
	a := &App{Stdout: &out, Stderr: &logs, Getenv: func(k string) string { return env[k] }, EUID: func() int { return 0 }}
	if code := a.Run(t.Context(), []string{"ca", "init"}); code != 0 {
		t.Fatalf("ca init exit %d: %s", code, &logs)
	}
	if !strings.Contains(out.String(), "created") {
		t.Fatalf("creation output: %s", &out)
	}
	certBefore, err := os.ReadFile(env["HIBD_CA_CERT"])
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certBefore)
	if block == nil {
		t.Fatal("no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || cert.Subject.CommonName != "Hibernaut CA" {
		t.Fatalf("certificate CN: %v, %v", cert, err)
	}
	keyBefore, err := os.ReadFile(env["HIBD_CA_KEY"])
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"ca", "init"}); code != 0 {
		t.Fatalf("second ca init exit %d: %s", code, &logs)
	}
	if !strings.Contains(out.String(), "existing") {
		t.Fatalf("idempotent output: %s", &out)
	}
	certAfter, _ := os.ReadFile(env["HIBD_CA_CERT"])
	keyAfter, _ := os.ReadFile(env["HIBD_CA_KEY"])
	if !bytes.Equal(certBefore, certAfter) || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("second init changed the CA")
	}
	if bytes.Contains(out.Bytes(), keyBefore) || bytes.Contains(logs.Bytes(), keyBefore) {
		t.Fatal("CA key reached output")
	}
	for path, mode := range map[string]os.FileMode{env["HIBD_CA_KEY"]: 0o600, env["HIBD_CA_CERT"]: 0o644} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode of %s: %v, %v", path, info, err)
		}
	}
}

func TestProxyCommandsRequireRootAndRejectArguments(t *testing.T) {
	for _, args := range [][]string{{"ca", "init"}, {"proxy"}, {"proxy", "-unhook"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a, _, logs := newApp(t, nil)
			a.EUID = func() int { return 1000 }
			if code := a.Run(t.Context(), args); code != 1 || !strings.Contains(logs.String(), "root") ||
				!strings.Contains(logs.String(), "sudo hibd") {
				t.Fatalf("root refusal: exit %d: %s", code, logs)
			}
		})
	}
	for _, args := range [][]string{{"ca"}, {"ca", "other"}, {"ca", "init", "extra"}, {"proxy", "extra"}, {"proxy", "-unhook", "extra"}, {"proxy", "-other"}} {
		a, _, logs := newApp(t, nil)
		a.EUID = func() int { return 0 }
		if code := a.Run(t.Context(), args); code != 2 {
			t.Fatalf("%v: exit %d: %s", args, code, logs)
		}
	}
}

func TestProxyMissingCAExplainsInitialization(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"HIBD_CONFIG":  filepath.Join(dir, "absent.env"),
		"E2B_API_KEY":  "test",
		"HIBD_CA_CERT": filepath.Join(dir, "ca.crt"),
		"HIBD_CA_KEY":  filepath.Join(dir, "ca.key"),
	}
	var out, logs bytes.Buffer
	a := &App{Stdout: &out, Stderr: &logs, Getenv: func(k string) string { return env[k] }, EUID: func() int { return 0 }}
	if code := a.Run(t.Context(), []string{"proxy"}); code != 1 ||
		!strings.Contains(logs.String(), "sudo hibd ca init") {
		t.Fatalf("missing CA: exit %d: %s", code, &logs)
	}
}

func TestProxyAnswersCodexRefreshWithOneStore(t *testing.T) {
	dir := t.TempDir()
	cfg := config{stateDir: dir, vaultPath: filepath.Join(dir, "vault.json"), vaultKeyPath: filepath.Join(dir, "vault.key")}
	now := time.Now()
	tokens := codextest.Tokens("acct-1234", "dev@example.com", "plus", now, now.Add(10*24*time.Hour))
	upstream := codextest.NewTokenServer(t, tokens)
	a := &App{CodexTokenURL: upstream.URL, CodexClient: upstream.Client}
	if _, err := a.codexStore(cfg, nil).CommitLogin(t.Context(), tokens); err != nil {
		t.Fatal(err)
	}
	placeholders, err := a.codexStore(cfg, nil).Public()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	server := &egress.Server{}
	a.serveCodex(server, cfg, func(format string, args ...any) { fmt.Fprintf(&logs, format+"\n", args...) })
	if len(server.Endpoints) != 1 || server.Rejected == nil {
		t.Fatalf("proxy has %d endpoints and Rejected set %v, want the token endpoint and Rejected", len(server.Endpoints), server.Rejected != nil)
	}
	ep := server.Endpoints[0]
	if ep.Site != codex.TokenSite || ep.Method != http.MethodPost || ep.Path != codex.TokenPath {
		t.Fatalf("endpoint = %s %s%s, want POST auth.openai.com/oauth/token", ep.Method, ep.Site, ep.Path)
	}
	refresh := func() {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": placeholders.RefreshToken})
		resp := ep.Handle(t.Context(), egress.EndpointRequest{Body: body, Audit: func(string) error { return nil }})
		var got struct {
			Access string `json:"access_token"`
		}
		if err := json.Unmarshal(resp.Body, &got); err != nil || resp.Status != http.StatusOK || got.Access != placeholders.AccessToken {
			t.Fatalf("refresh: status %d, placeholder answered %v", resp.Status, got.Access == placeholders.AccessToken)
		}
	}
	refresh()
	if upstream.Requests() != 0 {
		t.Fatal("a fresh login was refreshed")
	}
	server.Rejected(codex.AccessName, swap.Fingerprint(tokens.AccessToken))
	refresh()
	if upstream.Requests() != 1 {
		t.Fatalf("refresh after a rejection reached the token server %d times, want once", upstream.Requests())
	}
	if bytes.Contains(logs.Bytes(), []byte(tokens.AccessToken)) || bytes.Contains(logs.Bytes(), []byte(tokens.RefreshToken)) {
		t.Fatal("a real codex token reached the logs")
	}
}

type fakeHook struct {
	ensureErr, keepErr, removeErr error
	kept                          chan struct{}

	mu    sync.Mutex
	calls []string
}

func newFakeHook() *fakeHook { return &fakeHook{kept: make(chan struct{})} }

func (h *fakeHook) Ensure(context.Context) (bool, error) {
	h.record("ensure")
	return true, h.ensureErr
}

func (h *fakeHook) Keep(ctx context.Context, _ time.Duration, _ func(string, ...any)) error {
	h.record("keep")
	if h.keepErr != nil {
		return h.keepErr
	}
	close(h.kept)
	<-ctx.Done()
	return nil
}

func (h *fakeHook) Remove(ctx context.Context) error {
	if ctx.Err() != nil {
		h.record("remove with an ended context")
	} else {
		h.record("remove")
	}
	return h.removeErr
}

func (h *fakeHook) record(call string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
}

func (h *fakeHook) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

type testListener struct {
	net.Listener
	acceptErr error
	closed    atomic.Bool
}

func (l *testListener) Accept() (net.Conn, error) {
	if l.acceptErr != nil {
		return nil, l.acceptErr
	}
	return l.Listener.Accept()
}

func (l *testListener) Close() error {
	l.closed.Store(true)
	return l.Listener.Close()
}

func proxyApp(t *testing.T, hook *fakeHook) (a *App, env map[string]string, listener *testListener, listen *[]string) {
	t.Helper()
	dir := t.TempDir()
	env = map[string]string{
		"HIBD_CONFIG":     filepath.Join(dir, "absent.env"),
		"E2B_API_KEY":     "test",
		"HIBD_STATE_DIR":  dir,
		"HIBD_VAULT_KEY":  filepath.Join(dir, "vault.key"),
		"HIBD_CA_CERT":    filepath.Join(dir, "ca.crt"),
		"HIBD_CA_KEY":     filepath.Join(dir, "ca.key"),
		"HIBD_AUDIT_LOG":  filepath.Join(dir, "log", "audit.jsonl"),
		"HIBD_PROXY_PORT": "5443",
	}
	if _, err := ca.Init(env["HIBD_CA_CERT"], env["HIBD_CA_KEY"], "Hibernaut CA"); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener = &testListener{Listener: l}
	t.Cleanup(func() { l.Close() })
	listen = new([]string)
	a = &App{
		Stdout: &bytes.Buffer{}, Stderr: &syncBuffer{}, Getenv: func(k string) string { return env[k] },
		EUID:      func() int { return 0 },
		NewEngine: func() (engine.Engine, error) { return fake.New(), nil },
		ProxyHook: hook,
		ProxyListen: func(network, address string) (net.Listener, error) {
			*listen = append(*listen, network+" "+address)
			return listener, nil
		},
	}
	return a, env, listener, listen
}

func TestProxyStartupFailuresLeaveTheHookAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, a *App, env map[string]string)
		want  string
	}{
		{name: "no E2B key", want: "E2B_API_KEY is not set", setup: func(_ *testing.T, _ *App, env map[string]string) {
			delete(env, "E2B_API_KEY")
		}},
		{name: "bad config file", want: "want KEY=VALUE", setup: func(t *testing.T, _ *App, env map[string]string) {
			writeProxyFile(t, env["HIBD_CONFIG"], "not a setting\n")
		}},
		{name: "missing CA key", want: "CA is missing. Run sudo hibd ca init", setup: func(t *testing.T, _ *App, env map[string]string) {
			removeProxyFile(t, env["HIBD_CA_KEY"])
		}},
		{name: "unreadable CA cert", want: "reading CA cert", setup: func(t *testing.T, _ *App, env map[string]string) {
			writeProxyFile(t, env["HIBD_CA_CERT"], "not a certificate\n")
		}},
		{name: "port not a number", want: "HIBD_PROXY_PORT must be a port from 1 to 65535", setup: setProxyEnv("HIBD_PROXY_PORT", "https")},
		{name: "port zero", want: "HIBD_PROXY_PORT must be a port from 1 to 65535", setup: setProxyEnv("HIBD_PROXY_PORT", "0")},
		{name: "port too big", want: "HIBD_PROXY_PORT must be a port from 1 to 65535", setup: setProxyEnv("HIBD_PROXY_PORT", "65536")},
		{name: "engine", want: "engine unavailable", setup: func(_ *testing.T, a *App, _ map[string]string) {
			a.NewEngine = func() (engine.Engine, error) { return nil, errors.New("engine unavailable") }
		}},
		{name: "audit directory", want: "not a directory", setup: func(t *testing.T, _ *App, env map[string]string) {
			file := filepath.Join(env["HIBD_STATE_DIR"], "file")
			writeProxyFile(t, file, "")
			env["HIBD_AUDIT_LOG"] = filepath.Join(file, "log", "audit.jsonl")
		}},
		{name: "audit log", want: "is a directory", setup: func(t *testing.T, _ *App, env map[string]string) {
			if err := os.MkdirAll(env["HIBD_AUDIT_LOG"], 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "port taken", want: "address already in use", setup: func(_ *testing.T, a *App, _ map[string]string) {
			a.ProxyListen = func(string, string) (net.Listener, error) { return nil, errors.New("address already in use") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := newFakeHook()
			a, env, _, _ := proxyApp(t, hook)
			tc.setup(t, a, env)
			if code := a.Run(t.Context(), []string{"proxy"}); code != 1 {
				t.Fatalf("exit %d, want 1: %s", code, a.Stderr)
			}
			if logs := fmt.Sprint(a.Stderr); !strings.Contains(logs, "hibd proxy: ") || !strings.Contains(logs, tc.want) {
				t.Fatalf("stderr = %q, want %q", logs, tc.want)
			}
			if calls := hook.Calls(); len(calls) > 0 {
				t.Fatalf("a failed start touched the hook: %q", calls)
			}
		})
	}
}

func TestProxyListensOnPortsAtTheBoundaries(t *testing.T) {
	for _, port := range []string{"1", "65535"} {
		hook := newFakeHook()
		a, env, _, listen := proxyApp(t, hook)
		env["HIBD_PROXY_PORT"] = port
		a.ProxyListen = func(network, address string) (net.Listener, error) {
			*listen = append(*listen, network+" "+address)
			return nil, errors.New("not listening in tests")
		}
		if code := a.Run(t.Context(), []string{"proxy"}); code != 1 || !strings.Contains(fmt.Sprint(a.Stderr), "not listening in tests") {
			t.Fatalf("port %s: exit %d: %s", port, code, a.Stderr)
		}
		if want := []string{"tcp :" + port}; !slices.Equal(*listen, want) {
			t.Fatalf("port %s: listened on %q, want %q", port, *listen, want)
		}
	}
}

func TestProxyServesUntilCancelledAndUnhooksOnEveryExit(t *testing.T) {
	ensureErr := errors.New("iptables refused the rule")
	keepErr := errors.New("iptables went away")
	acceptErr := errors.New("accept failed")
	removeErr := errors.New("iptables is locked")
	hooked := []string{"ensure", "keep", "remove"}
	for _, tc := range []struct {
		name                          string
		ensureErr, keepErr, removeErr error
		acceptErr                     error
		cancel                        bool
		want                          string
		calls                         []string
	}{
		{name: "context ends", cancel: true, calls: hooked},
		{name: "hooking fails", ensureErr: ensureErr, want: "installing proxy hook: " + ensureErr.Error(), calls: []string{"ensure", "remove"}},
		{name: "keeping the hook fails", keepErr: keepErr, want: keepErr.Error(), calls: hooked},
		{name: "serving fails", acceptErr: acceptErr, want: acceptErr.Error(), calls: hooked},
		{name: "unhooking fails", cancel: true, removeErr: removeErr, want: "removing proxy hook: " + removeErr.Error(), calls: hooked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := newFakeHook()
			hook.ensureErr, hook.keepErr, hook.removeErr = tc.ensureErr, tc.keepErr, tc.removeErr
			a, env, listener, listen := proxyApp(t, hook)
			listener.acceptErr = tc.acceptErr
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan int, 1)
			go func() { done <- a.Run(ctx, []string{"proxy"}) }()
			if tc.cancel {
				select {
				case <-hook.kept:
				case <-time.After(5 * time.Second):
					t.Fatal("proxy did not keep the hook")
				}
				assertServes(t, listener.Addr().String())
				cancel()
			}
			var code int
			select {
			case code = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("proxy did not stop")
			}
			wantCode := 0
			if tc.want != "" {
				wantCode = 1
			}
			if code != wantCode || !strings.Contains(fmt.Sprint(a.Stderr), tc.want) {
				t.Fatalf("exit %d, want %d and %q: %s", code, wantCode, tc.want, a.Stderr)
			}
			if calls := hook.Calls(); !slices.Equal(calls, tc.calls) {
				t.Fatalf("hook calls = %q, want %q", calls, tc.calls)
			}
			if want := []string{"tcp :5443"}; !slices.Equal(*listen, want) {
				t.Fatalf("listened on %q, want %q", *listen, want)
			}
			if !listener.closed.Load() {
				t.Fatal("proxy left its port open")
			}
			if _, err := os.Stat(env["HIBD_AUDIT_LOG"]); err != nil {
				t.Fatalf("audit log: %v", err)
			}
		})
	}
}

func TestProxyUnhookRemovesTheHookWithoutConfiguration(t *testing.T) {
	hook := newFakeHook()
	a, env, _, listen := proxyApp(t, hook)
	clear(env)
	if code := a.Run(t.Context(), []string{"proxy", "-unhook"}); code != 0 {
		t.Fatalf("unhook exit %d: %s", code, a.Stderr)
	}
	if calls := hook.Calls(); !slices.Equal(calls, []string{"remove"}) {
		t.Fatalf("hook calls = %q, want one remove", calls)
	}
	hook.removeErr = errors.New("iptables is locked")
	if code := a.Run(t.Context(), []string{"proxy", "-unhook"}); code != 1 ||
		!strings.Contains(fmt.Sprint(a.Stderr), "hibd proxy: iptables is locked") {
		t.Fatalf("failed unhook: exit %d: %s", code, a.Stderr)
	}
	if len(*listen) > 0 {
		t.Fatalf("unhook listened on %q", *listen)
	}
}

func TestProxyUnhookWarnsAboutAStrayRule(t *testing.T) {
	rule := newFakeHook()
	a, _, _, _ := proxyApp(t, rule)
	rule.removeErr = fmt.Errorf("removing proxy hook: %w", hook.ErrStrayRule)
	if code := a.Run(t.Context(), []string{"proxy", "-unhook"}); code != 0 {
		t.Fatalf("unhook exit %d, want 0: %s", code, a.Stderr)
	}
	if logs := fmt.Sprint(a.Stderr); !strings.Contains(logs, "hibd proxy: warning: ") || !strings.Contains(logs, hook.ErrStrayRule.Error()) {
		t.Fatalf("stderr = %q, want a warning about the stray rule", logs)
	}
	if calls := rule.Calls(); !slices.Equal(calls, []string{"remove"}) {
		t.Fatalf("hook calls = %q, want one remove", calls)
	}
}

func assertServes(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.Read(make([]byte, 1)); n != 0 || err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("proxy did not answer a non-machine connection: read %d, %v", n, err)
	}
}

func setProxyEnv(key, value string) func(*testing.T, *App, map[string]string) {
	return func(_ *testing.T, _ *App, env map[string]string) { env[key] = value }
}

func writeProxyFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeProxyFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
