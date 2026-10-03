package hibapi_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestScopeWarning(t *testing.T) {
	const ghFix = "To fix it, run gh auth refresh -h github.com -s "
	for _, tc := range []struct {
		name string
		gh   hibapi.GitHub
		want string
	}{
		{name: "scopes unknown", gh: hibapi.GitHub{Source: hibapi.GitHubFromToken}},
		{name: "repo and workflow", gh: hibapi.GitHub{Scopes: []string{"gist", "repo", "workflow"}, ScopesKnown: true}},
		{
			name: "no workflow, from gh",
			gh:   hibapi.GitHub{Source: hibapi.GitHubFromGH, Scopes: []string{"gist", "read:org", "repo"}, ScopesKnown: true},
			want: "the token has no workflow scope, so machines can't push changes under .github/workflows. " +
				ghFix + "workflow, then hib connect github --from-gh again.",
		},
		{
			name: "no repo, from gh",
			gh:   hibapi.GitHub{Source: hibapi.GitHubFromGH, Scopes: []string{"public_repo", "workflow"}, ScopesKnown: true},
			want: "the token has no repo scope, so machines can't clone private repos or push. " +
				ghFix + "repo, then hib connect github --from-gh again.",
		},
		{
			name: "no scopes, source unknown",
			gh:   hibapi.GitHub{ScopesKnown: true},
			want: "the token has no repo or workflow scope, so machines can't clone private repos or push. " +
				ghFix + "repo,workflow, then hib connect github --from-gh again.",
		},
		{
			name: "no workflow, from a token",
			gh:   hibapi.GitHub{Source: hibapi.GitHubFromToken, Scopes: []string{"repo"}, ScopesKnown: true},
			want: "the token has no workflow scope, so machines can't push changes under .github/workflows. " +
				"To fix it, make a classic token with the workflow scope, then run hib connect github --token again.",
		},
		{
			name: "no scopes, from a token",
			gh:   hibapi.GitHub{Source: hibapi.GitHubFromToken, Scopes: []string{}, ScopesKnown: true},
			want: "the token has no repo or workflow scope, so machines can't clone private repos or push. " +
				"To fix it, make a classic token with the repo and workflow scopes, then run hib connect github --token again.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.gh.ScopeWarning(); got != tc.want {
				t.Errorf("ScopeWarning() =\n%q, want\n%q", got, tc.want)
			}
		})
	}
}

func TestReconnect(t *testing.T) {
	for source, want := range map[string]string{
		hibapi.GitHubFromGH:    "hib connect github --from-gh",
		"":                     "hib connect github --from-gh",
		hibapi.GitHubFromToken: "hib connect github --token",
	} {
		if got := (hibapi.GitHub{Source: source}).Reconnect(); got != want {
			t.Errorf("Reconnect() from %q = %q, want %q", source, got, want)
		}
	}
}

