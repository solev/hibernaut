package hibd

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
)

const (
	hibdDeviceCode = "hibdDeviceCode0123456789abcdef"
	devicePrompt   = "To connect GitHub, open https://github.com/login/device and enter the code WDJB-MJHT within 15 minutes.\n" +
		"Waiting for GitHub. Press Ctrl-C to cancel.\n"
	codeBody      = `{"device_code":"` + hibdDeviceCode + `","user_code":"WDJB-MJHT","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`
	pendingBody   = `{"error":"authorization_pending"}`
	tokenBody     = `{"access_token":"` + testToken + `","token_type":"bearer","scope":"repo,read:org,workflow"}`
	cancelledText = "the GitHub login was cancelled. Nothing was stored"
)

type deviceAnswer struct {
	status int
	body   string
}

type fakeDevice struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	code  deviceAnswer
	polls []deviceAnswer
	codes int
	gets  int
	now   time.Time
	waits int
	hook  func(n int, ctx context.Context)
}

func newFakeDevice(t *testing.T, a *App, code deviceAnswer, polls ...deviceAnswer) *fakeDevice {
	t.Helper()
	d := &fakeDevice{t: t, code: code, polls: polls, now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	d.srv = httptest.NewUnstartedServer(http.HandlerFunc(d.serve))
	d.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	d.srv.StartTLS()
	t.Cleanup(d.srv.Close)
	a.GitHubDeviceFlow = &github.DeviceFlow{Client: d.srv.Client(), Base: d.srv.URL, ClientID: "Ov23liHibdTest", Now: d.clock, Sleep: d.sleep}
	return d
}

func (d *fakeDevice) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	var a deviceAnswer
	switch r.URL.Path {
	case "/login/device/code":
		d.codes++
		a = d.code
	case "/login/oauth/access_token":
		d.gets++
		if len(d.polls) == 0 {
			d.t.Error("fake GitHub got a poll it has no answer for")
		} else {
			a = d.polls[min(d.gets, len(d.polls))-1]
		}
	default:
		d.t.Errorf("fake GitHub got %s %s", r.Method, r.URL.Path)
	}
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(cmp.Or(a.status, http.StatusOK))
	io.WriteString(w, a.body)
}

func (d *fakeDevice) clock() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.now
}

func (d *fakeDevice) sleep(ctx context.Context, wait time.Duration) error {
	d.mu.Lock()
	d.waits++
	n, hook := d.waits, d.hook
	d.mu.Unlock()
	if hook != nil {
		hook(n, ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	d.now = d.now.Add(wait)
	d.mu.Unlock()
	return nil
}

func (d *fakeDevice) requests() (codes, polls int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.codes, d.gets
}

func waitUntilDone(t *testing.T, ctx context.Context, what string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Errorf("%s didn't stop the wait", what)
	}
}

func openStdin(t *testing.T, a *App) io.Closer {
	t.Helper()
	r, w := io.Pipe()
	a.Stdin = r
	t.Cleanup(func() { w.Close() })
	return w
}

func connectDevice(t *testing.T, ctx context.Context, a *App, out *bytes.Buffer) (int, hibapi.Response) {
	t.Helper()
	out.Reset()
	code := a.Run(ctx, []string{"connect", "github", "-source", "device", "-json"})
	return code, decodeReply(t, out)
}

func decodeReply(t *testing.T, out *bytes.Buffer) hibapi.Response {
	t.Helper()
	r, err := hibapi.DecodeResponse(out.Bytes())
	if remote, ok := errors.AsType[*hibapi.RemoteError](err); ok {
		return hibapi.Response{Error: remote.Msg}
	}
	if err != nil {
		t.Fatalf("hibd printed %q: %v", out, err)
	}
	return r
}

func wantNothingStored(t *testing.T, a *App, gh *fakeGitHub) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json")); !os.IsNotExist(err) {
		t.Errorf("vault.json: %v, want none", err)
	}
	if _, err := os.Stat(profilePath(a)); !os.IsNotExist(err) {
		t.Errorf("github.json: %v, want none", err)
	}
	if n := gh.callCount(); n != 0 {
		t.Errorf("GitHub's API got %d calls, want none", n)
	}
}

func wantNoDeviceSecrets(t *testing.T, outputs ...string) {
	t.Helper()
	wantNoToken(t, nil, outputs...)
	for _, s := range outputs {
		if strings.Contains(s, hibdDeviceCode) {
			t.Errorf("the device code leaked into output:\n%s", strings.ReplaceAll(s, hibdDeviceCode, "<CODE>"))
		}
	}
}

