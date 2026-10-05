package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

const testGitHubToken = "gho_testOnlyToken1234567890"

func TestConnectGitHubDefaultsToDeviceFlow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		browser bool
	}{
		{"no flags", []string{"connect", "github"}, true},
		{"false token flag", []string{"connect", "github", "--token=false"}, true},
		{"nil browser", []string{"connect", "github"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			if tc.browser {
				c.browser = func(context.Context, string) error { return nil }
			}
			prompt := hibapi.GitHubDevicePrompt("WDJB-MJHT", 15*time.Minute)
			var deadline time.Time
			run := c.ssh
			c.ssh = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
				deadline, _ = ctx.Deadline()
				return run(ctx, args, stdin, stdout, stderr)
			}
			h.override = func(words []string, stdout, stderr io.Writer) (int, bool) {
				if want := []string{"sudo", "-n", "hibd", "connect", "github", "--source", "device", "--json"}; !slices.Equal(words, want) {
					t.Error("connect didn't select the device source through sudo")
				}
				io.WriteString(stderr, prompt)
				return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
					Login: "octocat", Email: "1+octocat@users.noreply.github.com",
					Source: hibapi.GitHubFromDevice, ScopesKnown: true, Scopes: []string{"repo", "read:org"},
				}}), true
			}
			start := time.Now()
			code := c.hib(tc.args...)
			end := time.Now()
			if code != 0 {
				t.Fatalf("device connect exited %d: %s", code, c.errOut)
			}
			if c.out.String() != "connected GitHub as octocat. Machines commit as 1+octocat@users.noreply.github.com\n" {
				t.Error("device connect changed the success output")
			}
			if !strings.Contains(c.errOut.String(), "run hib connect github again.") {
				t.Error("device connect didn't show the scope warning")
			}
			if !strings.HasPrefix(c.errOut.String(), prompt) {
				t.Error("device connect didn't stream the prompt")
			}
			calls := h.takeCalls()
			if len(calls) != 1 || !slices.Contains(calls[0], "StdinNull=no") {
				t.Error("device connect didn't pin SSH's stdin open")
			}
			if deadline.Before(start.Add(17*time.Minute)) || deadline.After(end.Add(17*time.Minute)) {
				t.Error("device connect didn't give hibd 17 minutes")
			}
		})
	}
}

func TestConnectGitHubDeviceHoldsStdinForEachAttempt(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run("wake "+strconv.FormatBool(wake), func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			var opens int
			c.browser = func(context.Context, string) error {
				opens++
				return nil
			}
			var inputs []int
			var deadlines []time.Time
			h.sshInput = func(ctx context.Context, args []string, stdin io.Reader) {
				if !strings.Contains(strings.Join(args, " "), "'connect' 'github'") {
					if stdin != nil {
						t.Error("an unattended host call received stdin")
					}
					return
				}
				fd := duplicateSSHStdin(t, stdin)
				assertSSHStdinOpen(t, fd)
				if len(inputs) > 0 {
					assertSSHStdinEOF(t, inputs[len(inputs)-1])
				}
				inputs = append(inputs, fd)
				deadline, _ := ctx.Deadline()
				deadlines = append(deadlines, deadline)
			}
			h.override = func(words []string, stdout, stderr io.Writer) (int, bool) {
				if words[0] != "sudo" {
					return 0, false
				}
				io.WriteString(stderr, hibapi.GitHubDevicePrompt("WDJB-MJHT", 15*time.Minute))
				if wake && len(inputs) == 1 {
					h.states = []string{"VM deallocated"}
					return 255, true
				}
				assertSSHStdinOpen(t, inputs[len(inputs)-1])
				return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{
					Login: "octocat", Email: "1+octocat@users.noreply.github.com",
				}}), true
			}
			if code := c.hib("connect", "github"); code != 0 {
				t.Fatalf("device connect exited %d: %s", code, c.errOut)
			}
			want := 1
			if wake {
				want = 2
			}
			if len(inputs) != want {
				t.Fatalf("connect attempts = %d, want %d", len(inputs), want)
			}
			for _, fd := range inputs {
				assertSSHStdinEOF(t, fd)
			}
			if opens != 1 {
				t.Errorf("browser opens = %d, want one across all attempts", opens)
			}
			if wake && !deadlines[1].After(deadlines[0]) {
				t.Error("the device login timeout didn't restart after waking")
			}
		})
	}
}

func duplicateSSHStdin(t *testing.T, stdin io.Reader) int {
	t.Helper()
	file, ok := stdin.(*os.File)
	if !ok {
		t.Fatal("attended SSH didn't receive a pipe file descriptor")
	}
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	return fd
}

func assertSSHStdinOpen(t *testing.T, fd int) {
	t.Helper()
	n, err := syscall.Read(fd, make([]byte, 1))
	if n > 0 || !errors.Is(err, syscall.EAGAIN) {
		t.Error("attended SSH's stdin got bytes or EOF before the call ended")
	}
}

