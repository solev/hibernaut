package hibd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

const connectGitHubUsage = "connect github [-source gh|token] [-json]"

func (a *App) connect(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "github":
			return a.connectGitHub(ctx, args[1:])
		case "codex":
			return a.connectCodex(ctx, args[1:])
		}
	}
	fmt.Fprintf(a.Stderr, "usage: hibd %s\n       hibd %s\n", connectGitHubUsage, connectCodexUsage)
	return 2
}

func (a *App) connectGitHub(ctx context.Context, args []string) int {
	fl := a.flags("connect github")
	source := fl.String("source", hibapi.GitHubFromGH, "how the token was given: gh or token")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, connectGitHubUsage); !ok {
		return 2
	}
	if *source != hibapi.GitHubFromGH && *source != hibapi.GitHubFromToken {
		fmt.Fprintf(a.Stderr, "hibd connect github: -source must be gh or token, not %q\n", termsafe.String(*source))
		return 2
	}
	if err := a.needRoot("connect github"); err != nil {
		return a.fail(*asJSON, "connect github", err)
	}
	gh, err := a.storeGitHub(ctx, *source)
	if err != nil {
		return a.fail(*asJSON, "connect github", err)
	}
	if *asJSON {
		return a.reply(hibapi.Response{GitHub: &gh})
	}
	fmt.Fprintf(a.Stdout, "connected GitHub as %s\n", termsafe.String(gh.Login))
	if warning := gh.ScopeWarning(); warning != "" {
		fmt.Fprintf(a.Stderr, "warning: %s\n", termsafe.String(warning))
	}
	return 0
}

func (a *App) storeGitHub(ctx context.Context, source string) (hibapi.GitHub, error) {
	token, err := vault.ReadValue(a.Stdin)
	if err != nil {
		return hibapi.GitHub{}, err
	}
	if err := github.CheckToken(token); err != nil {
		return hibapi.GitHub{}, err
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return hibapi.GitHub{}, err
	}
	f, err := vault.Load(cfg.vaultPath)
	if err != nil {
		return hibapi.GitHub{}, err
	}
	if err := checkGHTokenFree(f); err != nil {
		return hibapi.GitHub{}, err
	}
	user, err := github.GetUser(ctx, a.githubClient(), a.githubAPI(), token)
	if errors.Is(err, github.ErrUnauthorized) {
		return hibapi.GitHub{}, refusedToken(source)
	}
	if err != nil {
		return hibapi.GitHub{}, fmt.Errorf("checking the token with GitHub: %w. Nothing was stored", err)
	}
	p := github.Profile{Login: user.Login, ID: user.ID, Name: user.Name, Source: source}

	err = vault.Update(cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, key vault.Key) error {
		if err := checkGHTokenFree(f); err != nil {
			return err
		}
		if _, err := f.Put(key, github.SecretName, github.Env, github.Sites(), token); err != nil {
			return err
		}
		if err := github.SaveProfile(cfg.githubProfile, p); err != nil {
			return fmt.Errorf("saving the account failed: %w. Nothing was stored", err)
		}
		return nil
	})
	if err != nil {
		return hibapi.GitHub{}, err
	}
	a.pushSetup(ctx, cfg, "GitHub connected")
	return p.Account(&user), nil
}

func checkGHTokenFree(f *vault.File) error {
	for _, s := range f.Secrets() {
		if s.Name != github.SecretName && strings.EqualFold(s.Env, github.Env) {
			return fmt.Errorf("secret %s already sets %s in machines. Remove it with hib secret rm %s, then connect again",
				s.Name, github.Env, s.Name)
		}
	}
	return nil
}

func refusedToken(source string) error {
	reconnect := hibapi.GitHub{Source: source}.Reconnect()
	if source == hibapi.GitHubFromToken {
		return fmt.Errorf("%w. Nothing was stored. Make a new token on github.com, then run %s again",
			github.ErrUnauthorized, reconnect)
	}
	return fmt.Errorf("%w. Nothing was stored. To fix it, run gh auth login -h github.com, then %s",
		github.ErrUnauthorized, reconnect)
}

func (a *App) githubAPI() string {
	if a.GitHubAPI != "" {
		return a.GitHubAPI
	}
	return github.APIBase
}

func (a *App) githubClient() *http.Client {
	if a.GitHubClient != nil {
		return a.GitHubClient
	}
	return github.NewClient()
}

func rebindGitHub(cfg config) (bool, error) {
	p, err := github.LoadProfile(cfg.githubProfile)
	if err != nil || p == nil {
		return false, err
	}
	f, err := vault.Load(cfg.vaultPath)
	if err != nil || !githubSitesOutdated(f) {
		return false, err
	}
	changed := false
	err = vault.Update(cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, key vault.Key) error {
		if !githubSitesOutdated(f) {
			return nil
		}
		revealed, err := f.Open(key)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(revealed, func(r vault.Revealed) bool { return r.Name == github.SecretName })
		s := revealed[i]
		if _, err := f.Put(key, s.Name, s.Env, github.Sites(), s.Value); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

func githubSitesOutdated(f *vault.File) bool {
	secrets := f.Secrets()
	i := slices.IndexFunc(secrets, func(s vault.Secret) bool { return s.Name == github.SecretName })
	return i >= 0 && !slices.Equal(secrets[i].Sites, slices.Sorted(slices.Values(github.Sites())))
}
