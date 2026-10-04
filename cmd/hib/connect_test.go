package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

const testGitHubToken = "gho_testOnlyToken1234567890"

func TestConnectGitHubFromGH(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	var ghCalls, sshCalls int
	c.gh = func(_ context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		ghCalls++
		if !slices.Equal(args, []string{"auth", "token", "--hostname", "github.com"}) || stdin != nil {
			t.Error("gh didn't receive the expected auth token invocation")
		}
		fmtToken := testGitHubToken + "\n"
		io.WriteString(stdout, fmtToken)
		io.WriteString(stderr, "\x1b[2Jgh diagnostic\x1b[K\n")
		return 0, nil
	}
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		sshCalls++
		want := []string{"sudo", "-n", "hibd", "connect", "github", "--source", "gh", "--json"}
		if !slices.Equal(words, want) {
			t.Error("connect didn't run the expected sudo hibd command")
			return 1, true
		}
		data, err := io.ReadAll(h.lastStdin)
		if err != nil || string(data) != testGitHubToken {
			t.Error("gh token wasn't sent only on ssh stdin")
		}
		return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
			Login: "octocat", Name: "octocat", Email: "1+octocat@users.noreply.github.com",
		}}), true
	}
	code := c.hib("connect", "github", "--from-gh")
	assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
	if code != 0 || ghCalls != 1 || sshCalls != 1 {
		t.Fatalf("exit %d, gh calls %d, ssh calls %d", code, ghCalls, sshCalls)
	}
	want := "connected GitHub as octocat. Machines commit as 1+octocat@users.noreply.github.com\n"
	if c.out.String() != want || c.errOut.String() != "\\x1b[2Jgh diagnostic\\x1b[K\n" {
		t.Error("connect didn't print the account and terminal-safe gh diagnostic")
	}
}

func TestConnectGitHubFromStdin(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	var received bool
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		want := []string{"sudo", "-n", "hibd", "connect", "github", "--source", "token", "--json"}
		if !slices.Equal(words, want) {
			t.Error("connect didn't run the expected sudo hibd command")
			return 1, true
		}
		data, err := io.ReadAll(h.lastStdin)
		if err != nil {
			t.Fatal("couldn't read ssh stdin")
		}
		received = string(data) == testGitHubToken
		return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
			Login: "octocat", Name: "The Octocat", Email: "1+octocat@users.noreply.github.com",
		}}), true
	}
	c.stdin = strings.NewReader(testGitHubToken + "\r\n")
	code := c.hib("connect", "github", "--token")
	assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
	if code != 0 || !received {
		t.Fatalf("connect exit %d, expected the token only on ssh stdin", code)
	}
	want := "connected GitHub as octocat (The Octocat). Machines commit as 1+octocat@users.noreply.github.com\n"
	if c.out.String() != want || c.errOut.Len() != 0 {
		t.Error("connect didn't print the connected account")
	}
}

func TestConnectGitHubGHFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		code   int
		err    error
		want   string
	}{
		{"missing", "", -1, &exec.Error{Name: "gh", Err: exec.ErrNotFound}, "install gh"},
		{"not logged in", testGitHubToken, 1, nil, "run gh auth login -h github.com first"},
		{"runner failed", "", -1, errors.New(testGitHubToken), "run gh auth login -h github.com first"},
		{"empty token", "\n", 0, nil, "secret value is empty"},
		{"large token", strings.Repeat("x", (64<<10)+1), 0, nil, "64 KiB"},
		{"large output", testGitHubToken + strings.Repeat("x", 128<<10), 0, nil, "64 KiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			c.gh = func(_ context.Context, _ []string, _ io.Reader, stdout, stderr io.Writer) (int, error) {
				io.WriteString(stdout, tc.stdout)
				io.WriteString(stderr, "\x1b[2Jgh diagnostic\x1b[K\n")
				return tc.code, tc.err
			}
			code := c.hib("connect", "github", "--from-gh")
			calls := h.takeCalls()
			assertTokenPrivate(t, c, calls, testGitHubToken)
			if code != 1 || !strings.Contains(c.errOut.String(), tc.want) {
				t.Errorf("exit %d, expected a gh failure explaining %s", code, tc.want)
			}
			if !strings.Contains(c.errOut.String(), "\\x1b[2Jgh diagnostic\\x1b[K") {
				t.Error("gh stderr wasn't terminal-safe")
			}
			if tc.name == "missing" && (!strings.Contains(c.errOut.String(), "gh auth login") ||
				!strings.Contains(c.errOut.String(), "--token")) {
				t.Error("missing gh didn't explain login and the token alternative")
			}
			if c.out.Len() != 0 || len(calls) != 0 {
				t.Error("failed gh printed success or ran a host command")
			}
		})
	}
}

