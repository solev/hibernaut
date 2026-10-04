package hibd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

const (
	connectGitHubUsage = "connect github [-source gh|token|device] [-json]"
	githubLoginLock    = "github-login.lock"
)

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
	source := fl.String("source", hibapi.GitHubFromGH, "how the token is given: gh or token on stdin, or device to log in")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, connectGitHubUsage); !ok {
		return 2
	}
	if *source != hibapi.GitHubFromGH && *source != hibapi.GitHubFromToken && *source != hibapi.GitHubFromDevice {
		fmt.Fprintf(a.Stderr, "hibd connect github: -source must be gh, token or device, not %q\n", termsafe.String(*source))
		return 2
	}
	if err := a.needRoot("connect github"); err != nil {
		return a.fail(*asJSON, "connect github", err)
	}
	gh, err := a.loginGitHub(ctx, *source)
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

func (a *App) loginGitHub(ctx context.Context, source string) (hibapi.GitHub, error) {
	var token string
	if source != hibapi.GitHubFromDevice {
		var err error
		if token, err = vault.ReadValue(a.Stdin); err != nil {
			return hibapi.GitHub{}, err
		}
		if err := github.CheckToken(token); err != nil {
			return hibapi.GitHub{}, err
		}
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return hibapi.GitHub{}, err
	}
	unlock, ok, err := lockGitHubLogins(cfg.stateDir)
	if err != nil {
		return hibapi.GitHub{}, err
	}
	if !ok {
		return hibapi.GitHub{}, errors.New("another hib connect github is running: finish it, or stop it, then try again")
	}
	defer unlock()
	f, err := vault.Load(cfg.vaultPath)
	if err != nil {
		return hibapi.GitHub{}, err
	}
	if err := checkGHTokenFree(f); err != nil {
		return hibapi.GitHub{}, err
	}
	if source == hibapi.GitHubFromDevice {
		if token, err = a.githubDeviceLogin(ctx); err != nil {
			return hibapi.GitHub{}, err
		}
	}
	return a.storeGitHub(ctx, cfg, source, token)
}

func lockGitHubLogins(stateDir string) (unlock func(), ok bool, err error) {
	return tryLock(stateDir, githubLoginLock, "GitHub login")
}

func (a *App) githubDeviceLogin(ctx context.Context) (string, error) {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if stdin := a.Stdin; stdin != nil {
		// Nothing waits for this: hib ends stdin only after hibd exits.
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			cancel()
		}()
	}
	flow := a.GitHubDeviceFlow
	if flow == nil {
		flow = &github.DeviceFlow{ClientID: github.ClientID}
	}
	token, err := flow.Login(waitCtx, func(p github.DevicePrompt) {
		fmt.Fprint(a.Stderr, termsafe.String(hibapi.GitHubDevicePrompt(p.UserCode, p.ExpiresIn)+"Waiting for GitHub. Press Ctrl-C to cancel.\n"))
	})
	switch {
	case err == nil:
		return token, nil
	case waitCtx.Err() != nil:
		return "", errors.New("the GitHub login was cancelled. Nothing was stored")
	case errors.Is(err, github.ErrCodeExpired):
		return "", fmt.Errorf("%w. Run hib connect github again for a new code", err)
	case errors.Is(err, github.ErrAccessDenied):
		return "", fmt.Errorf("%w. Nothing was stored", github.ErrAccessDenied)
	case errors.Is(err, github.ErrAppRefused):
		return "", errors.New("GitHub refused the Hibernaut OAuth App. Its owner must turn on Device Flow at " +
			"github.com/settings/developers. Until then, run hib connect github --from-gh or --token")
	case errors.Is(err, github.ErrNoClientID):
		return "", errors.New("this hibd has no GitHub OAuth App to log in to. Run hib connect github --from-gh or --token")
	case errors.Is(err, github.ErrExpiringToken):
		return "", fmt.Errorf("%w, and hibd can't refresh tokens. Turn off expiring tokens for the Hibernaut OAuth App, "+
			"then run hib connect github again", github.ErrExpiringToken)
	}
	return "", fmt.Errorf("the GitHub login failed: %w. Nothing was stored", err)
}

func (a *App) storeGitHub(ctx context.Context, cfg config, source, token string) (hibapi.GitHub, error) {
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
	switch source {
	case hibapi.GitHubFromDevice:
		return fmt.Errorf("%w. Nothing was stored. Run %s again", github.ErrUnauthorized, reconnect)
	case hibapi.GitHubFromToken:
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
