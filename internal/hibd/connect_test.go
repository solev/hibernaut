package hibd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/vault"
)

const (
	testToken  = "gho_fakeTokenAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testToken2 = "gho_fakeTokenBBBBBBBBBBBBBBBBBBBBBBBBBB"
	testEmail  = "583231+octocat@users.noreply.github.com"
)

// account is how the fake GitHub answers GET /user for one token.
type account struct {
	status int // 0 means 200
	login  string
	id     int64
	name   string
	scopes *string // nil sends no X-OAuth-Scopes header
}

func scopes(s string) *string { return &s }

var octocat = account{login: "octocat", id: 583231, name: "The Octocat", scopes: scopes("repo, workflow, read:org")}

// fakeGitHub is GitHub's API, over TLS. Tokens it doesn't know get 401.
type fakeGitHub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	accounts map[string]account
	calls    int
	problems []string
	// delay is how long each answer takes.
	delay          time.Duration
	inFlight, most int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{accounts: map[string]account{}}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.serve))
	// Status checks at once, so a test can end while a spare connection's handshake runs.
	g.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	g.srv.StartTLS()
	t.Cleanup(func() {
		g.srv.Close()
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, p := range g.problems {
			t.Error(p)
		}
	})
	return g
}

func (g *fakeGitHub) set(token string, acct account) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accounts[token] = acct
}

func (g *fakeGitHub) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// mostAtOnce returns the most calls the fake answered at once.
func (g *fakeGitHub) mostAtOnce() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.most
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.calls++
	g.inFlight++
	g.most = max(g.most, g.inFlight)
	delay := g.delay
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()
	h := r.Header
	if r.Method != http.MethodGet || r.URL.Path != "/user" || h.Get("Accept") != "application/vnd.github+json" ||
		h.Get("X-GitHub-Api-Version") != "2022-11-28" || !strings.HasPrefix(h.Get("User-Agent"), "hibernaut-hibd/") {
		g.problems = append(g.problems, "fake GitHub got "+r.Method+" "+r.URL.Path+" without the right headers")
	}
	token, _ := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	acct, ok := g.accounts[token]
	g.mu.Unlock()
	time.Sleep(delay)
	if !ok {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	if acct.status != 0 {
		http.Error(w, `{"message":"oops"}`, acct.status)
		return
	}
	if acct.scopes != nil {
		w.Header().Set("X-OAuth-Scopes", *acct.scopes)
	}
	json.NewEncoder(w).Encode(map[string]any{"login": acct.login, "id": acct.id, "name": acct.name})
}

// newGitHubApp returns an App running as root, with a vault, and GitHub's API faked.
func newGitHubApp(t *testing.T, e engine.Engine) (*App, *bytes.Buffer, *bytes.Buffer, *fakeGitHub) {
	t.Helper()
	gh := newFakeGitHub(t)
	a, out, errOut := newApp(t, e)
	withSecretConfig(t, a)
	a.EUID = func() int { return 0 }
	a.GitHubAPI = gh.srv.URL
	a.GitHubClient = gh.srv.Client()
	return a, out, errOut, gh
}

// runningApp returns an engine running machine app, plus one hibd didn't make, and one paused.
func runningApp(guest *secretGuest) *fake.Engine {
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now(),
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev", "hib.idle": "0s"}})
	e.Add(engine.Machine{ID: "raw1", State: engine.Running, StartedAt: time.Now()})
	e.Add(engine.Machine{ID: "old1", State: engine.Paused, StartedAt: time.Now(),
		Metadata: map[string]string{"hib.name": "old", "hib.image": "dev", "hib.idle": "0s"}})
	return e
}

func profilePath(a *App) string {
	return filepath.Join(a.Getenv("HIBD_STATE_DIR"), github.ProfileFile)
}