func TestConnectGitHubWarnsAboutScopes(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	c.gh = func(_ context.Context, _ []string, _ io.Reader, stdout, _ io.Writer) (int, error) {
		io.WriteString(stdout, testGitHubToken)
		return 0, nil
	}
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
			Login: "octocat", Email: "1+octocat@users.noreply.github.com",
			Source: hibapi.GitHubFromGH, ScopesKnown: true, Scopes: []string{"repo", "read:org"},
		}}), true
	}
	code := c.hib("connect", "github", "--from-gh")
	assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
	want := "hib: warning: the token has no workflow scope, so machines can't push changes under .github/workflows. " +
		"To fix it, run gh auth refresh -h github.com -s workflow, then hib connect github --from-gh again.\n"
	if code != 0 || c.errOut.String() != want {
		t.Errorf("exit %d, expected a scope warning on stderr without failing connect", code)
	}
	if strings.Contains(c.out.String(), "warning") {
		t.Error("scope warning reached stdout")
	}
}

func TestConnectGitHubRejectsArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"both sources", []string{"github", "--from-gh", "--token"}, "choose exactly one"},
		{"neither source", []string{"github"}, "choose exactly one"},
		{"false source", []string{"github", "--token=false"}, "choose exactly one"},
		{"another provider", []string{"gitlab", "--token"}, "only GitHub and Codex are supported"},
		{"codex with a flag", []string{"codex", "--token"}, "hib connect codex takes no flags"},
		{"codex with a false flag", []string{"codex", "--from-gh=false"}, "hib connect codex takes no flags"},
		{"codex and a token", []string{"codex", testGitHubToken}, "arguments end up in shell history"},
		{"no arguments", nil, "usage: hib connect"},
		{"no provider", []string{"--token"}, "usage: hib connect"},
		{"positional token", []string{"github", "--token", testGitHubToken}, "arguments end up in shell history"},
		{"positional before flags", []string{"github", testGitHubToken, "--token"}, "arguments end up in shell history"},
		{"positional after separator", []string{"github", "--token", "--", testGitHubToken}, "arguments end up in shell history"},
		{"long token assignment", []string{"github", "--token=" + testGitHubToken}, "arguments end up in shell history"},
		{"short token assignment", []string{"github", "-token=" + testGitHubToken}, "arguments end up in shell history"},
		{"assignment before provider", []string{"--token=" + testGitHubToken, "github"}, "arguments end up in shell history"},
		{"from gh assignment", []string{"github", "--from-gh=" + testGitHubToken}, "arguments end up in shell history"},
		{"unknown flag", []string{"github", "--" + testGitHubToken}, "unknown connect flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			c.stdin = strings.NewReader(testGitHubToken)
			code := c.hib(append([]string{"connect"}, tc.args...)...)
			calls := h.takeCalls()
			assertTokenPrivate(t, c, calls, testGitHubToken)
			if code == 0 || !strings.Contains(c.errOut.String(), tc.want) {
				t.Errorf("exit %d, expected rejection explaining %s", code, tc.want)
			}
			if len(calls) != 0 {
				t.Error("rejected connect ran a host command")
			}
			if !strings.Contains(c.errOut.String(), "usage: hib connect") {
				t.Error("invalid connect arguments didn't show usage")
			}
		})
	}
}

func assertTokenPrivate(t *testing.T, c *client, calls [][]string, token string) {
	t.Helper()
	if strings.Contains(c.out.String(), token) || strings.Contains(c.errOut.String(), token) {
		t.Error("token reached CLI output")
	}
	for _, call := range calls {
		for _, arg := range call {
			if strings.Contains(arg, token) {
				t.Error("token reached command arguments")
			}
		}
	}
}