func TestStatusOK(t *testing.T) {
	up := hibapi.Health{OK: true}
	for _, tc := range []struct {
		name   string
		status hibapi.Status
		want   bool
	}{
		{name: "nothing to check", status: hibapi.Status{E2B: up}, want: true},
		{name: "E2B down", status: hibapi.Status{E2B: hibapi.Health{Detail: "502"}}},
		{
			name: "ok and unchecked secrets",
			status: hibapi.Status{E2B: up, Secrets: []hibapi.SecretHealth{
				{Name: "a", State: hibapi.SecretOK}, {Name: "b", State: hibapi.SecretUnchecked},
			}},
			want: true,
		},
		{name: "revoked", status: hibapi.Status{E2B: up, Secrets: []hibapi.SecretHealth{{Name: "a", State: hibapi.SecretRevoked}}}},
		{name: "failed", status: hibapi.Status{E2B: up, Secrets: []hibapi.SecretHealth{{Name: "a", State: hibapi.SecretFailed}}}},
		{name: "unknown state", status: hibapi.Status{E2B: up, Secrets: []hibapi.SecretHealth{{Name: "a", State: "odd"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.OK(); got != tc.want {
				t.Errorf("OK() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrintStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		host   string
		status hibapi.Status
		want   string
	}{
		{
			name: "healthy, GitHub without workflow",
			host: "azureuser@203.0.113.7",
			status: hibapi.Status{
				Version: "0.4.0",
				E2B:     hibapi.Health{OK: true},
				GitHub: &hibapi.GitHub{Login: "octo", ID: 42, Name: "Octo Cat", Email: "42+octo@users.noreply.github.com",
					Source: hibapi.GitHubFromGH, Scopes: []string{"repo"}, ScopesKnown: true},
				Secrets: []hibapi.SecretHealth{
					{Name: "github", Sites: []string{"github.com", "api.github.com"}, State: hibapi.SecretOK},
					{Name: "openai", Sites: []string{"api.openai.com"}, State: hibapi.SecretUnchecked, Detail: "no check for these sites"},
				},
			},
			want: "host    azureuser@203.0.113.7\n" +
				"hibd    0.4.0\n" +
				"E2B     ok\n" +
				"GitHub  octo (Octo Cat), commits as 42+octo@users.noreply.github.com\n" +
				"        warning: the token has no workflow scope, so machines can't push changes under .github/workflows. " +
				"To fix it, run gh auth refresh -h github.com -s workflow, then hib connect github --from-gh again.\n" +
				"\n" +
				"SECRET  SITES                      STATE      DETAIL\n" +
				"github  github.com,api.github.com  ok         -\n" +
				"openai  api.openai.com             unchecked  no check for these sites\n",
		},
		{
			name: "broken, not connected, no host",
			status: hibapi.Status{
				E2B: hibapi.Health{Detail: "http://127.0.0.1:3000/health: 502 Bad Gateway"},
				Secrets: []hibapi.SecretHealth{
					{Name: "gh", Sites: []string{"api.github.com"}, State: hibapi.SecretRevoked, Detail: "revoked: run hib secret add gh again"},
				},
			},
			want: "hibd    -\n" +
				"E2B     down: http://127.0.0.1:3000/health: 502 Bad Gateway\n" +
				"GitHub  not connected: run hib connect github --from-gh\n" +
				"\n" +
				"SECRET  SITES           STATE    DETAIL\n" +
				"gh      api.github.com  revoked  revoked: run hib secret add gh again\n",
		},
		{
			name: "name same as login, no secrets",
			status: hibapi.Status{
				Version: "0.4.0",
				E2B:     hibapi.Health{OK: true},
				GitHub:  &hibapi.GitHub{Login: "octo", ID: 42, Name: "octo", Email: "42+octo@users.noreply.github.com"},
			},
			want: "hibd     0.4.0\n" +
				"E2B      ok\n" +
				"GitHub   octo, commits as 42+octo@users.noreply.github.com\n" +
				"secrets  none\n",
		},
		{
			name: "terminal escapes and line breaks",
			status: hibapi.Status{
				Version: "0.4\x1b[2J",
				E2B:     hibapi.Health{Detail: "line one\nline\ttwo\r"},
				GitHub:  &hibapi.GitHub{Login: "octo", ID: 42, Name: "Evil\x07Name", Email: "42+octo@users.noreply.github.com"},
				Secrets: []hibapi.SecretHealth{
					{Name: "x\x1b", Sites: []string{"a.co\n"}, State: "odd\x00", Detail: "bad\tdetail\nhere"},
				},
			},
			want: "hibd    0.4\\x1b[2J\n" +
				"E2B     down: line one line two\n" +
				"GitHub  octo (Evil\\x07Name), commits as 42+octo@users.noreply.github.com\n" +
				"\n" +
				"SECRET  SITES  STATE    DETAIL\n" +
				"x\\x1b   a.co   odd\\x00  bad detail here\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			hibapi.PrintStatus(&out, tc.host, tc.status)
			if got := out.String(); got != tc.want {
				t.Errorf("PrintStatus =\n%s\nwant\n%s\n(%q)", got, tc.want, got)
			}
		})
	}
}

func TestStatusResponseRoundTrip(t *testing.T) {
	want := hibapi.Response{
		V: hibapi.Version,
		GitHub: &hibapi.GitHub{Login: "octo", ID: 42, Name: "Octo", Email: "42+octo@users.noreply.github.com",
			Source: hibapi.GitHubFromGH, Scopes: []string{"repo", "workflow"}, ScopesKnown: true},
		Status: &hibapi.Status{
			Version: "0.4.0",
			E2B:     hibapi.Health{OK: true},
			GitHub:  &hibapi.GitHub{Login: "octo", ID: 42, Name: "Octo", Email: "42+octo@users.noreply.github.com", Source: hibapi.GitHubFromToken},
			Secrets: []hibapi.SecretHealth{{Name: "github", Sites: []string{"github.com"}, State: hibapi.SecretRevoked, Detail: "revoked"}},
		},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := hibapi.DecodeResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	for _, field := range []string{`"github":{`, `"status":{`, `"scopesKnown":true`, `"state":"revoked"`, `"e2b":{"ok":true}`} {
		if !bytes.Contains(data, []byte(field)) {
			t.Errorf("JSON %s lacks %s", data, field)
		}
	}
}