func TestConnectGitHubWithTheDeviceFlow(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	a, out, errOut, gh := newGitHubApp(t, runningApp(guest))
	gh.set(testToken, octocat)
	d := newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: pendingBody}, deviceAnswer{body: tokenBody})
	openStdin(t, a)

	code, r := connectDevice(t, t.Context(), a, out)
	if code != 0 {
		t.Fatalf("exit %d: %+v, stderr %q", code, r, errOut)
	}
	if errOut.String() != devicePrompt {
		t.Errorf("stderr = %q, want %q", errOut, devicePrompt)
	}
	want := hibapi.GitHub{Login: "octocat", ID: 583231, Name: "The Octocat", Email: testEmail, Source: "device",
		Scopes: []string{"repo", "workflow", "read:org"}, ScopesKnown: true}
	if r.GitHub == nil || !equalGitHub(*r.GitHub, want) {
		t.Errorf("reply = %+v, want %+v", r.GitHub, want)
	}
	if codes, polls := d.requests(); codes != 1 || polls != 2 {
		t.Errorf("fake GitHub got %d code requests and %d polls, want 1 and 2", codes, polls)
	}
	if gh.callCount() != 1 {
		t.Errorf("GitHub's API got %d calls, want 1", gh.callCount())
	}
	revealed := openVault(t, a)
	if len(revealed) != 1 || revealed[0].Name != "github" || revealed[0].Env != "GH_TOKEN" || revealed[0].Value != testToken {
		t.Error("the vault doesn't hold the device flow's token as secret github")
	}
	p, err := github.LoadProfile(profilePath(a))
	if err != nil || p == nil || *p != (github.Profile{Login: "octocat", ID: 583231, Name: "The Octocat", Source: "device"}) {
		t.Errorf("profile = %+v, %v, want octocat from the device flow", p, err)
	}
	if !strings.Contains(guest.envString(), "GH_TOKEN=") {
		t.Errorf("/etc/environment = %q, want GH_TOKEN", guest.envString())
	}
	wantNoToken(t, guest, out.String(), errOut.String())
	wantNoDeviceSecrets(t, out.String(), errOut.String())
}

func TestConnectGitHubWithTheDeviceFlowSaysWhoAndWarnsAboutScopes(t *testing.T) {
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	acct := octocat
	acct.scopes = scopes("repo")
	gh.set(testToken, acct)
	newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody})
	openStdin(t, a)
	if code := a.Run(t.Context(), []string{"connect", "github", "-source", "device"}); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if out.String() != "connected GitHub as octocat\n" {
		t.Errorf("stdout = %q", out)
	}
	g := hibapi.GitHub{Source: "device", Scopes: []string{"repo"}, ScopesKnown: true}
	if want := devicePrompt + "warning: " + g.ScopeWarning() + "\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
	wantNoDeviceSecrets(t, out.String(), errOut.String())
}

func TestConnectGitHubWhileAnotherIsRunning(t *testing.T) {
	for _, source := range []string{"gh", "token", "device"} {
		t.Run(source, func(t *testing.T) {
			a, out, errOut, gh := newGitHubApp(t, fake.New())
			gh.set(testToken, octocat)
			d := newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody})
			unlock, ok, err := lockGitHubLogins(a.Getenv("HIBD_STATE_DIR"))
			if err != nil || !ok {
				t.Fatalf("taking the GitHub login lock: %v, %v", ok, err)
			}
			defer unlock()
			openStdin(t, a)
			exit := make(chan int, 1)
			go func() { exit <- a.Run(t.Context(), []string{"connect", "github", "-source", source, "-json"}) }()
			var code int
			select {
			case code = <-exit:
			case <-time.After(10 * time.Second):
				t.Fatal("hibd waited for stdin, which never ends, instead of failing on the busy lock")
			}
			r := decodeReply(t, out)
			if want := "another hib connect github is running: finish it, or stop it, then try again"; code != 1 || r.Error != want {
				t.Errorf("exit %d, error %q, want 1 and %q", code, r.Error, want)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut)
			}
			if codes, polls := d.requests(); codes+polls != 0 {
				t.Errorf("fake GitHub got %d requests, want none", codes+polls)
			}
			wantNothingStored(t, a, gh)
		})
	}
}

func TestConnectGitHubWithTheDeviceFlowStopsWhenStdinEnds(t *testing.T) {
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	gh.set(testToken, octocat)
	d := newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: pendingBody}, deviceAnswer{body: tokenBody})
	stdin := openStdin(t, a)
	d.hook = func(n int, ctx context.Context) {
		if n == 2 {
			stdin.Close()
			waitUntilDone(t, ctx, "the end of stdin")
		}
	}
	code, r := connectDevice(t, t.Context(), a, out)
	if code != 1 || r.Error != cancelledText {
		t.Errorf("exit %d, error %q, want 1 and %q", code, r.Error, cancelledText)
	}
	if errOut.String() != devicePrompt {
		t.Errorf("stderr = %q, want %q", errOut, devicePrompt)
	}
	if codes, polls := d.requests(); codes != 1 || polls != 1 {
		t.Errorf("fake GitHub got %d code requests and %d polls, want 1 and 1", codes, polls)
	}
	wantNothingStored(t, a, gh)
}