func TestConnectGitHubDoesNotPrintReflectedToken(t *testing.T) {
	for _, mode := range []string{"gh stderr", "ssh stderr", "hibd error", "unreadable reply", "account fields", "ssh runner error"} {
		t.Run(mode, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			c.gh = func(_ context.Context, _ []string, _ io.Reader, stdout, stderr io.Writer) (int, error) {
				io.WriteString(stdout, testGitHubToken)
				if mode == "gh stderr" {
					io.WriteString(stderr, "gh: \x1b[2J")
					io.WriteString(stderr, testGitHubToken[:10])
					io.WriteString(stderr, testGitHubToken[10:]+"\x1b[K\n")
				}
				return 0, nil
			}
			h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
				account := &hibapi.GitHub{Login: "octocat", Email: "1+octocat@users.noreply.github.com"}
				switch mode {
				case "ssh stderr":
					io.WriteString(stderr, "hibd: \x1b[2J")
					io.WriteString(stderr, testGitHubToken[:10])
					io.WriteString(stderr, testGitHubToken[10:]+"\x1b[K\n")
				case "hibd error":
					return writeTestResponse(stdout, hibapi.Response{Error: "token refused: " + testGitHubToken}), true
				case "unreadable reply":
					io.WriteString(stdout, "not JSON: "+testGitHubToken)
					return 1, true
				case "account fields":
					account.Login, account.Name, account.Email = testGitHubToken, testGitHubToken, testGitHubToken
				}
				return writeTestResponse(stdout, hibapi.Response{GitHub: account}), true
			}
			if mode == "ssh runner error" {
				c.ssh = func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
					h.record(args)
					return -1, errors.New(testGitHubToken)
				}
			}
			code := c.hib("connect", "github", "--from-gh")
			assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
			wantCode := 0
			if mode == "hibd error" || mode == "unreadable reply" || mode == "ssh runner error" {
				wantCode = 1
			}
			if code != wantCode {
				t.Errorf("exit %d, want %d", code, wantCode)
			}
			output := c.out.String() + c.errOut.String()
			if !strings.Contains(output, "[redacted]") {
				t.Error("reflected token wasn't replaced by a redaction marker")
			}
			if strings.Contains(output, "\x1b") {
				t.Error("reflected diagnostic contained terminal controls")
			}
		})
	}
}

func TestConnectGitHubRemoteErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response hibapi.Response
		empty    bool
		want     string
	}{
		{"missing account", hibapi.Response{}, false, "hibd didn't say which account it connected"},
		{"hibd error", hibapi.Response{Error: "GitHub refused the token: connect again\x1b[2J"}, false,
			"GitHub refused the token: connect again\\x1b[2J"},
		{"no sudo", hibapi.Response{}, true, "hibd needs passwordless sudo on the host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			c.stdin = strings.NewReader(testGitHubToken)
			h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
				if tc.empty {
					return 1, true
				}
				return writeTestResponse(stdout, tc.response), true
			}
			code := c.hib("connect", "github", "--token")
			assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
			if code != 1 || c.out.Len() != 0 || !strings.Contains(c.errOut.String(), tc.want) {
				t.Errorf("exit %d, expected the host's failure explanation", code)
			}
		})
	}
}

func TestConnectGitHubAcceptsBooleanLiterals(t *testing.T) {
	for _, literal := range []string{"1", "t", "T", "TRUE", "true", "True"} {
		t.Run(literal, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			c.stdin = strings.NewReader(testGitHubToken)
			h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
				if !slices.Equal(words, []string{"sudo", "-n", "hibd", "connect", "github", "--source", "token", "--json"}) {
					t.Error("boolean token flag didn't select stdin")
				}
				return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
					Login: "octocat", Email: "1+octocat@users.noreply.github.com",
				}}), true
			}
			code := c.hib("connect", "-token="+literal, "github")
			assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
			if code != 0 || c.errOut.Len() != 0 {
				t.Errorf("valid boolean literal exited %d", code)
			}
		})
	}
}

func TestConnectGitHubAccountIsTerminalSafe(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	c.stdin = strings.NewReader(testGitHubToken)
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
			Login: "octocat\x1b", Name: "The Octocat\x07", Email: "1+octocat@users.noreply.github.com\x1b",
		}}), true
	}
	code := c.hib("connect", "github", "--token")
	assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
	want := "connected GitHub as octocat\\x1b (The Octocat\\x07). Machines commit as 1+octocat@users.noreply.github.com\\x1b\n"
	if code != 0 || c.out.String() != want {
		t.Errorf("exit %d, account fields weren't terminal-safe", code)
	}
}

func TestConnectGitHubReplaysTokenAfterWakingHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.states = []string{"VM deallocated"}
	c.stdin = strings.NewReader(testGitHubToken + "\n")
	var attempts int
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if len(words) == 0 || words[0] != "sudo" {
			return 0, false
		}
		attempts++
		data, err := io.ReadAll(h.lastStdin)
		if err != nil || string(data) != testGitHubToken {
			t.Error("ssh didn't receive the complete token after waking")
		}
		if attempts == 1 {
			return 255, true
		}
		return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
			Login: "octocat", Email: "1+octocat@users.noreply.github.com",
		}}), true
	}
	code := c.hib("connect", "github", "--token")
	calls := h.takeCalls()
	assertTokenPrivate(t, c, calls, testGitHubToken)
	if code != 0 || attempts != 2 || !strings.Contains(c.errOut.String(), "the host is up after") {
		t.Errorf("exit %d, connect attempts %d, expected a wake and retry", code, attempts)
	}
	var starts int
	for _, call := range calls {
		if call[0] == "az" && call[2] == "start" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("Azure starts %d, want 1", starts)
	}
}

