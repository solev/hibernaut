package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
)

// TestMain lets the test binary play codex: hibd runs it through a link named codex.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "codex" {
		os.Exit(fakeCodex())
	}
	// t.TempDir makes directories with mode 0777 less the umask. Under Ubuntu's default of 002
	// they are group-writable, and the state directory checks rightly refuse them.
	syscall.Umask(0o022)
	os.Exit(m.Run())
}

const (
	fakeCodexCalls   = "calls.jsonl"
	fakeCodexVersion = "version"
	fakeCodexAuth    = "auth.json"
	fakeCodexFail    = "fail"
	fakeCodexHang    = "hang"
	fakeCodexStarted = "started"
	fakeDeviceCode   = "FAKE-7Q2K"
)

type fakeCodexCall struct {
	Args          []string `json:"args"`
	Env           []string `json:"env"`
	Dir           string   `json:"dir"`
	CodexHomeMode string   `json:"codexHomeMode"`
}

func fakeCodex() int {
	here := filepath.Dir(os.Args[0])
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(here, name)); return err == nil }
	call := fakeCodexCall{Args: os.Args[1:], Env: os.Environ()}
	call.Dir, _ = os.Getwd()
	if info, err := os.Stat(os.Getenv("CODEX_HOME")); err == nil && info.IsDir() {
		call.CodexHomeMode = fmt.Sprintf("%#o", info.Mode().Perm())
	}
	line, _ := json.Marshal(call)
	f, err := os.OpenFile(filepath.Join(here, fakeCodexCalls), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 70
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	if slices.Equal(os.Args[1:], []string{"--version"}) {
		version, err := os.ReadFile(filepath.Join(here, fakeCodexVersion))
		if err != nil {
			version = []byte("codex-cli " + codex.Version + "\n")
		}
		os.Stdout.Write(version)
		return 0
	}
	if len(os.Args) < 2 || os.Args[1] != "login" {
		fmt.Fprintf(os.Stderr, "fake codex: unexpected %q\n", os.Args[1:])
		return 2
	}
	fmt.Fprintf(os.Stderr, "\x1b[94mFollow these steps to sign in with ChatGPT using device code authorization:\x1b[0m\n\n"+
		"1. Open this link in your browser and sign in to your account\n   https://auth.openai.com/codex/device\n\n"+
		"2. Enter this one-time code (expires in 15 minutes)\n   %s\n", fakeDeviceCode)
	switch {
	case exists(fakeCodexFail):
		fmt.Fprintln(os.Stderr, "Error logging in with device code: device code request failed with status 404 Not Found")
		return 1
	case exists(fakeCodexHang):
		_ = os.WriteFile(filepath.Join(here, fakeCodexStarted), fmt.Appendf(nil, "%d", os.Getpid()), 0o600)
		time.Sleep(time.Hour)
		return 3
	}
	auth, err := os.ReadFile(filepath.Join(here, fakeCodexAuth))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake codex: no auth.json to copy")
		return 70
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), auth, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "fake codex:", err)
		return 70
	}
	fmt.Fprintln(os.Stderr, "Successfully logged in")
	return 0
}

type fakeCodexBin struct {
	dir, bin string
}

func newFakeCodex(t *testing.T, auth []byte) fakeCodexBin {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := fakeCodexBin{dir: t.TempDir()}
	f.bin = filepath.Join(f.dir, "codex")
	if err := os.Symlink(self, f.bin); err != nil {
		t.Fatal(err)
	}
	if auth != nil {
		f.write(t, fakeCodexAuth, string(auth))
	}
	return f
}

func (f fakeCodexBin) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f fakeCodexBin) calls(t *testing.T) []fakeCodexCall {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, fakeCodexCalls))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []fakeCodexCall
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
		var c fakeCodexCall
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func newCodexApp(t *testing.T, guest *secretGuest, tokens codex.Tokens) (*App, *bytes.Buffer, *bytes.Buffer, fakeCodexBin) {
	t.Helper()
	e := fake.New()
	if guest != nil {
		e = runningApp(guest)
	}
	a, out, errOut := newApp(t, e)
	withSecretConfig(t, a)
	a.EUID = func() int { return 0 }
	server := codextest.NewTokenServer(t, tokens)
	a.CodexTokenURL, a.CodexClient = server.URL, server.Client
	f := newFakeCodex(t, codextest.AuthFile(tokens))
	a.CodexBin = f.bin
	return a, out, errOut, f
}

func testCodexTokens() codex.Tokens {
	now := time.Now()
	return codextest.Tokens("acct-1234", "dev@example.com", "plus", now, now.Add(10*24*time.Hour))
}

func wantNoCodexToken(t *testing.T, tokens codex.Tokens, outputs ...[]byte) {
	t.Helper()
	for _, token := range []string{tokens.AccessToken, tokens.RefreshToken, tokens.IDToken} {
		for i, s := range outputs {
			if bytes.Contains(s, []byte(token)) {
				t.Errorf("output %d holds a real codex token", i)
			}
		}
	}
}

