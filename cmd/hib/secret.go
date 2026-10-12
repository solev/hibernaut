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
	if env == "" {
		return a.mistake("missing --env VAR")
	}
	sites := vault.SplitSites(bind)
	if err := vault.CheckSecret(name, env, sites); err != nil {
		return a.mistake(err.Error())
	}
	value, err := a.readSecretValue(fmt.Sprintf("Value for %s: ", name))
	if err != nil {
		return a.fail(fmt.Errorf("%w: type it at the prompt or pipe it on stdin", err))
	}
	a, flush := a.hidingSecret(value)
	defer flush()
	r, err := a.remoteSudo(ctx, cfg, shortTimeout, callOptions{secret: []byte(value)}, "secret", "add", name, "--env", env, "--bind", strings.Join(sites, ","), "--json")
	if err != nil {
		return a.failWaiting(ctx, err, fmt.Sprintf("The host may still be storing %s: check hib secret ls", name))
	}
	if r.Secret == nil {
		return a.fail(errors.New("hibd didn't say which secret it stored: run hib secret ls to see your secrets"))
	}
	fmt.Fprintf(a.stdout, "stored %s as %s for %s\n", safe(r.Secret.Name), safe(r.Secret.Env), safe(strings.Join(r.Secret.Sites, ",")))
	return 0
}

func (a *app) secretLS(ctx context.Context, cfg *config, _ []string) int {
	r, err := a.remote(ctx, cfg, shortTimeout, "secret", "ls", "--json")
	if err != nil {
		return a.failWaiting(ctx, err, "")
	}
	if a.json {
		return a.reply(hibapi.Response{Secrets: r.Secrets}, nil)
	}
	hibapi.PrintSecrets(a.stdout, r.Secrets)
	return 0
}

func (a *app) secretRM(ctx context.Context, cfg *config, args []string) int {
	if !a.checkNames(args[0]) {
		return 2
	}
	if _, err := a.remoteSudo(ctx, cfg, shortTimeout, callOptions{}, "secret", "rm", args[0], "--json"); err != nil {
		return a.failWaiting(ctx, err, fmt.Sprintf("The host may still be removing %s: check hib secret ls", args[0]))
	}
	fmt.Fprintf(a.stdout, "removed %s\n", args[0])
	return 0
}

// log prints the audit log: the secret log, or with links the link log.
func (a *app) log(ctx context.Context, cfg *config, n int, links bool) int {
	if n < 0 {
		return a.mistake("--n must be non-negative")
	}
	if links {
		return a.logLinks(ctx, cfg, n)
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "log", "--n", fmt.Sprint(n), "--json")
	if err != nil {
		return a.failWaiting(ctx, err, "")
	}
	if a.json {
		return a.reply(hibapi.Response{Logs: r.Logs, Corrupt: r.Corrupt}, nil)
	}
	if r.Corrupt > 0 {
		fmt.Fprintf(a.stderr, "hib: skipped %d corrupt audit log line(s)\n", r.Corrupt)
	}
	printLogEntries(a.stdout, r.Logs)
	return 0
}

// logLinks prints the newest n entries of the link log, which only a hibd with the port command
// keeps.
func (a *app) logLinks(ctx context.Context, cfg *config, n int) int {
	switch err := a.needPorts(ctx, cfg); {
	case errors.Is(err, errNoPorts):
		return a.fail(errors.New("this host's hibd is too old for hib log --links. Upgrade it"))
	case err != nil:
		return a.failWaiting(ctx, err, "")
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "log", "--n", fmt.Sprint(n), "--links", "--json")
	if err != nil {
		return a.failWaiting(ctx, err, "")
	}
	links := r.Links
	if links == nil {
		links = []hibapi.LinkEntry{}
	}
	if a.json {
		return a.reply(hibapi.Response{Links: links, Corrupt: r.Corrupt}, nil)
	}
	if r.Corrupt > 0 {
		fmt.Fprintf(a.stderr, "hib: skipped %d corrupt link log line(s)\n", r.Corrupt)
	}
	hibapi.PrintLinkEntries(a.stdout, links, linkTime)
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
			termsafe.Line(e.Machine),
			termsafe.Line(e.Site),
			termsafe.Line(e.Method),
			termsafe.Line(e.Path),
			termsafe.Line(strings.Join(e.Secrets, ",")))
	}
	tw.Flush()
}

// linkTime shows the time hibd sends in local time, as the secret log does, and anything else
// as it came.
func linkTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format(time.RFC3339)
}
