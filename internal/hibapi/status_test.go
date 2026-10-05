package hibapi_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

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
			name: "no workflow, from the device flow",
			gh:   hibapi.GitHub{Source: hibapi.GitHubFromDevice, Scopes: []string{"read:org", "repo"}, ScopesKnown: true},
			want: "the token has no workflow scope, so machines can't push changes under .github/workflows. " +
				"To fix it, run hib connect github again.",
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
		hibapi.GitHubFromGH:     "hib connect github --from-gh",
		"":                      "hib connect github --from-gh",
		hibapi.GitHubFromToken:  "hib connect github --token",
		hibapi.GitHubFromDevice: "hib connect github",
	} {
		if got := (hibapi.GitHub{Source: source}).Reconnect(); got != want {
			t.Errorf("Reconnect() from %q = %q, want %q", source, got, want)
		}
	}
}

func TestGitHubDevicePrompt(t *testing.T) {
	for _, tc := range []struct {
		code      string
		expiresIn time.Duration
		want      string
	}{
		{"WDJB-MJHT", 15 * time.Minute, "To connect GitHub, open https://github.com/login/device and enter the code WDJB-MJHT within 15 minutes.\n"},
		{"WDJB-MJHT", 899 * time.Second, "To connect GitHub, open https://github.com/login/device and enter the code WDJB-MJHT within 14 minutes.\n"},
		{"AB12", time.Minute, "To connect GitHub, open https://github.com/login/device and enter the code AB12 within 1 minute.\n"},
		{"AB12", 30 * time.Second, "To connect GitHub, open https://github.com/login/device and enter the code AB12 within 1 minute.\n"},
	} {
		got := hibapi.GitHubDevicePrompt(tc.code, tc.expiresIn)
		if got != tc.want {
			t.Errorf("GitHubDevicePrompt(%q, %v) =\n%q, want\n%q", tc.code, tc.expiresIn, got, tc.want)
		}
		if line := strings.TrimSuffix(got, "\n"); !hibapi.IsGitHubDevicePrompt(line) {
			t.Errorf("IsGitHubDevicePrompt(%q) = false, want true", line)
		}
	}
	const prompt = "To connect GitHub, open https://github.com/login/device and enter the code WDJB-MJHT within 15 minutes."
	for _, line := range []string{
		prompt + "\n",
		prompt + " ",
		"hibd: " + prompt,
		strings.Replace(prompt, "https://github.com/login/device", "https://github.com/login/device.example", 1),
		strings.Replace(prompt, "https://github.com/login/device", "https://evil.example/login/device", 1),
		strings.Replace(prompt, "WDJB-MJHT", "WDJB MJHT", 1),
		strings.Replace(prompt, "WDJB-MJHT", "WDJB-", 1),
		strings.Replace(prompt, "15 minutes", "0 minutes", 1),
		strings.Replace(prompt, "15 minutes", "15 minutes or so", 1),
		"Waiting for GitHub. Press Ctrl-C to cancel.",
		"",
	} {
		if hibapi.IsGitHubDevicePrompt(line) {
			t.Errorf("IsGitHubDevicePrompt(%q) = true, want false", line)
		}
	}
}

