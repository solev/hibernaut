package hibd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/version"
)

const (
	openAIKey  = "sk-proj-realSecretValue1234567890"
	flakyToken = "ghp_flakyTokenDDDDDDDDDDDDDDDDDDDDDDDDDD"
)

// withEnv makes a see the variables in kv.
func withEnv(a *App, kv map[string]string) {
	old := a.Getenv
	a.Getenv = func(k string) string {
		if v, ok := kv[k]; ok {
			return v
		}
		return old(k)
	}
}

// e2bHealth serves E2B's health URLs. Paths starting with /down answer 503.
func e2bHealth(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/down") {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newStatusApp returns an App like newGitHubApp's, with E2B up.
func newStatusApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer, *fakeGitHub) {
	t.Helper()
	a, out, errOut, gh := newGitHubApp(t, fake.New())
	e2b := e2bHealth(t)
	withEnv(a, map[string]string{"E2B_HEALTH_URLS": e2b.URL + "/api " + e2b.URL + "/orchestrator"})
	return a, out, errOut, gh
}

func runStatus(t *testing.T, a *App, out *bytes.Buffer) hibapi.Status {
	t.Helper()
	code, r, err := runJSON(t, a, out, "status", "-json")
	if code != 0 || err != nil || r.Status == nil {
		t.Fatalf("status -json exit %d, %+v, %v; stdout %q", code, r, err, out)
	}
	return *r.Status
}

// runStatusText runs status without -json, and checks it prints s as PrintStatus does.
func runStatusText(t *testing.T, a *App, out *bytes.Buffer, s hibapi.Status) int {
	t.Helper()
	out.Reset()
	code := a.Run(t.Context(), []string{"status"})
	var want bytes.Buffer
	hibapi.PrintStatus(&want, "", s)
	if out.String() != want.String() {
		t.Errorf("status printed\n%s\nwant\n%s", out, &want)
	}
	return code
}

func TestStatus(t *testing.T) {
	a, out, errOut, gh := newStatusApp(t)
	gh.set(testToken, octocat)
	gh.set(flakyToken, account{status: http.StatusInternalServerError})
	if code, r := connectGitHub(t, a, out, testToken); code != 0 {
		t.Fatalf("connect exit %d: %+v", code, r)
	}
	storeTestSecret(t, a, "openai", "OPENAI_API_KEY", "api.openai.com", openAIKey)
	storeTestSecret(t, a, "bot", "BOT_TOKEN", "api.github.com", testToken2)
	storeTestSecret(t, a, "flaky", "FLAKY_TOKEN", "api.github.com", flakyToken)
	calls := gh.callCount()

	s := runStatus(t, a, out)
	if s.Version != version.Version || !s.E2B.OK || s.E2B.Detail != "" {
		t.Errorf("version %q, E2B %+v; want %q and ok", s.Version, s.E2B, version.Version)
	}
	wantGitHub := hibapi.GitHub{Login: "octocat", ID: 583231, Name: "The Octocat", Email: testEmail, Source: "gh",
		Scopes: []string{"repo", "workflow", "read:org"}, ScopesKnown: true}
	if s.GitHub == nil || !equalGitHub(*s.GitHub, wantGitHub) {
		t.Errorf("GitHub = %+v, want %+v", s.GitHub, wantGitHub)
	}
	want := []hibapi.SecretHealth{
		{Name: "bot", Sites: []string{"api.github.com"}, State: hibapi.SecretRevoked, Detail: "revoked: run hib secret add bot again"},
		{Name: "flaky", Sites: []string{"api.github.com"}, State: hibapi.SecretFailed, Detail: "GitHub answered 500 Internal Server Error"},
		{Name: "github", Sites: github.Sites(), State: hibapi.SecretOK},
		{Name: "openai", Sites: []string{"api.openai.com"}, State: hibapi.SecretUnchecked, Detail: "no check for these sites"},
	}
	slices.Sort(want[2].Sites)
	if !slices.EqualFunc(s.Secrets, want, equalSecretHealth) {
		t.Errorf("secrets = %+v,\nwant %+v", s.Secrets, want)
	}
	if got := gh.callCount() - calls; got != 3 {
		t.Errorf("GitHub got %d calls, want 3: one per secret bound to api.github.com", got)
	}

	if code := runStatusText(t, a, out, s); code != 1 {
		t.Errorf("status exit %d, want 1 with a revoked secret", code)
	}
	wantNoToken(t, nil, out.String(), errOut.String())
	for _, value := range []string{openAIKey, flakyToken} {
		if strings.Contains(out.String()+errOut.String(), value) {
			t.Error("a secret value leaked into output")
		}
	}
}