func loadVault(t *testing.T, a *App) []vault.Secret {
	t.Helper()
	f, err := vault.Load(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	return f.Secrets()
}

// wantNoToken fails the test if any of what a command printed, or ran in a machine, holds a
// real token.
func wantNoToken(t *testing.T, guest *secretGuest, outputs ...string) {
	t.Helper()
	for _, token := range []string{testToken, testToken2} {
		for _, s := range outputs {
			if strings.Contains(s, token) {
				t.Errorf("a token leaked into output:\n%s", strings.ReplaceAll(s, token, "<TOKEN>"))
			}
		}
		if guest == nil {
			continue
		}
		if strings.Contains(guest.envString(), token) {
			t.Error("a token leaked into /etc/environment")
		}
		for _, req := range guest.allRequests() {
			if strings.Contains(strings.Join(req.Args, " "), token) {
				t.Errorf("a token leaked into argv of %s", req.Cmd)
			}
		}
	}
}

// connectGitHub runs connect github -json with token on stdin. A reply that's an error comes
// back in Response.Error.
func connectGitHub(t *testing.T, a *App, out *bytes.Buffer, token string, args ...string) (int, hibapi.Response) {
	t.Helper()
	a.Stdin = strings.NewReader(token + "\n")
	return runJSONReply(t, a, out, append([]string{"connect", "github", "-json"}, args...)...)
}

// runJSONReply is runJSON, with a reply that's an error in Response.Error.
func runJSONReply(t *testing.T, a *App, out *bytes.Buffer, args ...string) (int, hibapi.Response) {
	t.Helper()
	code, r, err := runJSON(t, a, out, args...)
	if remote, ok := errors.AsType[*hibapi.RemoteError](err); ok {
		return code, hibapi.Response{Error: remote.Msg}
	}
	if err != nil {
		t.Fatalf("hibd %q printed %q: %v", args, out, err)
	}
	return code, r
}

func TestConnectGitHub(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e := runningApp(guest)
	a, out, errOut, gh := newGitHubApp(t, e)
	gh.set(testToken, octocat)

	code, r := connectGitHub(t, a, out, testToken)
	if code != 0 {
		t.Fatalf("connect github exit %d: %+v; stderr %s", code, r, errOut)
	}
	want := hibapi.GitHub{Login: "octocat", ID: 583231, Name: "The Octocat", Email: testEmail, Source: "gh",
		Scopes: []string{"repo", "workflow", "read:org"}, ScopesKnown: true}
	if r.GitHub == nil || !equalGitHub(*r.GitHub, want) {
		t.Errorf("reply = %+v, want %+v", r.GitHub, want)
	}

	secrets := loadVault(t, a)
	if len(secrets) != 1 || secrets[0].Name != "github" || secrets[0].Env != "GH_TOKEN" ||
		!slices.Equal(secrets[0].Sites, []string{"*.githubcopilot.com", "api.github.com", "github.com", "uploads.github.com"}) {
		t.Fatalf("vault = %+v, want secret github, GH_TOKEN, bound to GitHub's sites and Copilot's", secrets)
	}

	fi, err := os.Stat(profilePath(a))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("github.json: %v, %v; want mode 0644", fi, err)
	}
	data, err := os.ReadFile(profilePath(a))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(testToken)) {
		t.Error("github.json holds the token")
	}
	p, err := github.LoadProfile(profilePath(a))
	if err != nil || p == nil || *p != (github.Profile{Login: "octocat", ID: 583231, Name: "The Octocat", Source: "gh"}) {
		t.Errorf("profile = %+v, %v", p, err)
	}

	if want := "PATH=/usr/bin\n# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n" +
		"GH_TOKEN=" + secrets[0].Placeholder + "\n# END hibernaut secrets\n"; guest.envString() != want {
		t.Errorf("/etc/environment = %q, want %q", guest.envString(), want)
	}
	if id := guest.identity(); id == nil || *id != (machines.GitIdentity{Name: "The Octocat", Email: testEmail}) {
		t.Errorf("git identity = %+v", id)
	}
	if n := len(guest.allRequests()); n != 3 {
		t.Errorf("exec requests = %d, want 3 (read, write, git) for the one running named machine", n)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut)
	}
	wantNoToken(t, guest, out.String(), errOut.String())
}

func equalGitHub(a, b hibapi.GitHub) bool {
	return a.Login == b.Login && a.ID == b.ID && a.Name == b.Name && a.Email == b.Email && a.Source == b.Source &&
		slices.Equal(a.Scopes, b.Scopes) && a.ScopesKnown == b.ScopesKnown
}

