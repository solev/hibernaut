package hibd

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/egress/swap"
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
