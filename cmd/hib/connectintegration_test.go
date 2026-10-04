package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/hibd"
	"github.com/solev/hibernaut/internal/vault"
)

const integrationDeviceCode = "cliDeviceCode0123456789abcdef"

type deviceWait struct {
	ctx     context.Context
	proceed chan struct{}
}

type deviceGitHub struct {
	t     *testing.T
	host  *host
	flow  *github.DeviceFlow
	waits chan deviceWait

	mu                  sync.Mutex
	now                 time.Time
	approved            bool
	codes, polls, users int
	pollsAfterReturn    int
	stdinFD             int
	cancelledWithEOF    bool
}

func newDeviceIntegration(t *testing.T) (*client, *deviceGitHub) {
	t.Helper()
	h := newHost(t)
	c := initClient(t, h, true)
	state := h.env["HIBD_STATE_DIR"]
	h.env["HIBD_VAULT_KEY"] = filepath.Join(state, "vault.key")
	h.env["HIBD_AUDIT_LOG"] = filepath.Join(state, "audit.jsonl")
	g := &deviceGitHub{
		t: t, host: h, waits: make(chan deviceWait), stdinFD: -1,
		now: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
	}
	server := httptest.NewTLSServer(http.HandlerFunc(g.serve))
	t.Cleanup(func() {
		server.Close()
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.pollsAfterReturn != 0 {
			t.Error("GitHub received a poll after in-process hibd returned")
		}
	})
	g.flow = &github.DeviceFlow{
		Client: server.Client(), Base: server.URL, ClientID: "Ov23liCliTest",
		Now: g.clock, Sleep: g.sleep,
	}
	h.sudoApp = func(a *hibd.App) {
		if a.Stdin != h.lastStdin {
			t.Error("in-process hibd didn't receive SSH's unchanged stdin")
		}
		if _, ok := a.Stdin.(*os.File); !ok {
			t.Error("in-process hibd didn't receive the held pipe's read end")
		}
		a.GitHubAPI, a.GitHubClient = server.URL, server.Client()
		a.GitHubDeviceFlow = g.flow
	}
	h.sshInput = func(_ context.Context, _ []string, stdin io.Reader) {
		g.stdinFD = duplicateSSHStdin(t, stdin)
		assertSSHStdinOpen(t, g.stdinFD)
	}
	t.Cleanup(func() {
		if h.lastHibd() != nil {
			h.waitHibd()
		}
	})
	return c, g
}

