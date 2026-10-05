package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

func (a *app) secretAdd(ctx context.Context, cfg *config, name, env, bind string) int {
	sites := vault.SplitSites(bind)
	if err := vault.CheckSecret(name, env, sites); err != nil {
		return a.fail(err)
	}
	value, err := a.readSecretValue(fmt.Sprintf("Value for %s: ", name))
	if err != nil {
		return a.fail(err)
	}
	r, err := a.remoteSudo(ctx, cfg, shortTimeout, callOptions{stdin: []byte(value)}, "secret", "add", name, "--env", env, "--bind", strings.Join(sites, ","), "--json")
	if err != nil {
		return a.fail(err)
	}
	if r.Secret == nil {
		return a.fail(errors.New("hibd didn't say which secret it stored"))
	}
	fmt.Fprintf(a.stdout, "stored %s as %s for %s\n", safe(r.Secret.Name), safe(r.Secret.Env), safe(strings.Join(r.Secret.Sites, ",")))
	return 0
}

func (a *app) secretLS(ctx context.Context, cfg *config, _ []string) int {
	r, err := a.remote(ctx, cfg, shortTimeout, "secret", "ls", "--json")
	if err != nil {
		return a.fail(err)
	}
	hibapi.PrintSecrets(a.stdout, r.Secrets)
	return 0
}

func (a *app) secretRM(ctx context.Context, cfg *config, args []string) int {
	if err := hibapi.CheckName(args[0]); err != nil {
		return a.fail(err)
	}
	if _, err := a.remoteSudo(ctx, cfg, shortTimeout, callOptions{}, "secret", "rm", args[0], "--json"); err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "removed %s\n", args[0])
	return 0
}

func (a *app) log(ctx context.Context, cfg *config, n int) int {
	if n < 0 {
		return a.mistake("--n must be non-negative")
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "log", "--n", fmt.Sprint(n), "--json")
	if err != nil {
		return a.fail(err)
	}
	if r.Corrupt > 0 {
		fmt.Fprintf(a.stderr, "hib: skipped %d corrupt audit log line(s)\n", r.Corrupt)
	}
	printLogEntries(a.stdout, r.Logs)
	return 0
}

func (a *app) readSecretValue(prompt string) (string, error) {
	if file, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(a.stderr, termsafe.String(prompt))
		data, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(a.stderr)
		if err != nil {
			return "", err
		}
		return vault.ReadValue(bytes.NewReader(data))
	}
	return vault.ReadValue(a.stdin)
}

func printLogEntries(w io.Writer, entries []hibapi.LogEntry) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tMACHINE\tSITE\tMETHOD\tPATH\tSECRETS")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Time.Local().Format(time.RFC3339),
			termsafe.String(e.Machine),
			termsafe.String(e.Site),
			termsafe.String(e.Method),
			termsafe.String(e.Path),
			termsafe.String(strings.Join(e.Secrets, ",")))
	}
	tw.Flush()
}