func TestConnectGitHubSaysWhoAndWarnsAboutScopes(t *testing.T) {
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	acct := octocat
	acct.scopes = scopes("repo")
	gh.set(testToken, acct)
	a.Stdin = strings.NewReader(testToken)
	if code := a.Run(t.Context(), []string{"connect", "github", "-source", "token"}); code != 0 {
		t.Fatalf("exit %d; stderr %s", code, errOut)
	}
	if out.String() != "connected GitHub as octocat\n" {
		t.Errorf("stdout = %q", out)
	}
	g := hibapi.GitHub{Source: "token", Scopes: []string{"repo"}, ScopesKnown: true}
	if want := "warning: " + g.ScopeWarning() + "\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
	p, err := github.LoadProfile(profilePath(a))
	if err != nil || p == nil || p.Source != "token" {
		t.Errorf("profile = %+v, %v; want source token", p, err)
	}
	wantNoToken(t, nil, out.String(), errOut.String())
}

func TestConnectGitHubWithoutScopesDoesntWarn(t *testing.T) {
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	acct := octocat
	acct.scopes = nil
	acct.name = ""
	gh.set(testToken, acct)
	code, r := connectGitHub(t, a, out, testToken, "-source", "token")
	if code != 0 || r.GitHub == nil || r.GitHub.ScopesKnown || r.GitHub.Scopes != nil || r.GitHub.Name != "octocat" {
		t.Fatalf("exit %d, reply %+v; want unknown scopes and the login as the name", code, r.GitHub)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestReconnectGitHubKeepsThePlaceholder(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	a, out, errOut, gh := newGitHubApp(t, runningApp(guest))
	gh.set(testToken, octocat)
	if code, r := connectGitHub(t, a, out, testToken); code != 0 {
		t.Fatalf("first connect exit %d: %+v", code, r)
	}
	first := loadVault(t, a)[0].Placeholder

	renamed := octocat
	renamed.name = "Mona Lisa Octocat"
	gh.set(testToken2, renamed)
	if code, r := connectGitHub(t, a, out, testToken2); code != 0 {
		t.Fatalf("second connect exit %d: %+v", code, r)
	}
	secrets := loadVault(t, a)
	if len(secrets) != 1 || secrets[0].Placeholder != first {
		t.Errorf("vault = %+v, want one secret with placeholder %q", secrets, first)
	}
	if id := guest.identity(); id == nil || id.Name != "Mona Lisa Octocat" {
		t.Errorf("git identity = %+v, want the new name", id)
	}
	revealed := openVault(t, a)
	if len(revealed) != 1 || revealed[0].Value != testToken2 {
		t.Error("the vault doesn't hold the new token")
	}
	wantNoToken(t, guest, out.String(), errOut.String())
}

func openVault(t *testing.T, a *App) []vault.Revealed {
	t.Helper()
	f, err := vault.Load(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadKey(a.Getenv("HIBD_VAULT_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := f.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	return revealed
}

func TestConnectGitHubRefusedStoresNothing(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"gh", "run gh auth login -h github.com, then hib connect github --from-gh"},
		{"token", "hib connect github --token"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
			a, out, errOut, gh := newGitHubApp(t, runningApp(guest))
			code, r := connectGitHub(t, a, out, testToken, "-source", tc.source)
			if code != 1 || !strings.Contains(r.Error, "GitHub refused the token") || !strings.Contains(r.Error, tc.want) {
				t.Errorf("exit %d, error %q; want 1 and %q", code, r.Error, tc.want)
			}
			if gh.callCount() != 1 {
				t.Errorf("GitHub got %d calls, want 1", gh.callCount())
			}
			if _, err := os.Stat(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json")); !os.IsNotExist(err) {
				t.Errorf("vault.json: %v, want none", err)
			}
			if _, err := os.Stat(profilePath(a)); !os.IsNotExist(err) {
				t.Errorf("github.json: %v, want none", err)
			}
			if n := len(guest.allRequests()); n != 0 {
				t.Errorf("machines got %d exec requests, want none", n)
			}
			wantNoToken(t, guest, out.String(), errOut.String())
		})
	}
}

func TestConnectGitHubRejectsABadTokenWithoutQuotingIt(t *testing.T) {
	for _, bad := range []string{"gho_has space", "gho_" + strings.Repeat("x", 252), "gho_semi;colon", "ghp_über"} {
		a, out, errOut, gh := newGitHubApp(t, fake.New())
		code, r := connectGitHub(t, a, out, bad)
		if code != 1 || r.Error == "" {
			t.Errorf("exit %d, %+v; want 1 and an error", code, r)
		}
		for _, s := range []string{out.String(), errOut.String()} {
			if strings.Contains(s, bad) {
				t.Errorf("output quotes the bad token: %q", s)
			}
		}
		if gh.callCount() != 0 {
			t.Error("a bad token went to GitHub")
		}
	}
}

// unread fails the test if anything reads it.
type unread struct{ t *testing.T }

func (u unread) Read([]byte) (int, error) {
	u.t.Error("stdin was read")
	return 0, io.EOF
}

func TestConnectAndStatusNeedRoot(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"connect", "github"}, "run it as root: sudo hibd connect github"},
		{[]string{"connect", "github", "-json"}, "run it as root: sudo hibd connect github"},
		{[]string{"status"}, "run it as root: sudo hibd status"},
		{[]string{"status", "-json"}, "run it as root: sudo hibd status"},
	} {
		a, out, errOut, gh := newGitHubApp(t, fake.New())
		a.EUID = func() int { return 1000 }
		a.Stdin = unread{t}
		code := a.Run(t.Context(), tc.args)
		if code != 1 || !strings.Contains(out.String()+errOut.String(), tc.want) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want 1 and %q", tc.args, code, out, errOut, tc.want)
		}
		if gh.callCount() != 0 {
			t.Errorf("%q called GitHub", tc.args)
		}
	}
}