func TestWho(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"The Octocat", "octocat (The Octocat)"},
		{"octocat", "octocat"},
		{"", "octocat"},
	} {
		if got := (hibapi.GitHub{Login: "octocat", Name: tc.name}).Who(); got != tc.want {
			t.Errorf("Who() with name %q = %q, want %q", tc.name, got, tc.want)
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
		{name: "codex ok", status: hibapi.Status{E2B: up, Codex: &hibapi.Codex{Health: hibapi.CodexOK}}, want: true},
		{name: "codex dead", status: hibapi.Status{E2B: up, Codex: &hibapi.Codex{Health: hibapi.CodexDead}}},
		{name: "codex stuck", status: hibapi.Status{E2B: up, Codex: &hibapi.Codex{Health: hibapi.CodexStuck}}},
		{name: "codex unknown health", status: hibapi.Status{E2B: up, Codex: &hibapi.Codex{Health: "odd"}}},
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
				Codex: &hibapi.Codex{Email: "octo@example.com", Plan: "plus", AccountID: "acct-1", Health: hibapi.CodexOK,
					Expires: time.Date(2026, 10, 14, 9, 30, 0, 0, time.FixedZone("EEST", 3*3600))},
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
				"Codex   octo@example.com, plus plan, token expires 2026-10-14 06:30 UTC\n" +
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
				"Codex   not connected: run hib connect codex\n" +
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
				"Codex    not connected: run hib connect codex\n" +
				"secrets  none\n",
		},
		{
			name: "codex stuck",
			status: hibapi.Status{
				Version: "0.5.0",
				E2B:     hibapi.Health{OK: true},
				Codex: &hibapi.Codex{Email: "octo@example.com", Plan: "pro", Health: hibapi.CodexStuck,
					Problem: "a refresh was interrupted: run hib connect codex", Expires: time.Date(2026, 10, 14, 6, 30, 0, 0, time.UTC)},
			},
			want: "hibd     0.5.0\n" +
				"E2B      ok\n" +
				"GitHub   not connected: run hib connect github --from-gh\n" +
				"Codex    octo@example.com, pro plan, token expires 2026-10-14 06:30 UTC\n" +
				"         stuck: a refresh was interrupted: run hib connect codex\n" +
				"secrets  none\n",
		},
		{
			name: "codex dead, without a fix in the problem",
			status: hibapi.Status{
				Version: "0.5.0",
				E2B:     hibapi.Health{OK: true},
				Codex: &hibapi.Codex{Email: "octo@example.com", Plan: "pro", Health: hibapi.CodexDead,
					Problem: "OpenAI refused the refresh token", Expires: time.Date(2026, 10, 14, 6, 30, 0, 0, time.UTC)},
			},
			want: "hibd     0.5.0\n" +
				"E2B      ok\n" +
				"GitHub   not connected: run hib connect github --from-gh\n" +
				"Codex    octo@example.com, pro plan, token expires 2026-10-14 06:30 UTC\n" +
				"         dead: OpenAI refused the refresh token. Run hib connect codex\n" +
				"secrets  none\n",
		},
		{
			name: "codex dead, without a problem",
			status: hibapi.Status{
				Version: "0.5.0",
				E2B:     hibapi.Health{OK: true},
				Codex:   &hibapi.Codex{Email: "octo@example.com", Plan: "pro", Health: hibapi.CodexDead, Expires: time.Date(2026, 10, 14, 6, 30, 0, 0, time.UTC)},
			},
			want: "hibd     0.5.0\n" +
				"E2B      ok\n" +
				"GitHub   not connected: run hib connect github --from-gh\n" +
				"Codex    octo@example.com, pro plan, token expires 2026-10-14 06:30 UTC\n" +
				"         dead: run hib connect codex\n" +
				"secrets  none\n",
		},
		{
			name: "terminal escapes and line breaks",
			status: hibapi.Status{
				Version: "0.4\x1b[2J",
				E2B:     hibapi.Health{Detail: "line one\nline\ttwo\r"},
				GitHub:  &hibapi.GitHub{Login: "octo", ID: 42, Name: "Evil\x07Name", Email: "42+octo@users.noreply.github.com"},
				Codex: &hibapi.Codex{Email: "a@b\x1b]0;x\x07", Plan: "pl\nus", Health: "odd\x1b", Problem: "line\none",
					Expires: time.Date(2026, 10, 14, 6, 30, 0, 0, time.UTC)},
				Secrets: []hibapi.SecretHealth{
					{Name: "x\x1b", Sites: []string{"a.co\n"}, State: "odd\x00", Detail: "bad\tdetail\nhere"},
				},
			},
			want: "hibd    0.4\\x1b[2J\n" +
				"E2B     down: line one line two\n" +
				"GitHub  octo (Evil\\x07Name), commits as 42+octo@users.noreply.github.com\n" +
				"Codex   a@b\\x1b]0;x\\x07, pl us plan, token expires 2026-10-14 06:30 UTC\n" +
				"        odd\\x1b: line one. Run hib connect codex\n" +
				"\n" +
				"SECRET  SITES  STATE    DETAIL\n" +
				"x\\x1b   a.co   odd\\x00  bad detail here\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			hibapi.PrintStatus(&out, tc.host, tc.status, nil)
			if got := out.String(); got != tc.want {
				t.Errorf("PrintStatus =\n%s\nwant\n%s\n(%q)", got, tc.want, got)
			}
		})
	}
}

func TestPrintCodex(t *testing.T) {
	var out bytes.Buffer
	hibapi.PrintCodex(&out, hibapi.Codex{Email: "octo@example.com", Plan: "pro", Health: hibapi.CodexStuck,
		Expires: time.Date(2026, 10, 14, 6, 30, 0, 0, time.UTC)}, nil)
	want := "Codex  octo@example.com, pro plan, token expires 2026-10-14 06:30 UTC\n" +
		"       stuck: run hib connect codex\n"
	if out.String() != want {
		t.Errorf("PrintCodex = %q, want %q", out.String(), want)
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
