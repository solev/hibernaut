package hibd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

const connectUsage = "connect github [-source gh|token] [-json]"

func (a *App) connect(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "github" {
		fmt.Fprintf(a.Stderr, "usage: hibd %s\n", connectUsage)
		return 2
	}
	return a.connectGitHub(ctx, args[1:])
}

// connectGitHub stores the token on stdin as secret github once GitHub accepts it, saves the
// account, and writes the machine setup into running machines.
func (a *App) connectGitHub(ctx context.Context, args []string) int {
	fl := a.flags("connect github")
	source := fl.String("source", hibapi.GitHubFromGH, "how the token was given: gh or token")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, connectUsage); !ok {
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
	// The account is saved under the vault's lock, so two connects can't leave one's token with
	// the other's account.
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
	return hibapi.GitHub{Login: p.Login, ID: p.ID, Name: p.Name, Email: p.Email(), Source: p.Source,
		Scopes: user.Scopes, ScopesKnown: user.ScopesKnown}, nil
}

// checkGHTokenFree fails if a secret other than github sets GH_TOKEN in machines.
func checkGHTokenFree(f *vault.File) error {
	for _, s := range f.Secrets() {
		if s.Name != github.SecretName && strings.EqualFold(s.Env, github.Env) {
			return fmt.Errorf("secret %s already sets %s in machines. Remove it with hib secret rm %s, then connect again",
				s.Name, github.Env, s.Name)
		}
	}
	return nil
}

// refusedToken says GitHub refused a token from source, and how to get one that works.
func refusedToken(source string) error {
	if source == hibapi.GitHubFromToken {
		return fmt.Errorf("%w. Nothing was stored. Make a new token on github.com, then run hib connect github --token again",
			github.ErrUnauthorized)
	}
	return fmt.Errorf("%w. Nothing was stored. To fix it, run gh auth login -h github.com, then hib connect github --from-gh",
		github.ErrUnauthorized)
}

// needRoot fails unless hibd runs as root, saying how to run command.
func (a *App) needRoot(command string) error {
	if a.euid() != 0 {
		return fmt.Errorf("it reads real secret values, so run it as root: sudo hibd %s", command)
	}
	return nil
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