func TestConnectUsage(t *testing.T) {
	for _, args := range [][]string{{"connect"}, {"connect", "gitlab"}, {"connect", "github", "-source", "pat"}, {"connect", "github", "extra"}} {
		a, _, errOut, _ := newGitHubApp(t, fake.New())
		a.Stdin = unread{t}
		if code := a.Run(t.Context(), args); code != 2 {
			t.Errorf("%q: exit %d, want 2; stderr %q", args, code, errOut)
		}
	}
}

func TestConnectGitHubWhileAnotherSecretSetsGHToken(t *testing.T) {
	a, out, _, gh := newGitHubApp(t, fake.New())
	gh.set(testToken, octocat)
	storeTestSecret(t, a, "bot", "GH_TOKEN", "github.com", "ghp_botTokenCCCCCCCCCCCCCCCCCCCCCCCCCCCC")
	code, r := connectGitHub(t, a, out, testToken)
	if code != 1 || !strings.Contains(r.Error, "bot") || !strings.Contains(r.Error, "hib secret rm bot") {
		t.Errorf("exit %d, error %q; want 1, naming secret bot and how to remove it", code, r.Error)
	}
	if secrets := loadVault(t, a); len(secrets) != 1 || secrets[0].Name != "bot" {
		t.Errorf("vault = %+v, want only bot", secrets)
	}
	if _, err := os.Stat(profilePath(a)); !os.IsNotExist(err) {
		t.Errorf("github.json: %v, want none", err)
	}
}

// The token and the account are saved together, under the vault's lock, so two connects can't
// leave one's token with the other's account.
func TestConnectGitHubStoresNothingIfTheAccountCantBeSaved(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	a, out, errOut, gh := newGitHubApp(t, runningApp(guest))
	gh.set(testToken, octocat)
	if err := os.Mkdir(profilePath(a), 0o755); err != nil {
		t.Fatal(err)
	}
	code, r := connectGitHub(t, a, out, testToken)
	if code != 1 || !strings.Contains(r.Error, "Nothing was stored") {
		t.Errorf("exit %d, error %q; want 1 and Nothing was stored", code, r.Error)
	}
	if _, err := os.Stat(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json")); !os.IsNotExist(err) {
		t.Errorf("vault.json: %v, want none", err)
	}
	if n := len(guest.allRequests()); n != 0 {
		t.Errorf("machines got %d exec requests, want none", n)
	}
	wantNoToken(t, guest, out.String(), errOut.String())
}

func TestConnectGitHubIntoAMachineWithoutGit(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n"), noGit: t.TempDir()}
	a, out, errOut, gh := newGitHubApp(t, runningApp(guest))
	gh.set(testToken, octocat)
	if code, r := connectGitHub(t, a, out, testToken); code != 0 {
		t.Fatalf("exit %d: %+v", code, r)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want no warning for a machine without git", errOut)
	}
	if !strings.Contains(guest.envString(), "GH_TOKEN=") {
		t.Errorf("/etc/environment = %q, want the block", guest.envString())
	}
}