func codexStoreFor(a *App) *codex.Store {
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		panic(err)
	}
	return a.codexStore(cfg, nil)
}

func codexLogins(t *testing.T, a *App) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "codex-login-*"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestConnectCodex(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	tokens := testCodexTokens()
	a, out, errOut, f := newCodexApp(t, guest, tokens)
	state := a.Getenv("HIBD_STATE_DIR")
	leftover := filepath.Join(state, "codex-login-1234")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, r := runJSONReply(t, a, out, "connect", "codex", "-json")
	if code != 0 || r.Codex == nil {
		t.Fatalf("connect codex exit %d, reply %+v; stderr %s", code, r, errOut)
	}
	if r.Codex.Email != "dev@example.com" || r.Codex.Plan != "plus" || r.Codex.AccountID != "acct-1234" || r.Codex.Health != hibapi.CodexOK {
		t.Errorf("reply = %+v, want dev@example.com on plus, account acct-1234, health ok", r.Codex)
	}

	calls := f.calls(t)
	if len(calls) != 2 {
		t.Fatalf("codex ran %d times, want twice: --version, then login", len(calls))
	}
	var home string
	for _, kv := range calls[0].Env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	if filepath.Dir(home) != state || !strings.HasPrefix(filepath.Base(home), "codex-login-") {
		t.Errorf("HOME = %s, want a codex-login- directory in %s", home, state)
	}
	// The state directory may be behind a symbolic link, as on macOS.
	resolved, err := filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	realHome := filepath.Join(resolved, filepath.Base(home))
	wantEnv := []string{"CODEX_HOME=" + home, "HOME=" + home, "LANG=C.UTF-8", "PATH=/usr/bin:/bin"}
	for i, wantArgs := range [][]string{{"--version"}, {"login", "--device-auth", "-c", `cli_auth_credentials_store="file"`}} {
		c := calls[i]
		if !slices.Equal(c.Args, wantArgs) {
			t.Errorf("codex call %d args = %q, want %q", i, c.Args, wantArgs)
		}
		if env := slices.Sorted(slices.Values(c.Env)); !slices.Equal(env, wantEnv) {
			t.Errorf("codex call %d environment = %q, want exactly %q", i, env, wantEnv)
		}
		if c.Dir != realHome || c.CodexHomeMode != "0700" {
			t.Errorf("codex call %d ran in %s, CODEX_HOME mode %s; want %s, 0700", i, c.Dir, c.CodexHomeMode, realHome)
		}
	}
	if logins := codexLogins(t, a); len(logins) != 0 {
		t.Errorf("login directories left: %q", logins)
	}

	if !strings.Contains(errOut.String(), fakeDeviceCode) || !strings.Contains(errOut.String(), "https://auth.openai.com/codex/device") {
		t.Errorf("stderr = %q, want codex's code and link", errOut)
	}
	if strings.Contains(errOut.String(), "\x1b") {
		t.Errorf("stderr = %q, want no terminal escapes", errOut)
	}

	store := codexStoreFor(a)
	p, err := store.Load()
	if err != nil || p.AccountID != "acct-1234" {
		t.Fatalf("profile = %+v, %v", p, err)
	}
	pub, err := store.Public()
	if err != nil {
		t.Fatal(err)
	}
	want, err := codex.RenderAuthFile(pub)
	if err != nil {
		t.Fatal(err)
	}
	if got := guest.codexAuthFile(); !bytes.Equal(got, want) {
		t.Errorf("the running machine's auth.json isn't the placeholder login file:\n%s", got)
	}
	var requests []byte
	for _, req := range guest.allRequests() {
		requests = append(requests, strings.Join(req.Args, " ")...)
	}
	wantNoCodexToken(t, tokens, out.Bytes(), errOut.Bytes(), guest.codexAuthFile(), requests, []byte(guest.envString()))
}