func assertSSHStdinEOF(t *testing.T, fd int) {
	t.Helper()
	n, err := syscall.Read(fd, make([]byte, 1))
	if n != 0 || err != nil {
		t.Error("attended SSH's stdin didn't reach EOF after the call ended")
	}
}

func TestConnectGitHubDeviceCancellationIsHonest(t *testing.T) {
	for _, reply := range []string{"ssh cancelled", "hibd cancelled", "already stored"} {
		t.Run(reply, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fd := -1
			h.sshInput = func(_ context.Context, _ []string, stdin io.Reader) {
				fd = duplicateSSHStdin(t, stdin)
				assertSSHStdinOpen(t, fd)
			}
			h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
				cancel()
				if reply == "already stored" {
					return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{Login: "octocat"}}), true
				}
				return writeTestResponse(stdout, hibapi.Response{Error: "the GitHub login was cancelled. Nothing was stored"}), true
			}
			if reply == "ssh cancelled" {
				run := c.ssh
				c.ssh = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
					run(ctx, args, stdin, stdout, stderr)
					return -1, ctx.Err()
				}
			}
			c.out.Reset()
			c.errOut.Reset()
			code := c.run(ctx, []string{"-config", c.dir, "connect", "github"})
			want := "hib: the GitHub login was cancelled. Run hib status to see whether GitHub is connected\n"
			if code != 1 || c.out.Len() != 0 || c.errOut.String() != want {
				t.Errorf("cancelled login: exit %d, stdout %q, stderr %q", code, c.out, c.errOut)
			}
			assertSSHStdinEOF(t, fd)
		})
	}
}

func TestConnectGitHubDeviceOpensBrowserOnceAfterCompletePrompt(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	opened := make(chan string, 2)
	c.browser = func(_ context.Context, url string) error {
		opened <- url
		return nil
	}
	prompt := hibapi.GitHubDevicePrompt("WDJB-MJHT", 15*time.Minute)
	log := strings.Repeat("l", 2048) + "\n\x1b[2Jhost log\x1b[K\n"
	h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
		io.WriteString(stderr, log)
		for _, part := range []string{prompt[:17], prompt[17:50], prompt[50 : len(prompt)-1]} {
			io.WriteString(stderr, part)
		}
		select {
		case <-opened:
			t.Error("the browser opened before the complete prompt line")
		default:
		}
		io.WriteString(stderr, "\n")
		if url := <-opened; url != "https://github.com/login/device" {
			t.Error("the browser didn't receive the fixed device URL")
		}
		io.WriteString(stderr, prompt+prompt)
		return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{Login: "octocat"}}), true
	}
	if code := c.hib("connect", "github"); code != 0 {
		t.Fatalf("device connect exited %d: %s", code, c.errOut)
	}
	select {
	case <-opened:
		t.Error("the browser opened more than once")
	default:
	}
	if c.errOut.String() != termsafe.String(log)+strings.Repeat(prompt, 3) {
		t.Error("the streamed diagnostics weren't preserved and terminal-safe")
	}
}

func TestConnectGitHubDeviceDoesNotOpenBrowserForOtherLines(t *testing.T) {
	prompt := hibapi.GitHubDevicePrompt("WDJB-MJHT", 15*time.Minute)
	for _, line := range []string{
		"Waiting for GitHub. Press Ctrl-C to cancel.\n",
		"See https://github.com/login/device yourself.\n",
		strings.Replace(prompt, "https://github.com/login/device", "https://example.org/login/device", 1),
		strings.Replace(prompt, "WDJB-MJHT", strings.Repeat("A", 2048), 1),
		strings.Repeat("x", 2048) + prompt,
		strings.Repeat("\x1b[0m", 512) + prompt,
		strings.TrimSuffix(prompt, "\n"),
	} {
		t.Run(strconv.Itoa(len(line))+line[:min(16, len(line))], func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			c.browser = func(context.Context, string) error {
				t.Error("the browser opened for something other than a bounded prompt line")
				return nil
			}
			h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
				for i := 0; i < len(line); i += 31 {
					io.WriteString(stderr, line[i:min(i+31, len(line))])
				}
				return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{Login: "octocat"}}), true
			}
			if code := c.hib("connect", "github"); code != 0 || c.errOut.String() != termsafe.String(line) {
				t.Error("non-prompt diagnostics weren't passed through")
			}
		})
	}
}

func TestConnectGitHubBrowserOnlyWatchesDeviceCalls(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	c.browser = func(context.Context, string) error {
		t.Error("the browser opened outside the device flow")
		return nil
	}
	h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
		io.WriteString(stderr, hibapi.GitHubDevicePrompt("WDJB-MJHT", 15*time.Minute))
		return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{Login: "octocat"}}), true
	}
	c.stdin = strings.NewReader(testGitHubToken)
	if code := c.hib("connect", "github", "--token"); code != 0 {
		t.Errorf("token connect exited %d", code)
	}
}