func TestConnectGitHubWarnsWhenAMachineCantBeUpdated(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n"), gitExit: 255}
	a, out, errOut, gh := newGitHubApp(t, runningApp(guest))
	gh.set(testToken, octocat)
	if code, r := connectGitHub(t, a, out, testToken); code != 0 || r.GitHub == nil {
		t.Fatalf("exit %d: %+v; a machine that can't be updated is a warning", code, r)
	}
	if !strings.Contains(errOut.String(), "warning: GitHub connected, but updating app failed") {
		t.Errorf("stderr = %q", errOut)
	}
	wantNoToken(t, guest, out.String(), errOut.String())
}

func TestConnectGitHubLeavesMachinesAloneWhileQuiescing(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	a, out, _, gh := newGitHubApp(t, runningApp(guest))
	gh.set(testToken, octocat)
	if err := os.WriteFile(a.Getenv("HIBD_QUIESCING"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, r := connectGitHub(t, a, out, testToken); code != 0 {
		t.Fatalf("exit %d: %+v", code, r)
	}
	if n := len(guest.allRequests()); n != 0 {
		t.Errorf("machines got %d exec requests while the host goes to sleep", n)
	}
}

// storeOldGitHub stores secret github as a connection made before M5 left it: bound to git, the
// API and uploads, but not Copilot.
func storeOldGitHub(t *testing.T, a *App, token string) vault.Secret {
	t.Helper()
	var saved vault.Secret
	err := vault.Update(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json"), a.Getenv("HIBD_VAULT_KEY"), func(f *vault.File, key vault.Key) error {
		var err error
		saved, err = f.Put(key, github.SecretName, github.Env, []string{"github.com", "api.github.com", "uploads.github.com"}, token)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestDaemonBindsAnOldGitHubConnectionToCopilot(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	withSecretConfig(t, a)
	old := storeOldGitHub(t, a, testToken)
	saveTestProfile(t, a, "The Octocat")
	logs, stop := startDaemon(t, a)
	waitFor(t, logs, "the daemon binds secret github to Copilot", func() bool {
		s := loadVault(t, a)
		return len(s) == 1 && slices.Contains(s[0].Sites, "*.githubcopilot.com")
	})
	stop()
	secrets := loadVault(t, a)
	want := []string{"*.githubcopilot.com", "api.github.com", "github.com", "uploads.github.com"}
	if s := secrets[0]; s.Name != github.SecretName || s.Env != github.Env || s.Placeholder != old.Placeholder || !slices.Equal(s.Sites, want) {
		t.Errorf("vault = %+v, want github with the same variable and placeholder, bound to %q", secrets, want)
	}
	if revealed := openVault(t, a); len(revealed) != 1 || revealed[0].Value != testToken {
		t.Error("the vault doesn't hold the same token")
	}
	if n := strings.Count(logs.String(), "*.githubcopilot.com"); n != 1 {
		t.Errorf("the log mentions Copilot's sites %d times, want once:\n%s", n, logs)
	}

	// Once bound, the daemon leaves the vault alone.
	fi, err := os.Stat(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	logs, stop = startDaemon(t, a)
	waitFor(t, logs, "the daemon starts", func() bool { return strings.Contains(logs.String(), "keeping busy machines running") })
	stop()
	again, err := os.Stat(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(fi.ModTime()) || strings.Contains(logs.String(), "githubcopilot") {
		t.Errorf("the daemon rewrote a vault that was already right; log:\n%s", logs)
	}
	wantNoToken(t, nil, logs.String())
}

func TestDaemonLeavesAGitHubSecretWithoutAConnectionAlone(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	withSecretConfig(t, a)
	storeOldGitHub(t, a, testToken)
	logs, stop := startDaemon(t, a)
	waitFor(t, logs, "the daemon starts", func() bool { return strings.Contains(logs.String(), "keeping busy machines running") })
	stop()
	if s := loadVault(t, a); slices.Contains(s[0].Sites, "*.githubcopilot.com") {
		t.Errorf("vault = %+v: hib secret add github isn't a GitHub connection", s)
	}
}