func (g *deviceGitHub) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/login/device/code":
		if r.Method != http.MethodPost || r.ParseForm() != nil ||
			r.Form.Get("client_id") != "Ov23liCliTest" || r.Form.Get("scope") != "repo read:org workflow" {
			g.t.Error("the device code request didn't have the expected method and form")
		}
		g.mu.Lock()
		g.codes++
		g.mu.Unlock()
		io.WriteString(w, `{"device_code":"`+integrationDeviceCode+`","user_code":"WDJB-MJHT",`+
			`"verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
	case "/login/oauth/access_token":
		if r.Method != http.MethodPost || r.ParseForm() != nil ||
			r.Form.Get("client_id") != "Ov23liCliTest" || r.Form.Get("device_code") != integrationDeviceCode ||
			r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
			g.t.Error("the token poll didn't have the expected method and form")
		}
		returned := g.host.hibdReturned()
		g.mu.Lock()
		g.polls++
		if returned {
			g.pollsAfterReturn++
		}
		approved := g.approved
		g.mu.Unlock()
		if approved {
			io.WriteString(w, `{"access_token":"`+testGitHubToken+`","token_type":"bearer","scope":"repo,read:org,workflow"}`)
		} else {
			io.WriteString(w, `{"error":"authorization_pending"}`)
		}
	case "/user":
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testGitHubToken {
			g.t.Error("the account lookup didn't use the approved token")
		}
		g.mu.Lock()
		g.users++
		g.mu.Unlock()
		w.Header().Set("X-OAuth-Scopes", "repo, read:org, workflow")
		io.WriteString(w, `{"login":"octocat","id":583231,"name":"The Octocat"}`)
	default:
		g.t.Error("the fake GitHub received an unexpected endpoint")
		w.WriteHeader(http.StatusNotFound)
	}
}

func (g *deviceGitHub) clock() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.now
}

func (g *deviceGitHub) sleep(ctx context.Context, d time.Duration) error {
	wait := deviceWait{ctx: ctx, proceed: make(chan struct{})}
	select {
	case g.waits <- wait:
	case <-ctx.Done():
		return g.cancelled(ctx)
	}
	select {
	case <-wait.proceed:
	case <-ctx.Done():
		return g.cancelled(ctx)
	}
	if err := ctx.Err(); err != nil {
		return g.cancelled(ctx)
	}
	g.mu.Lock()
	g.now = g.now.Add(d)
	g.mu.Unlock()
	return nil
}

func (g *deviceGitHub) cancelled(ctx context.Context) error {
	n, err := syscall.Read(g.stdinFD, make([]byte, 1))
	g.mu.Lock()
	g.cancelledWithEOF = n == 0 && err == nil
	g.mu.Unlock()
	return ctx.Err()
}

func (g *deviceGitHub) nextWait() deviceWait {
	g.t.Helper()
	select {
	case wait := <-g.waits:
		return wait
	case <-time.After(10 * time.Second):
		g.t.Fatal("hibd didn't reach the controlled device wait")
		return deviceWait{}
	}
}

func (g *deviceGitHub) approve() {
	g.mu.Lock()
	g.approved = true
	g.mu.Unlock()
}

func (g *deviceGitHub) requests() (codes, polls, users int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.codes, g.polls, g.users
}

func (g *deviceGitHub) assertPrivate(c *client) {
	g.t.Helper()
	assertSSHStdinEOF(g.t, g.stdinFD)
	assertTokenPrivate(g.t, c, g.host.takeCalls(), testGitHubToken)
	run := g.host.lastHibd()
	output := c.out.String() + c.errOut.String() + run.stdout.String() + run.stderr.String()
	if strings.Contains(output, testGitHubToken) {
		g.t.Error("the approved token reached hib or hibd output")
	}
	if strings.Contains(output, integrationDeviceCode) {
		g.t.Error("the private device code reached hib or hibd output")
	}
}

func (g *deviceGitHub) assertStopped(wait deviceWait) {
	g.t.Helper()
	if code := g.host.waitHibd(); code != 1 {
		g.t.Errorf("cancelled hibd exited %d, want 1", code)
	}
	select {
	case <-wait.ctx.Done():
	default:
		g.t.Error("hibd's device wait wasn't cancelled")
	}
	close(wait.proceed)
	g.mu.Lock()
	eof, latePolls := g.cancelledWithEOF, g.pollsAfterReturn
	g.mu.Unlock()
	if !eof {
		g.t.Error("hibd stopped before SSH's stdin reached EOF")
	}
	if latePolls != 0 {
		g.t.Error("GitHub received another poll after hibd returned")
	}
	if codes, polls, users := g.requests(); codes != 1 || polls != 1 || users != 0 {
		g.t.Errorf("cancelled GitHub requests = %d codes, %d polls, %d users, want 1, 1, 0", codes, polls, users)
	}
	g.assertNothingStored()
}

func (g *deviceGitHub) assertNothingStored() {
	g.t.Helper()
	for _, name := range []string{"vault.json", "vault.key", github.ProfileFile} {
		if _, err := os.Stat(filepath.Join(g.host.env["HIBD_STATE_DIR"], name)); !errors.Is(err, os.ErrNotExist) {
			g.t.Errorf("%s exists or can't be checked after an unapproved login", name)
		}
	}
}

func TestConnectGitHubDeviceIntegrationSuccess(t *testing.T) {
	c, g := newDeviceIntegration(t)
	opened := make(chan string, 2)
	c.browser = func(_ context.Context, url string) error {
		opened <- url
		return nil
	}
	done := make(chan int, 1)
	go func() { done <- c.hib("connect", "github") }()
	first := g.nextWait()
	if url := <-opened; url != "https://github.com/login/device" {
		t.Error("the browser didn't open the fixed device URL")
	}
	prompt := "To connect GitHub, open https://github.com/login/device and enter the code WDJB-MJHT within 15 minutes.\n" +
		"Waiting for GitHub. Press Ctrl-C to cancel.\n"
	if c.errOut.String() != prompt {
		t.Error("the device prompt wasn't streamed while hibd was waiting")
	}
	close(first.proceed)
	second := g.nextWait()
	g.approve()
	close(second.proceed)
	if code := <-done; code != 0 {
		t.Fatalf("device integration exited %d", code)
	}
	select {
	case <-opened:
		t.Error("the device integration opened the browser more than once")
	default:
	}
	want := "connected GitHub as octocat (The Octocat). Machines commit as 583231+octocat@users.noreply.github.com\n"
	if c.out.String() != want {
		t.Error("the connected account wasn't printed as expected")
	}
	if codes, polls, users := g.requests(); codes != 1 || polls != 2 || users != 1 {
		t.Errorf("GitHub requests = %d codes, %d polls, %d users, want 1, 2, 1", codes, polls, users)
	}
	f, err := vault.Load(filepath.Join(g.host.env["HIBD_STATE_DIR"], "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadKey(g.host.env["HIBD_VAULT_KEY"])
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := f.Open(key)
	if err != nil || len(revealed) != 1 || revealed[0].Name != "github" ||
		revealed[0].Env != "GH_TOKEN" || revealed[0].Value != testGitHubToken {
		t.Error("the vault didn't hold the token approved through the device flow")
	}
	p, err := github.LoadProfile(filepath.Join(g.host.env["HIBD_STATE_DIR"], github.ProfileFile))
	if err != nil || p == nil || p.Login != "octocat" || p.Source != hibapi.GitHubFromDevice {
		t.Error("the connected GitHub profile wasn't saved with source device")
	}
	g.assertPrivate(c)
}

func TestConnectGitHubDeviceIntegrationCancellation(t *testing.T) {
	c, g := newDeviceIntegration(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sshError := make(chan error, 1)
	sshMayReturn := make(chan struct{})
	releaseSSH := sync.OnceFunc(func() { close(sshMayReturn) })
	defer releaseSSH()
	ssh := c.ssh
	c.ssh = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		code, err := ssh(ctx, args, stdin, stdout, stderr)
		sshError <- err
		<-sshMayReturn
		return code, err
	}
	done := make(chan int, 1)
	go func() { done <- c.hibContext(ctx, "connect", "github") }()
	first := g.nextWait()
	close(first.proceed)
	pending := g.nextWait()
	cancel()
	if err := <-sshError; !errors.Is(err, context.Canceled) {
		t.Error("the fake SSH didn't return its context's cancellation error")
	}
	if pending.ctx.Err() != nil || g.host.hibdReturned() {
		t.Error("hib's cancellation reached hibd before the held pipe closed")
	}
	assertSSHStdinOpen(t, g.stdinFD)
	releaseSSH()
	if code := <-done; code != 1 {
		t.Errorf("cancelled hib exited %d, want 1", code)
	}
	want := "hib: the GitHub login was cancelled. Run hib status to see whether GitHub is connected\n"
	if c.out.Len() != 0 || !strings.HasSuffix(c.errOut.String(), want) ||
		strings.Contains(strings.ToLower(c.errOut.String()), "nothing was stored") {
		t.Error("hib didn't report cancellation without claiming nothing was stored")
	}
	g.assertStopped(pending)
	g.assertPrivate(c)
}

func TestConnectGitHubDeviceIntegrationPipeEOF(t *testing.T) {
	c, g := newDeviceIntegration(t)
	disconnected := make(chan struct{})
	g.host.sudoDisconnect = disconnected
	done := make(chan int, 1)
	go func() { done <- c.hib("connect", "github") }()
	first := g.nextWait()
	close(first.proceed)
	pending := g.nextWait()
	close(disconnected)
	if code := <-done; code != 1 {
		t.Errorf("disconnected hib exited %d, want 1", code)
	}
	if c.out.Len() != 0 || !strings.Contains(c.errOut.String(), "couldn't reach "+testHost) ||
		strings.Contains(c.errOut.String(), "the GitHub login was cancelled") {
		t.Error("hib didn't report the independent SSH disconnect")
	}
	g.assertStopped(pending)
	g.assertPrivate(c)
}

func TestConnectGitHubDeviceIntegrationNoClientID(t *testing.T) {
	c, g := newDeviceIntegration(t)
	g.flow.ClientID = ""
	c.browser = func(context.Context, string) error {
		t.Error("a login without a client ID opened the browser")
		return nil
	}
	if code := c.hib("connect", "github"); code != 1 {
		t.Errorf("hib without a client ID exited %d, want 1", code)
	}
	want := "hib: this hibd has no GitHub OAuth App to log in to. Run hib connect github --from-gh or --token\n"
	if c.out.Len() != 0 || c.errOut.String() != want {
		t.Error("hibd's missing-client-ID explanation didn't reach hib's user")
	}
	if code := g.host.waitHibd(); code != 1 {
		t.Errorf("hibd without a client ID exited %d, want 1", code)
	}
	if codes, polls, users := g.requests(); codes != 0 || polls != 0 || users != 0 {
		t.Errorf("missing-client-ID requests = %d codes, %d polls, %d users, want none", codes, polls, users)
	}
	g.assertNothingStored()
	g.assertPrivate(c)
}