func TestConnectCodexSaysWho(t *testing.T) {
	a, out, errOut, _ := newCodexApp(t, nil, testCodexTokens())
	if code := a.Run(t.Context(), []string{"connect", "codex"}); code != 0 {
		t.Fatalf("exit %d; stderr %s", code, errOut)
	}
	if want := "connected Codex as dev@example.com, on the plus plan\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

func TestConnectCodexChecksTheVersion(t *testing.T) {
	a, out, errOut, f := newCodexApp(t, nil, testCodexTokens())
	f.write(t, fakeCodexVersion, "codex-cli 0.147.0\x1b[2J\n")
	code, r := runJSONReply(t, a, out, "connect", "codex", "-json")
	if code != 1 || !strings.Contains(r.Error, "codex-cli 0.147.0") || !strings.Contains(r.Error, "codex-cli "+codex.Version) ||
		strings.Contains(r.Error, "\x1b") {
		t.Errorf("exit %d, error %q; want 1, naming both versions, without escapes", code, r.Error)
	}
	if calls := f.calls(t); len(calls) != 1 {
		t.Errorf("codex ran %d times, want only --version", len(calls))
	}
	if _, err := codexStoreFor(a).Load(); !errors.Is(err, codex.ErrNotConnected) {
		t.Errorf("Load = %v, want not connected", err)
	}
	if logins := codexLogins(t, a); len(logins) != 0 {
		t.Errorf("login directories left: %q", logins)
	}
	_ = errOut
}

func TestConnectCodexHintsWhenTheLoginFails(t *testing.T) {
	a, out, errOut, f := newCodexApp(t, nil, testCodexTokens())
	f.write(t, fakeCodexFail, "")
	code, r := runJSONReply(t, a, out, "connect", "codex", "-json")
	if code != 1 || !strings.Contains(r.Error, "turn on device code login in ChatGPT, under Settings, Security") ||
		!strings.Contains(r.Error, "hib connect codex") {
		t.Errorf("exit %d, error %q; want 1, with the hint", code, r.Error)
	}
	if !strings.Contains(errOut.String(), "404 Not Found") {
		t.Errorf("stderr = %q, want codex's own error", errOut)
	}
	if logins := codexLogins(t, a); len(logins) != 0 {
		t.Errorf("login directories left: %q", logins)
	}
	if _, err := codexStoreFor(a).Load(); !errors.Is(err, codex.ErrNotConnected) {
		t.Errorf("Load = %v, want not connected", err)
	}
}

func TestConnectCodexRefusesABadLoginFile(t *testing.T) {
	for name, auth := range map[string]string{"none": "", "api key": `{"OPENAI_API_KEY":"sk-test","auth_mode":"apikey"}`} {
		t.Run(name, func(t *testing.T) {
			a, out, _, f := newCodexApp(t, nil, testCodexTokens())
			if auth == "" {
				if err := os.Remove(filepath.Join(f.dir, fakeCodexAuth)); err != nil {
					t.Fatal(err)
				}
			} else {
				f.write(t, fakeCodexAuth, auth)
			}
			code, r := runJSONReply(t, a, out, "connect", "codex", "-json")
			if code != 1 || r.Error == "" {
				t.Errorf("exit %d, reply %+v; want an error", code, r)
			}
			if logins := codexLogins(t, a); len(logins) != 0 {
				t.Errorf("login directories left: %q", logins)
			}
			if _, err := codexStoreFor(a).Load(); !errors.Is(err, codex.ErrNotConnected) {
				t.Errorf("Load = %v, want not connected", err)
			}
		})
	}
}

func TestConnectCodexNeedsRoot(t *testing.T) {
	a, out, _, f := newCodexApp(t, nil, testCodexTokens())
	a.EUID = func() int { return 1000 }
	code, r := runJSONReply(t, a, out, "connect", "codex", "-json")
	if code != 1 || !strings.Contains(r.Error, "sudo hibd connect codex") {
		t.Errorf("exit %d, error %q; want 1, saying to run it as root", code, r.Error)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("codex ran %d times, want never", len(calls))
	}
}

func TestConnectCodexStopsCodexWhenCancelled(t *testing.T) {
	a, _, errOut, f := newCodexApp(t, nil, testCodexTokens())
	f.write(t, fakeCodexHang, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"connect", "codex"}) }()
	var pid int
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if data, err := os.ReadFile(filepath.Join(f.dir, fakeCodexStarted)); err == nil && len(data) > 0 {
			if _, err := fmt.Sscan(string(data), &pid); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("codex login never started")
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("connect codex didn't stop after the context was cancelled")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("codex is still running after the context was cancelled: %v", err)
	}
	if logins := codexLogins(t, a); len(logins) != 0 {
		t.Errorf("login directories left: %q", logins)
	}
	if !strings.Contains(errOut.String(), "cancelled") {
		t.Errorf("stderr = %q, want it to say the login was cancelled", errOut)
	}
}

func TestDaemonDeletesLeftoverCodexLogins(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	state := a.Getenv("HIBD_STATE_DIR")
	leftover := filepath.Join(state, "codex-login-99", "sub")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(state, "codex-logins")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	logs, stop := startDaemon(t, a)
	defer stop()
	waitFor(t, logs, "the daemon starts", func() bool { return strings.Contains(logs.String(), "keeping busy machines running") })
	if logins := codexLogins(t, a); len(logins) != 0 {
		t.Errorf("login directories left after the daemon started: %q", logins)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("the daemon deleted more than login directories: %v", err)
	}
}

func pinnedCodex(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("HIB_CODEX")
	if bin == "" {
		if os.Getenv("HIB_REQUIRE_CODEX") == "1" {
			t.Fatal("HIB_REQUIRE_CODEX=1, but HIB_CODEX doesn't name the pinned codex")
		}
		t.Skip("set HIB_CODEX to the pinned codex to run this test")
	}
	return bin
}

func TestCheckCodexVersionAcceptsThePinnedCodex(t *testing.T) {
	bin := pinnedCodex(t)
	if err := checkCodexVersion(t.Context(), bin, t.TempDir()); err != nil {
		t.Fatalf("the pinned codex failed the version check: %v", err)
	}
}
