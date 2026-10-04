package github_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/version"
)

const testToken = "gho_testTokenValue0123456789abcdefABCDEF"

func TestSitesIsAFreshCopy(t *testing.T) {
	want := []string{"github.com", "api.github.com", "uploads.github.com", "*.githubcopilot.com"}
	sites := github.Sites()
	if !slices.Equal(sites, want) {
		t.Fatalf("Sites() = %q, want %q", sites, want)
	}
	sites[0] = "evil.example"
	if got := github.Sites(); !slices.Equal(got, want) {
		t.Fatalf("changing what Sites returned changed the next call: %q", got)
	}
}

func TestCheckToken(t *testing.T) {
	for _, ok := range []string{"a", "gho_" + strings.Repeat("A", 36), "github_pat_11ABC_def", strings.Repeat("x", 255)} {
		if err := github.CheckToken(ok); err != nil {
			t.Errorf("CheckToken(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 256), "gho_abc def", "gho_abc\n", "gho-abc", "ghö_abc", "gho_abc\x1b[2J", "Bearer gho_abc"} {
		err := github.CheckToken(bad)
		if err == nil {
			t.Errorf("CheckToken(%q) = nil, want an error", bad)
			continue
		}
		if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("CheckToken's error quotes the token: %v", err)
		}
	}
	err := github.CheckToken("gho_secretPart$")
	if err == nil || strings.Contains(err.Error(), "secretPart") {
		t.Errorf("CheckToken error = %v, want one that doesn't quote the token", err)
	}
}

func TestCheckLogin(t *testing.T) {
	for _, ok := range []string{"a", "octocat", "Octo-Cat", "a1-b2-c3", strings.Repeat("a", 39)} {
		if err := github.CheckLogin(ok); err != nil {
			t.Errorf("CheckLogin(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "-octocat", "octocat-", "octo--cat", "octo_cat", "octo cat", strings.Repeat("a", 40), "octocat\n", "../x"} {
		if err := github.CheckLogin(bad); err == nil {
			t.Errorf("CheckLogin(%q) = nil, want an error", bad)
		}
	}
}

func TestNoreply(t *testing.T) {
	if got, want := github.Noreply(583231, "octocat"), "583231+octocat@users.noreply.github.com"; got != want {
		t.Errorf("Noreply = %q, want %q", got, want)
	}
}

func TestCleanName(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "The Octocat", "The Octocat"},
		{"surrounding spaces", "  The Octocat \t", "The Octocat"},
		{"control characters", "The\x00 Oct\nocat\x1b[2J\x7f\u0085", "The Octocat[2J"},
		{"empty", "", "octocat"},
		{"only spaces and controls", " \t\x01 \n", "octocat"},
		{"starts with a dash", "-n Octocat", "octocat"},
		{"dash after spaces", "  --global", "octocat"},
		{"dash after a control character", "\x00-n", "octocat"},
		{"dash inside", "Mary-Jane", "Mary-Jane"},
		{"invalid UTF-8", "Oct\xffocat", "Octocat"},
		{"unicode", "Ōctø Cát 🐙", "Ōctø Cát 🐙"},
		{"long", long, strings.Repeat("é", 127)},
		{"long ending in a space", strings.Repeat("a", 254) + " b", strings.Repeat("a", 254)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := github.CleanName(tc.in, "octocat")
			if got != tc.want {
				t.Errorf("CleanName(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) > 255 {
				t.Errorf("CleanName(%q) is %d bytes, want at most 255", tc.in, len(got))
			}
		})
	}
}

func TestParseScopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header http.Header
		want   []string
		known  bool
	}{
		{name: "no header", header: http.Header{}},
		{name: "empty", header: http.Header{"X-Oauth-Scopes": {""}}, known: true},
		{name: "gh's", header: http.Header{"X-Oauth-Scopes": {"gist, read:org, repo"}}, want: []string{"gist", "read:org", "repo"}, known: true},
		{name: "messy", header: http.Header{"X-Oauth-Scopes": {" repo ,,workflow,  "}}, want: []string{"repo", "workflow"}, known: true},
		{name: "two lines", header: http.Header{"X-Oauth-Scopes": {"repo", "workflow"}}, want: []string{"repo", "workflow"}, known: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, known := github.ParseScopes(tc.header)
			if !slices.Equal(got, tc.want) || known != tc.known {
				t.Errorf("ParseScopes(%v) = %q, %v, want %q, %v", tc.header, got, known, tc.want, tc.known)
			}
		})
	}
}