func equalSecretHealth(a, b hibapi.SecretHealth) bool {
	return a.Name == b.Name && slices.Equal(a.Sites, b.Sites) && a.State == b.State && a.Detail == b.Detail
}

func TestStatusAllWell(t *testing.T) {
	a, out, errOut, gh := newStatusApp(t)
	gh.set(testToken, octocat)
	if code, r := connectGitHub(t, a, out, testToken); code != 0 {
		t.Fatalf("connect exit %d: %+v", code, r)
	}
	storeTestSecret(t, a, "openai", "OPENAI_API_KEY", "api.openai.com", openAIKey)
	const wildToken = "ghp_wildTokenEEEEEEEEEEEEEEEEEEEEEEEEEEE"
	gh.set(wildToken, octocat)
	storeTestSecret(t, a, "wild", "WILD_TOKEN", "*.github.com", wildToken)
	s := runStatus(t, a, out)
	if !s.OK() {
		t.Errorf("status = %+v, want OK", s)
	}
	if i := slices.IndexFunc(s.Secrets, func(h hibapi.SecretHealth) bool { return h.Name == "wild" }); i < 0 || s.Secrets[i].State != hibapi.SecretOK {
		t.Errorf("secrets = %+v, want wild, bound to *.github.com and so to api.github.com, checked", s.Secrets)
	}
	if code := runStatusText(t, a, out, s); code != 0 {
		t.Errorf("status exit %d, want 0; stderr %q", code, errOut)
	}
	wantNoToken(t, nil, out.String(), errOut.String())
	if strings.Contains(out.String()+errOut.String(), wildToken) {
		t.Error("a secret value leaked into output")
	}
}

func TestStatusRevokedGitHub(t *testing.T) {
	for _, source := range []string{"gh", "token"} {
		t.Run(source, func(t *testing.T) {
			a, out, errOut, gh := newStatusApp(t)
			gh.set(testToken, octocat)
			if code, r := connectGitHub(t, a, out, testToken, "-source", source); code != 0 {
				t.Fatalf("connect exit %d: %+v", code, r)
			}
			gh.set(testToken, account{status: http.StatusUnauthorized})
			s := runStatus(t, a, out)
			reconnect := hibapi.GitHub{Source: source}.Reconnect()
			if len(s.Secrets) != 1 || s.Secrets[0].State != hibapi.SecretRevoked ||
				s.Secrets[0].Detail != "revoked: run "+reconnect+" again" {
				t.Errorf("secrets = %+v, want github revoked, saying to run %s again", s.Secrets, reconnect)
			}
			if s.GitHub == nil || s.GitHub.Source != source || s.GitHub.ScopesKnown || s.GitHub.Scopes != nil {
				t.Errorf("GitHub = %+v, want the profile without scopes", s.GitHub)
			}
			if code := runStatusText(t, a, out, s); code != 1 {
				t.Errorf("status exit %d, want 1", code)
			}
			if !strings.Contains(out.String(), "revoked: run "+reconnect+" again") {
				t.Errorf("status printed %q", out)
			}
			wantNoToken(t, nil, out.String(), errOut.String())
		})
	}
}