func TestConnectGitHubDeviceWaitsForBrowserAndSerializesFailureNote(t *testing.T) {
	for _, ending := range []string{"\n", ""} {
		t.Run("line ending "+strconv.Itoa(len(ending)), func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			prompt := hibapi.GitHubDevicePrompt("WDJB-MJHT", 15*time.Minute)
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{})
				failed := make(chan struct{})
				sshReturned := make(chan struct{})
				c.browser = func(context.Context, string) error {
					close(started)
					<-release
					close(failed)
					return errors.New("opener failed\x1b[2J")
				}
				h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
					io.WriteString(stderr, prompt)
					<-started
					io.WriteString(stderr, "host partial ")
					close(sshReturned)
					<-failed
					synctest.Wait()
					io.WriteString(stderr, "line"+ending)
					return writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{Login: "octocat"}}), true
				}
				done := make(chan int, 1)
				go func() { done <- c.hib("connect", "github") }()
				<-sshReturned
				synctest.Wait()
				close(release)
				if code := <-done; code != 0 {
					t.Errorf("a failed browser changed connect's result to %d", code)
				}
				want := prompt + "host partial line\nhib: note: couldn't open a browser (opener failed\\x1b[2J). " +
					"Open https://github.com/login/device yourself\n"
				if c.errOut.String() != want {
					t.Errorf("browser note interleaved with a host line: %q", c.errOut)
				}
			})
		})
	}
}

func TestConnectGitHubDeviceWaitsForSlowBrowserAfterSSHReturns(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		sshReturned := make(chan struct{})
		c.browser = func(context.Context, string) error {
			close(started)
			<-release
			return nil
		}
		h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
			io.WriteString(stderr, hibapi.GitHubDevicePrompt("WDJB-MJHT", 15*time.Minute))
			<-started
			code := writeTestResponse(stdout, hibapi.Response{GitHub: &hibapi.GitHub{Login: "octocat"}})
			close(sshReturned)
			return code, true
		}
		done := make(chan int, 1)
		go func() { done <- c.hib("connect", "github") }()
		<-sshReturned
		synctest.Wait()
		select {
		case <-done:
			t.Error("connect returned while its browser opener was still running")
		default:
		}
		close(release)
		if code := <-done; code != 0 {
			t.Errorf("device connect exited %d", code)
		}
	})
}

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
		{"both sources", []string{"github", "--from-gh", "--token"}, "choose at most one"},
		{"another provider", []string{"gitlab", "--token"}, "unknown command gitlab"},
		{"codex with a flag", []string{"codex", "--token"}, "unknown flag --token"},
		{"codex with a false flag", []string{"codex", "--from-gh=false"}, "unknown flag --from-gh"},
		{"token as provider", []string{testGitHubToken}, "arguments end up in shell history"},
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
			if code != 2 || !strings.Contains(c.errOut.String(), tc.want) {
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
			if mode == "unreadable reply" {
				if !strings.Contains(output, "unreadable reply from hibd") || strings.Contains(output, "not JSON") {
					t.Errorf("an unreadable reply to a token was shown: %q", output)
				}
			} else if !strings.Contains(output, "[redacted]") {
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
		for _, source := range []string{"token", "device"} {
			t.Run(tc.name+" "+source, func(t *testing.T) {
				h := newHost(t)
				c := initClient(t, h)
				c.stdin = strings.NewReader(testGitHubToken)
				fd := -1
				if source == "device" {
					h.sshInput = func(_ context.Context, _ []string, stdin io.Reader) {
						fd = duplicateSSHStdin(t, stdin)
						assertSSHStdinOpen(t, fd)
					}
				}
				h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
					if tc.empty {
						return 1, true
					}
					return writeTestResponse(stdout, tc.response), true
				}
				args := []string{"connect", "github"}
				if source == "token" {
					args = append(args, "--token")
				}
				code := c.hib(args...)
				assertTokenPrivate(t, c, h.takeCalls(), testGitHubToken)
				if code != 1 || c.out.Len() != 0 || !strings.Contains(c.errOut.String(), tc.want) {
					t.Errorf("exit %d, expected the host's failure explanation", code)
				}
				if source == "device" {
					assertSSHStdinEOF(t, fd)
				}
			})
		}
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
	end := time.Now()
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
	if deadline.Before(start.Add(16*time.Minute)) || deadline.After(end.Add(16*time.Minute)) {
		t.Errorf("hibd deadline = %v, want 16m from a time between %v and %v", deadline, start, end)
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
		{"missing account", hibapi.Response{}, false, "hib: hibd didn't say which account it connected: run hib status to see what's connected\n"},
		{"no sudo", hibapi.Response{}, true, "hib: hibd needs passwordless sudo on the host: run hib init --host USER@HOST with a user that has it\n"},
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