func TestConnectGitHubWithTheDeviceFlowStopsWhenStdinHasEnded(t *testing.T) {
	a, out, _, gh := newGitHubApp(t, fake.New())
	gh.set(testToken, octocat)
	d := newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody})
	a.Stdin = strings.NewReader("")
	d.hook = func(_ int, ctx context.Context) { waitUntilDone(t, ctx, "stdin that had ended") }
	code, r := connectDevice(t, t.Context(), a, out)
	if code != 1 || r.Error != cancelledText {
		t.Errorf("exit %d, error %q, want 1 and %q", code, r.Error, cancelledText)
	}
	if _, polls := d.requests(); polls != 0 {
		t.Errorf("fake GitHub got %d polls, want none", polls)
	}
	wantNothingStored(t, a, gh)
}

func TestConnectGitHubWithTheDeviceFlowStopsWithItsContext(t *testing.T) {
	a, out, _, gh := newGitHubApp(t, fake.New())
	gh.set(testToken, octocat)
	d := newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: pendingBody})
	openStdin(t, a)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.hook = func(n int, _ context.Context) {
		if n == 2 {
			cancel()
		}
	}
	code, r := connectDevice(t, ctx, a, out)
	if code != 1 || r.Error != cancelledText {
		t.Errorf("exit %d, error %q, want 1 and %q", code, r.Error, cancelledText)
	}
	if _, polls := d.requests(); polls != 1 {
		t.Errorf("fake GitHub got %d polls, want 1", polls)
	}
	wantNothingStored(t, a, gh)
}

type endWatch struct {
	r     io.Reader
	once  sync.Once
	ended chan struct{}
}

func (w *endWatch) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if err != nil {
		w.once.Do(func() { close(w.ended) })
	}
	return n, err
}

type beforeEach struct {
	next http.RoundTripper
	hook func()
}

func (b beforeEach) RoundTrip(r *http.Request) (*http.Response, error) {
	b.hook()
	return b.next.RoundTrip(r)
}

func TestConnectGitHubWithTheDeviceFlowStoresATokenAfterStdinEnds(t *testing.T) {
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	gh.set(testToken, octocat)
	newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody})
	r, w := io.Pipe()
	t.Cleanup(func() { w.Close() })
	stdin := &endWatch{r: r, ended: make(chan struct{})}
	a.Stdin = stdin
	client := *a.GitHubClient
	client.Transport = beforeEach{next: client.Transport, hook: func() {
		w.Close()
		select {
		case <-stdin.ended:
		case <-time.After(10 * time.Second):
			t.Error("hibd didn't see stdin end")
		}
	}}
	a.GitHubClient = &client

	code, reply := connectDevice(t, t.Context(), a, out)
	if code != 0 || reply.GitHub == nil || reply.GitHub.Source != "device" {
		t.Fatalf("exit %d, reply %+v, stderr %q", code, reply, errOut)
	}
	if revealed := openVault(t, a); len(revealed) != 1 || revealed[0].Value != testToken {
		t.Error("the vault doesn't hold the token GitHub granted")
	}
	if p, err := github.LoadProfile(profilePath(a)); err != nil || p == nil || p.Source != "device" {
		t.Errorf("profile = %+v, %v, want source device", p, err)
	}
}