// fakeAPI serves GET /user as GitHub does for testToken, and refuses other tokens with 401. It
// sends X-OAuth-Scopes if scopes isn't nil.
func fakeAPI(t *testing.T, body string, scopes *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		for header, want := range map[string]string{
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28",
			"User-Agent":           "hibernaut-hibd/" + version.Version,
		} {
			if got := r.Header.Get(header); got != want {
				http.Error(w, header+" is "+got, http.StatusBadRequest)
				return
			}
		}
		if scopes != nil {
			w.Header().Set("X-OAuth-Scopes", *scopes)
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetUser(t *testing.T) {
	scopes := "repo, workflow"
	srv := fakeAPI(t, `{"login":"octocat","id":583231,"name":" The\nOctocat ","email":null}`, &scopes)
	u, err := github.GetUser(t.Context(), srv.Client(), srv.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	want := github.User{Login: "octocat", ID: 583231, Name: "TheOctocat", Scopes: []string{"repo", "workflow"}, ScopesKnown: true}
	if u.Login != want.Login || u.ID != want.ID || u.Name != want.Name || !slices.Equal(u.Scopes, want.Scopes) || u.ScopesKnown != want.ScopesKnown {
		t.Fatalf("GetUser = %+v, want %+v", u, want)
	}
}

func TestGetUserWithoutScopesOrName(t *testing.T) {
	srv := fakeAPI(t, `{"login":"octocat","id":583231,"name":null}`, nil)
	u, err := github.GetUser(t.Context(), srv.Client(), srv.URL+"/", testToken)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "octocat" || u.ScopesKnown || u.Scopes != nil {
		t.Fatalf("GetUser = %+v, want the login as name and unknown scopes", u)
	}
}

func TestGetUserUnauthorized(t *testing.T) {
	srv := fakeAPI(t, `{}`, nil)
	const revoked = "gho_revokedTokenValue0123456789abcdef"
	_, err := github.GetUser(t.Context(), srv.Client(), srv.URL, revoked)
	if !errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("GetUser error = %v, want ErrUnauthorized", err)
	}
	checkNoToken(t, err, revoked)
}

func TestGetUserFails(t *testing.T) {
	big := `{"login":"octocat","id":1,"name":"` + strings.Repeat("x", 1<<20) + `"}`
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "server error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "oops "+r.Header.Get("Authorization"), http.StatusInternalServerError)
			},
			want: "500",
		},
		{
			name: "forbidden",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			},
			want: "403",
		},
		{
			name: "redirect",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/elsewhere" {
					io.WriteString(w, `{"login":"octocat","id":1}`)
					return
				}
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
			},
			want: "redirect",
		},
		{
			name: "big body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, big)
			},
			want: "1 MiB",
		},
		{
			name: "bad JSON",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"login": `+r.Header.Get("Authorization"))
			},
			want: "unreadable",
		},
		{
			name: "bad login",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"login":"-octocat\u001b[2J","id":1}`)
			},
			want: "login",
		},
		{
			name: "no login",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"id":1}`)
			},
			want: "login",
		},
		{
			name: "bad ID",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"login":"octocat","id":0}`)
			},
			want: "ID",
		},
		{
			name: "negative ID",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"login":"octocat","id":-5}`)
			},
			want: "ID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				tc.handler(w, r)
			}))
			defer srv.Close()
			// The test server's client follows redirects. GetUser mustn't.
			u, err := github.GetUser(t.Context(), srv.Client(), srv.URL, testToken)
			if err == nil {
				t.Fatalf("GetUser = %+v, want an error", u)
			}
			if errors.Is(err, github.ErrUnauthorized) {
				t.Errorf("GetUser error = %v, want one that isn't ErrUnauthorized", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("GetUser error = %q, want it to say %q", err, tc.want)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("GetUser made %d requests, want 1", n)
			}
			checkNoToken(t, err, testToken)
		})
	}
}

func TestGetUserSlowServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := github.GetUser(ctx, srv.Client(), srv.URL, testToken)
	if err == nil || errors.Is(err, github.ErrUnauthorized) {
		t.Fatalf("GetUser error = %v, want a timeout", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("GetUser took %v, want it to give up with ctx", took)
	}
	if !strings.Contains(err.Error(), "didn't answer") {
		t.Errorf("GetUser error = %q, want it to say GitHub didn't answer", err)
	}
	checkNoToken(t, err, testToken)
}

func TestGetUserRejectsABadTokenWithoutSendingIt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	const bad = "gho_bad\r\nX-Evil: 1"
	_, err := github.GetUser(t.Context(), srv.Client(), srv.URL, bad)
	if err == nil || calls.Load() != 0 {
		t.Fatalf("GetUser error = %v after %d requests, want an error and none", err, calls.Load())
	}
	checkNoToken(t, err, "gho_bad")
}

func TestNewClient(t *testing.T) {
	c := github.NewClient()
	if c.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", c.Timeout)
	}
	if c.CheckRedirect == nil || !errors.Is(c.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Error("NewClient's client follows redirects")
	}
}

func checkNoToken(t *testing.T, err error, token string) {
	t.Helper()
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "Bearer") {
		t.Errorf("error quotes the token or its header: %v", err)
	}
}
