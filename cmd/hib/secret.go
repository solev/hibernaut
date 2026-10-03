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

func (a *app) secret(ctx context.Context, cfg *config, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.stderr, "usage: hib secret add NAME --env VAR --bind SITE[,SITE...] | secret ls | secret rm NAME")
		return 2
	}
	switch args[0] {
	case "add":
		return a.secretAdd(ctx, cfg, args[1:])
	case "ls":
		return a.secretLS(ctx, cfg, args[1:])
	case "rm":
		return a.secretRM(ctx, cfg, args[1:])
	}
	fmt.Fprintf(a.stderr, "hib secret: unknown command %q\n", args[0])
	return 2
}

func (a *app) secretAdd(ctx context.Context, cfg *config, args []string) int {
	fl := a.flags("secret add")
	env := fl.String("env", "", "environment variable to set in machines")
	bind := fl.String("bind", "", "comma-separated bound sites")
	names, ok := a.parse(fl, args, 1, "secret add NAME --env VAR --bind SITE[,SITE...]")
	if !ok {
		return 2
	}
	sites := splitSites(*bind)
	if err := vault.CheckSecret(names[0], *env, sites); err != nil {
		return a.fail(err)
	}
	value, err := a.readSecretValue(names[0])
	if err != nil {
		return a.fail(err)
	}
	r, err := a.remoteSudo(ctx, cfg, shortTimeout, strings.NewReader(value), "secret", "add", names[0], "--env", *env, "--bind", strings.Join(sites, ","), "--json")
	if err != nil {
		return a.fail(err)
	}
	if r.Secret == nil {
		return a.fail(errors.New("hibd didn't say which secret it stored"))
	}
	fmt.Fprintf(a.stdout, "stored %s as %s for %s\n", safe(r.Secret.Name), safe(r.Secret.Env), safe(strings.Join(r.Secret.Sites, ",")))
	return 0
}

func (a *app) secretLS(ctx context.Context, cfg *config, args []string) int {
	if _, ok := a.parse(a.flags("secret ls"), args, 0, "secret ls"); !ok {
		return 2
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "secret", "ls", "--json")
	if err != nil {
		return a.fail(err)
	}
	printSecrets(a.stdout, r.Secrets)
	return 0
}

func (a *app) secretRM(ctx context.Context, cfg *config, args []string) int {
	names, ok := a.parse(a.flags("secret rm"), args, 1, "secret rm NAME")
	if !ok {
		return 2
	}
	if err := vault.CheckName(names[0]); err != nil {
		return a.fail(err)
	}
	if _, err := a.remoteSudo(ctx, cfg, shortTimeout, nil, "secret", "rm", names[0], "--json"); err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "removed %s\n", names[0])
	return 0
}

func (a *app) log(ctx context.Context, cfg *config, args []string) int {
	fl := a.flags("log")
	n := fl.Int("n", 50, "number of newest entries to show")
	if _, ok := a.parse(fl, args, 0, "log [-n N]"); !ok {
		return 2
	}
	if *n < 0 {
		fmt.Fprintln(a.stderr, "hib log: -n must be non-negative")
		return 2
	}
	r, err := a.remote(ctx, cfg, shortTimeout, "log", "--n", fmt.Sprint(*n), "--json")
	if err != nil {
		return a.fail(err)
	}
	if r.Corrupt > 0 {
		fmt.Fprintf(a.stderr, "hib: skipped %d corrupt audit log line(s)\n", r.Corrupt)
	}
	printLogEntries(a.stdout, r.Logs)
	return 0
}

func (a *app) readSecretValue(name string) (string, error) {
	if file, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprintf(a.stderr, "Value for %s: ", name)
		data, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(a.stderr)
		if err != nil {
			return "", err
		}
		return cleanSecretValue(data)
	}
	data, err := io.ReadAll(io.LimitReader(a.stdin, vault.MaxValue+1))
	if err != nil {
		return "", err
	}
	return cleanSecretValue(data)
}

func cleanSecretValue(data []byte) (string, error) {
	if len(data) > vault.MaxValue {
		return "", fmt.Errorf("secret value is bigger than 64 KiB")
	}
	data = trimOneNewline(data)
	if len(data) == 0 {
		return "", errors.New("secret value is empty")
	}
	return string(data), nil
}

func trimOneNewline(data []byte) []byte {
	if len(data) >= 2 && bytes.Equal(data[len(data)-2:], []byte("\r\n")) {
		return data[:len(data)-2]
	}
	if len(data) > 0 && data[len(data)-1] == '\n' {
		return data[:len(data)-1]
	}
	return data
}

func splitSites(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func printSecrets(w io.Writer, secrets []hibapi.Secret) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tENV\tSITES\tPLACEHOLDER")
	for _, s := range secrets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", safe(s.Name), safe(s.Env), safe(strings.Join(s.Sites, ",")), safe(s.Placeholder))
	}
	tw.Flush()
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