func TestConnectGitHubWithTheDeviceFlowFails(t *testing.T) {
	expiring := `{"access_token":"` + testToken + `","expires_in":28800,"refresh_token":"ghr_refresh","token_type":"bearer"}`
	for _, tc := range []struct {
		name     string
		code     deviceAnswer
		poll     deviceAnswer
		clientID string
		want     string
		prompt   string
	}{
		{"expired", deviceAnswer{body: codeBody}, deviceAnswer{body: `{"error":"expired_token"}`}, "",
			"the GitHub code expired. Run hib connect github again for a new code", devicePrompt},
		{"expired while retrying", deviceAnswer{body: strings.Replace(codeBody, `"expires_in":900`, `"expires_in":20`, 1)},
			deviceAnswer{status: http.StatusServiceUnavailable}, "",
			"the GitHub code expired while hibd was retrying: GitHub answered 503 Service Unavailable. Run hib connect github again for a new code",
			strings.Replace(devicePrompt, "15 minutes", "1 minute", 1)},
		{"denied", deviceAnswer{body: codeBody}, deviceAnswer{status: http.StatusBadRequest, body: `{"error":"access_denied"}`}, "",
			"the GitHub login was denied. Nothing was stored", devicePrompt},
		{"app refused", deviceAnswer{status: http.StatusBadRequest, body: `{"error":"device_flow_disabled"}`}, deviceAnswer{}, "",
			"GitHub refused the Hibernaut OAuth App. Its owner must turn on Device Flow at github.com/settings/developers. " +
				"Until then, run hib connect github --from-gh or --token", ""},
		{"no client ID", deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody}, "none",
			"this hibd has no GitHub OAuth App to log in to. Run hib connect github --from-gh or --token", ""},
		{"expiring token", deviceAnswer{body: codeBody}, deviceAnswer{body: expiring}, "",
			"GitHub gave a token that expires, and hibd can't refresh tokens. The Hibernaut OAuth App's owner must opt out of " +
				"token expiration in its Optional features at github.com/settings/developers. Then run hib connect github again",
			devicePrompt},
		{"another refusal", deviceAnswer{body: codeBody}, deviceAnswer{body: `{"error":"incorrect_device_code"}`}, "",
			"the GitHub login failed: GitHub refused the device login (incorrect_device_code). Nothing was stored", devicePrompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, out, errOut, gh := newGitHubApp(t, fake.New())
			gh.set(testToken, octocat)
			d := newFakeDevice(t, a, tc.code, tc.poll)
			if tc.clientID == "none" {
				a.GitHubDeviceFlow.ClientID = ""
			}
			openStdin(t, a)
			code, r := connectDevice(t, t.Context(), a, out)
			if code != 1 || r.Error != tc.want {
				t.Errorf("exit %d, error %q, want 1 and %q", code, r.Error, tc.want)
			}
			if errOut.String() != tc.prompt {
				t.Errorf("stderr = %q, want %q", errOut, tc.prompt)
			}
			if tc.clientID == "none" {
				if codes, polls := d.requests(); codes+polls != 0 {
					t.Errorf("fake GitHub got %d requests, want none", codes+polls)
				}
			}
			wantNothingStored(t, a, gh)
			wantNoDeviceSecrets(t, out.String(), errOut.String())
		})
	}
}

func TestConnectGitHubWithTheDeviceFlowChecksTheVaultFirst(t *testing.T) {
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	gh.set(testToken, octocat)
	d := newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody})
	storeTestSecret(t, a, "bot", "GH_TOKEN", "github.com", "ghp_deviceBot"+strings.Repeat("D", 26))
	a.Stdin = unread{t}
	code, r := connectDevice(t, t.Context(), a, out)
	if code != 1 || !strings.Contains(r.Error, "hib secret rm bot") {
		t.Errorf("exit %d, error %q, want 1, naming secret bot and how to remove it", code, r.Error)
	}
	if codes, polls := d.requests(); codes+polls != 0 {
		t.Errorf("fake GitHub got %d requests, want none", codes+polls)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut)
	}
	if secrets := loadVault(t, a); len(secrets) != 1 || secrets[0].Name != "bot" {
		t.Errorf("vault = %+v, want only bot", secrets)
	}
	if gh.callCount() != 0 {
		t.Errorf("GitHub's API got %d calls, want none", gh.callCount())
	}
}

func TestConnectGitHubWithTheDeviceFlowRefusedToken(t *testing.T) {
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody})
	openStdin(t, a)
	code, r := connectDevice(t, t.Context(), a, out)
	if want := "GitHub refused the token. Nothing was stored. Run hib connect github again"; code != 1 || r.Error != want {
		t.Errorf("exit %d, error %q, want 1 and %q", code, r.Error, want)
	}
	if gh.callCount() != 1 {
		t.Errorf("GitHub's API got %d calls, want 1", gh.callCount())
	}
	if _, err := os.Stat(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json")); !os.IsNotExist(err) {
		t.Errorf("vault.json: %v, want none", err)
	}
	if _, err := os.Stat(profilePath(a)); !os.IsNotExist(err) {
		t.Errorf("github.json: %v, want none", err)
	}
	wantNoDeviceSecrets(t, out.String(), errOut.String())
}

func TestConnectGitHubBadSource(t *testing.T) {
	a, _, errOut, gh := newGitHubApp(t, fake.New())
	a.Stdin = unread{t}
	if code := a.Run(t.Context(), []string{"connect", "github", "-source", "pat"}); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if want := "hibd connect github: -source must be gh, token or device, not \"pat\"\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
	if gh.callCount() != 0 {
		t.Error("a bad source called GitHub")
	}
}