func TestConnectCodex(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	var deadline time.Time
	run := c.ssh
	c.ssh = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		deadline, _ = ctx.Deadline()
		return run(ctx, args, stdin, stdout, stderr)
	}
	var shown string
	h.override = func(words []string, stdout, stderr io.Writer) (int, bool) {
		if want := []string{"sudo", "-n", "hibd", "connect", "codex", "--json"}; !slices.Equal(words, want) {
			t.Errorf("connect codex ran %q, want %q", words, want)
			return 1, true
		}
		if h.lastStdin != nil {
			t.Error("connect codex sent hibd input")
		}
		io.WriteString(stderr, "Follow these steps to sign in with ChatGPT using device code authorization:\n")
		io.WriteString(stderr, "1. Open this link in your browser\n   \x1b[94mhttps://auth.openai.com/codex/device\x1b[0m\n")
		io.WriteString(stderr, "2. Enter this one-time code \x1b[90m(expires in 15 minutes)\x1b[0m\n   \x1b[94mABCD-12345\x1b[0m\n")
		shown = c.errOut.String()
		return writeTestResponse(stdout, hibapi.Response{Codex: &hibapi.Codex{
			Email: "dev@example.com", Plan: "plus", AccountID: "acct-1234", Health: hibapi.CodexOK,
		}}), true
	}
	start := time.Now()
	code := c.hib("connect", "codex")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if want := "connected Codex as dev@example.com, on the plus plan\n"; c.out.String() != want {
		t.Errorf("printed %q, want %q", c.out, want)
	}
	if !strings.Contains(shown, "\n   ABCD-12345\n") || !strings.Contains(shown, "\n   https://auth.openai.com/codex/device\n") {
		t.Errorf("while hibd waited, hib showed %q, want the link and the code, without Codex's colours", shown)
	}
	if strings.Contains(c.errOut.String(), "\x1b") {
		t.Error("hib passed hibd's terminal escapes through")
	}
	if wait := deadline.Sub(start); wait < 16*time.Minute-time.Second || wait > 16*time.Minute+time.Second {
		t.Errorf("hib waits %v for hibd, want 16m, a minute more than the code lasts", wait)
	}
}

func TestConnectCodexWaitsTheFullTimeAfterWakingHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.states = []string{"VM deallocated"}
	var deadlines []time.Time
	run := c.ssh
	c.ssh = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(strings.Join(args, " "), "'connect' 'codex'") {
			deadline, _ := ctx.Deadline()
			deadlines = append(deadlines, deadline)
		}
		return run(ctx, args, stdin, stdout, stderr)
	}
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if !slices.Contains(words, "codex") {
			return 0, false
		}
		if len(deadlines) == 1 {
			return 255, true
		}
		return writeTestResponse(stdout, hibapi.Response{Codex: &hibapi.Codex{
			Email: "dev@example.com", Plan: "plus", AccountID: "acct-1234", Health: hibapi.CodexOK,
		}}), true
	}
	code := c.hib("connect", "codex")
	if code != 0 || len(deadlines) != 2 || !strings.Contains(c.errOut.String(), "the host is up after") {
		t.Fatalf("exit %d, connect attempts %d, expected a wake and retry: %s", code, len(deadlines), c.errOut)
	}
	// hibd starts the login only once the host is up, so waking it mustn't eat into the code's time.
	if !deadlines[1].After(deadlines[0]) {
		t.Error("hib's 16 minutes for hibd started before the host woke, want them to start after")
	}
}

func TestConnectCodexFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response hibapi.Response
		empty    bool
		want     string
	}{
		{"hibd error", hibapi.Response{Error: "codex login failed: turn on device code login in ChatGPT\x1b[2J"}, false,
			"hib: codex login failed: turn on device code login in ChatGPT\\x1b[2J\n"},
		{"missing account", hibapi.Response{}, false, "hib: hibd didn't say which account it connected\n"},
		{"no sudo", hibapi.Response{}, true, "hib: hibd needs passwordless sudo on the host\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
				if tc.empty {
					return 1, true
				}
				return writeTestResponse(stdout, tc.response), true
			}
			code := c.hib("connect", "codex")
			if code != 1 || c.out.Len() != 0 || c.errOut.String() != tc.want {
				t.Errorf("exit %d, stdout %q, stderr %q, want exit 1 and stderr %q", code, c.out, c.errOut, tc.want)
			}
		})
	}
}

func TestConnectCodexAccountIsTerminalSafe(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{Codex: &hibapi.Codex{Email: "dev@example.com\x1b]0;x\x07", Plan: "plus\x1b[2J"}}), true
	}
	code := c.hib("connect", "codex")
	want := "connected Codex as dev@example.com\\x1b]0;x\\x07, on the plus\\x1b[2J plan\n"
	if code != 0 || c.out.String() != want {
		t.Errorf("exit %d, printed %q, want %q", code, c.out, want)
	}
}