func TestStatusRevokedGitHubWithoutAProfile(t *testing.T) {
	a, out, _, _ := newStatusApp(t)
	storeTestSecret(t, a, "github", "GH_TOKEN", "api.github.com", testToken)
	s := runStatus(t, a, out)
	if len(s.Secrets) != 1 || s.Secrets[0].Detail != "revoked: run hib connect github --from-gh again" {
		t.Errorf("secrets = %+v", s.Secrets)
	}
	if s.GitHub != nil {
		t.Errorf("GitHub = %+v, want nil without github.json", s.GitHub)
	}
}

func TestStatusE2BDown(t *testing.T) {
	a, out, _, _ := newStatusApp(t)
	e2b := e2bHealth(t)
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	withEnv(a, map[string]string{"E2B_HEALTH_URLS": e2b.URL + "/api " + e2b.URL + "/down " + gone.URL + "/health"})
	s := runStatus(t, a, out)
	if s.E2B.OK || !strings.Contains(s.E2B.Detail, "503") || !strings.Contains(s.E2B.Detail, gone.URL) ||
		strings.Contains(s.E2B.Detail, "/api") {
		t.Errorf("E2B = %+v, want down, naming the URLs that failed", s.E2B)
	}
	if code := runStatusText(t, a, out, s); code != 1 {
		t.Errorf("status exit %d, want 1 while E2B is down", code)
	}
}

func TestStatusWithoutSecrets(t *testing.T) {
	a, out, _, gh := newStatusApp(t)
	s := runStatus(t, a, out)
	if s.Secrets == nil || len(s.Secrets) != 0 || s.GitHub != nil || !s.OK() {
		t.Errorf("status = %+v, want no secrets, no GitHub, and OK", s)
	}
	if !strings.Contains(out.String(), `"secrets":[]`) {
		t.Errorf("status -json printed %q, want an empty list of secrets", out)
	}
	if code := runStatusText(t, a, out, s); code != 0 {
		t.Errorf("status exit %d, want 0", code)
	}
	if gh.callCount() != 0 {
		t.Error("status called GitHub without secrets")
	}
}

func TestStatusProfileWithoutTheSecret(t *testing.T) {
	a, out, _, _ := newStatusApp(t)
	if err := github.SaveProfile(profilePath(a), github.Profile{Login: "octocat", ID: 1, Name: "octocat", Source: "gh"}); err != nil {
		t.Fatal(err)
	}
	storeTestSecret(t, a, "openai", "OPENAI_API_KEY", "api.openai.com", openAIKey)
	if s := runStatus(t, a, out); s.GitHub != nil {
		t.Errorf("GitHub = %+v, want nil while the vault has no secret github", s.GitHub)
	}
}

func TestStatusBrokenProfile(t *testing.T) {
	a, out, _, _ := newStatusApp(t)
	if err := os.WriteFile(profilePath(a), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, r := runJSONReply(t, a, out, "status", "-json")
	if code != 1 || !strings.Contains(r.Error, "github.json") {
		t.Errorf("exit %d, %+v; want 1 and an error about github.json", code, r)
	}
}

func TestStatusChecksAtMostEightAtOnce(t *testing.T) {
	a, out, _, gh := newStatusApp(t)
	gh.delay = 50 * time.Millisecond
	for i := range 20 {
		token := fmt.Sprintf("ghp_parallel%028d", i)
		gh.set(token, octocat)
		storeTestSecret(t, a, fmt.Sprintf("bot%d", i), fmt.Sprintf("BOT%d_TOKEN", i), "api.github.com", token)
	}
	s := runStatus(t, a, out)
	for _, sec := range s.Secrets {
		if sec.State != hibapi.SecretOK {
			t.Errorf("%s = %+v, want ok", sec.Name, sec)
		}
	}
	if most := gh.mostAtOnce(); most > 8 || most < 2 {
		t.Errorf("GitHub got %d calls at once, want 2 to 8", most)
	}
}
