package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

const connectUsage = "connect github (--from-gh | --token)"

func (a *app) connect(ctx context.Context, cfg *config, args []string) int {
	if err := checkConnectArgs(args); err != nil {
		a.fail(err)
		fmt.Fprintf(a.stderr, "usage: hib %s\n", connectUsage)
		return 2
	}
	fl := a.flags("connect")
	fromGH := fl.Bool("from-gh", false, "use this laptop's gh login")
	token := fl.Bool("token", false, "read a token from a hidden prompt or stdin")
	providers, ok := a.parse(fl, args, 1, connectUsage)
	if !ok {
		return 2
	}
	if providers[0] != "github" {
		a.fail(errors.New("only GitHub is supported: run hib connect github"))
		fmt.Fprintf(a.stderr, "usage: hib %s\n", connectUsage)
		return 2
	}
	if *fromGH == *token {
		fmt.Fprintf(a.stderr, "hib connect: choose exactly one of --from-gh and --token\nusage: hib %s\n", connectUsage)
		return 2
	}
	source := hibapi.GitHubFromToken
	var value string
	var err error
	if *fromGH {
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
	r, err := a.remoteSudo(ctx, cfg, shortTimeout, []byte(value), "connect", "github", "--source", source, "--json")
	if err != nil {
		return a.fail(err)
	}
	if r.GitHub == nil {
		return a.fail(errors.New("hibd didn't say which account it connected"))
	}
	g := r.GitHub
	who := termsafe.String(g.Login)
	if g.Name != "" && g.Name != g.Login {
		who += " (" + termsafe.String(g.Name) + ")"
	}
	fmt.Fprintf(a.stdout, "connected GitHub as %s; machines commit as %s\n", who, termsafe.String(g.Email))
	if warning := g.ScopeWarning(); warning != "" {
		fmt.Fprintf(a.stderr, "hib: warning: %s\n", termsafe.String(warning))
	}
	return 0
}

func (a *app) readGitHubToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, shortTimeout)
	defer cancel()
	var out, diagnostics tokenOutput
	stderr := termsafe.NewWriter(a.stderr)
	defer stderr.Flush()
	code, err := a.gh(ctx, []string{"auth", "token", "--hostname", "github.com"}, nil, &out, &diagnostics)
	value, valueErr := vault.ReadValue(bytes.NewReader(out.buffer.Bytes()))
	redacted := &secretWriter{w: stderr, value: []byte(value)}
	redacted.Write(diagnostics.buffer.Bytes())
	redacted.Flush()
	if diagnostics.overflow {
		fmt.Fprintln(a.stderr, "hib: gh diagnostics were truncated after 64 KiB")
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return "", errors.New("install gh and run gh auth login -h github.com, or use hib connect github --token")
	}
	if err != nil || code != 0 {
		return "", errors.New("couldn't read the token from gh: run gh auth login -h github.com first, or use --token")
	}
	if out.overflow {
		return "", errors.New("gh returned more than 64 KiB: run gh auth login -h github.com first")
	}
	return value, valueErr
}

// tokenOutput drains gh's output while keeping at most 64 KiB and a trailing CRLF.
type tokenOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (w *tokenOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := (64 << 10) + 2 - w.buffer.Len()
	if n > left {
		w.overflow = true
		p = p[:left]
	}
	w.buffer.Write(p)
	return n, nil
}

// checkConnectArgs keeps a pasted token out of flag's errors, which quote invalid values.
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
			if positional > 1 {
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
