package hibd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

func (a *App) disconnect(ctx context.Context, args []string) int {
	if len(args) == 0 || (args[0] != "codex" && args[0] != "github") {
		fmt.Fprintln(a.Stderr, "usage: hibd disconnect codex|github [-json]")
		return 2
	}
	provider := args[0]
	cmd := "disconnect " + provider
	fl := a.flags(cmd)
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args[1:], 0, cmd+" [-json]"); !ok {
		return 2
	}
	if err := a.needRoot(cmd); err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	removed, err := disconnectLogin(ctx, cfg, provider)
	if err != nil {
		return a.fail(*asJSON, cmd, err)
	}
	label, warning := "GitHub", "Machines keep placeholders. GitHub in them stops working until you run hib connect github again. "+
		"Revoke the login itself in GitHub's authorized OAuth Apps or the token's page."
	if provider == "codex" {
		label, warning = "Codex", "Machines keep placeholders in ~/.codex/auth.json. Codex in them stops working until you run hib connect codex again. "+
			"Revoke the login itself in ChatGPT's settings."
	}
	if removed {
		fmt.Fprintf(a.Stderr, "removed the %s login from this host. %s\n", label, warning)
	} else {
		fmt.Fprintf(a.Stderr, "%s is not connected on this host. %s\n", label, warning)
	}
	if *asJSON {
		return a.reply(hibapi.Response{})
	}
	return 0
}

func disconnectLogin(ctx context.Context, cfg config, provider string) (bool, error) {
	lock, what := githubLoginLock, "GitHub login"
	names, files := []string{github.SecretName}, []string{github.ProfileFile}
	if provider == "codex" {
		lock, what = codexLoginLock, "codex login"
		names = []string{codex.AccessName, codex.RefreshName, codex.IDName}
		files = []string{codex.ProfileFile, codex.RetiredFile}
	}
	unlock, ok, err := tryLock(cfg.stateDir, lock, what)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("a %s login or disconnect is in progress. Finish it or stop it, then run hib disconnect %s again", provider, provider)
	}
	defer unlock()
	if provider == "codex" {
		unlock, err := waitLock(ctx, cfg.stateDir, codex.LockFile, "Codex refresh")
		if err != nil {
			return false, fmt.Errorf("waiting for the Codex refresh lock: %w. Finish the refresh, then run hib disconnect codex again", err)
		}
		defer unlock()
	}
	removed := false
	if err := vault.UpdateContext(ctx, cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, _ vault.Key) error {
		for _, name := range names {
			err := f.Remove(name)
			if err != nil && !errors.Is(err, vault.ErrNotFound) {
				return err
			}
			removed = removed || err == nil
		}
		return nil
	}); err != nil {
		return false, err
	}
	var failures []error
	for _, file := range files {
		err := os.Remove(filepath.Join(cfg.stateDir, file))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("removing %s: %w", termsafe.Line(file), err))
		}
		removed = removed || err == nil
	}
	if err := errors.Join(failures...); err != nil {
		return removed, fmt.Errorf("%w. Run hib disconnect %s again to finish removing its files", err, provider)
	}
	return removed, nil
}
