package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

const (
	githubCodeLifetime = 15 * time.Minute
	githubLoginTimeout = githubCodeLifetime + 2*time.Minute
	// codexLoginTimeout is how long hib connect codex waits for hibd: a minute longer than
	// Codex's device code lasts.
	codexLoginTimeout = 16 * time.Minute
)

func (a *app) connectGitHub(ctx context.Context, cfg *config, fromGH, token bool) int {
	if fromGH && token {
		return a.mistake("choose at most one of --from-gh and --token")
	}
	if !fromGH && !token {
		return a.connectGitHubDevice(ctx, cfg)
	}
	source := hibapi.GitHubFromToken
	var value string
	var err error
	if fromGH {
		source = hibapi.GitHubFromGH
		value, err = a.readGitHubToken(ctx)
	} else {
		value, err = a.readSecretValue("GitHub token: ")
	}
	if err != nil {
		return a.fail(err)
	}
	stdout := &secretWriter{w: a.stdout, value: []byte(value)}
	stderr := &secretWriter{w: a.stderr, value: []byte(value)}
	defer stdout.Flush()
	defer stderr.Flush()
	connected := *a
	connected.stdout, connected.stderr = stdout, stderr
	a = &connected
	r, err := a.remoteSudo(ctx, cfg, shortTimeout, callOptions{stdin: []byte(value)}, "connect", "github", "--source", source, "--json")
	if err != nil {
		return a.fail(err)
	}
	return a.connectedGitHub(r)
}

func (a *app) connectGitHubDevice(ctx context.Context, cfg *config) int {
	args := []string{"connect", "github", "--source", hibapi.GitHubFromDevice, "--json"}
	var browser *githubBrowser
	if a.browser != nil {
		sshArgs, err := cfg.sudoHibdArgs(args...)
		if err != nil {
			return a.fail(err)
		}
		browser = &githubBrowser{
			ctx: ctx, open: a.browser,
			stderr: &lineWriter{w: a.stderr, lineStart: true},
		}
		connected := *a
		connected.stderr = browser.stderr
		ssh := a.ssh
		connected.ssh = func(ctx context.Context, call []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
			if slices.Equal(call, sshArgs) {
				stderr = &githubPromptWriter{w: stderr, browser: browser}
			}
			return ssh(ctx, call, stdin, stdout, stderr)
		}
		a = &connected
	}
	r, err := a.remoteSudo(ctx, cfg, githubLoginTimeout, callOptions{holdStdin: true}, args...)
	if browser != nil {
		browser.wait()
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return a.fail(errors.New("the GitHub login was cancelled. Run hib status to see whether GitHub is connected"))
	}
	if err != nil {
		return a.fail(err)
	}
	return a.connectedGitHub(r)
}

func (a *app) connectedGitHub(r hibapi.Response) int {
	if r.GitHub == nil {
		return a.fail(errors.New("hibd didn't say which account it connected"))
	}
	g := r.GitHub
	fmt.Fprintf(a.stdout, "connected GitHub as %s. Machines commit as %s\n", termsafe.String(g.Who()), termsafe.String(g.Email))
	if warning := g.ScopeWarning(); warning != "" {
		fmt.Fprintf(a.stderr, "hib: warning: %s\n", termsafe.String(warning))
	}
	return 0
}

func (a *app) connectCodex(ctx context.Context, cfg *config, _ []string) int {
	r, err := a.remoteSudo(ctx, cfg, codexLoginTimeout, callOptions{}, "connect", "codex", "--json")
	if err != nil {
		return a.fail(err)
	}
	if r.Codex == nil {
		return a.fail(errors.New("hibd didn't say which account it connected"))
	}
	fmt.Fprintf(a.stdout, "connected Codex as %s, on the %s plan\n", termsafe.String(r.Codex.Email), termsafe.String(r.Codex.Plan))
	return 0
}

func (a *app) readGitHubToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	var out, diagnostics cappedBuffer
	stderr := termsafe.NewWriter(a.stderr)
	defer stderr.Flush()
	code, err := a.gh(ctx, []string{"auth", "token", "--hostname", "github.com"}, nil, &out, &diagnostics)
	value, valueErr := vault.ReadValue(bytes.NewReader(out.buffer.Bytes()))
	redacted := &secretWriter{w: stderr, value: []byte(value)}
	redacted.Write(diagnostics.buffer.Bytes())
	redacted.Flush()
	if diagnostics.overflow {
		fmt.Fprintf(a.stderr, "hib: gh diagnostics were truncated after %d KiB\n", maxGHOutput>>10)
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return "", errors.New("install gh and run gh auth login -h github.com, or use hib connect github --token")
	}
	if err != nil || code != 0 {
		return "", errors.New("couldn't read the token from gh: run gh auth login -h github.com first, or use --token")
	}
	if out.overflow {
		return "", fmt.Errorf("gh returned more than %d KiB: run gh auth login -h github.com first", maxGHOutput>>10)
	}
	return value, valueErr
}

const maxGHOutput = 64 << 10

type cappedBuffer struct {
	buffer   bytes.Buffer
	overflow bool
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := maxGHOutput + 2 - w.buffer.Len()
	if n > left {
		w.overflow = true
		p = p[:left]
	}
	w.buffer.Write(p)
	return n, nil
}

var commandWord = regexp.MustCompile(`^[a-z][a-z-]{0,15}$`)

// checkConnectArgs keeps a pasted token out of flag's errors, which quote invalid values, and out
// of "unknown command" mistakes.
func checkConnectArgs(args []string) error {
	const tokenArgument = "pass the token at the prompt or on stdin, not as an argument: arguments end up in shell history"
	positional := 0
	flags := true
	for _, arg := range args {
		if flags && arg == "--" {
			flags = false
			continue
		}
		if !flags || !strings.HasPrefix(arg, "-") || arg == "-" {
			positional++
			if positional > 1 || !commandWord.MatchString(arg) {
				return errors.New(tokenArgument)
			}
			continue
		}
		name, value, assigned := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		switch name {
		case "token", "from-gh":
			if assigned {
				if _, err := strconv.ParseBool(value); err != nil {
					return errors.New(tokenArgument)
				}
			}
		case "h", "help":
		default:
			return errors.New("unknown connect flag: use --from-gh or --token")
		}
	}
	return nil
}
