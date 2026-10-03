package hibd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/vault"
	"github.com/solev/hibernaut/internal/version"
)

const (
	// githubAPIHost is the site a GitHub token must be bound to for status to check it.
	githubAPIHost = "api.github.com"
	// maxChecks is how many secrets status checks at once.
	maxChecks = 8
)

// status reports hibd's version, whether E2B is up, the connected GitHub account, and each
// secret's health. Without -json, it exits 1 if something is broken.
func (a *App) status(ctx context.Context, args []string) int {
	fl := a.flags("status")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "status [-json]"); !ok {
		return 2
	}
	if err := a.needRoot("status"); err != nil {
		return a.fail(*asJSON, "status", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "status", err)
	}
	s, err := a.hostStatus(ctx, cfg)
	if err != nil {
		return a.fail(*asJSON, "status", err)
	}
	if *asJSON {
		return a.reply(hibapi.Response{Status: &s})
	}
	hibapi.PrintStatus(a.Stdout, "", s)
	if !s.OK() {
		return 1
	}
	return 0
}

func (a *App) hostStatus(ctx context.Context, cfg config) (hibapi.Status, error) {
	profile, err := github.LoadProfile(cfg.githubProfile)
	if err != nil {
		return hibapi.Status{}, err
	}
	secrets, err := loadStatusSecrets(cfg)
	if err != nil {
		return hibapi.Status{}, err
	}
	var e2b hibapi.Health
	var wg sync.WaitGroup
	wg.Go(func() { e2b = checkE2B(ctx, cfg.healthURLs) })
	health, githubUser := a.checkSecrets(ctx, secrets, profile)
	wg.Wait()

	s := hibapi.Status{Version: version.Version, E2B: e2b, Secrets: health}
	if profile != nil && slices.ContainsFunc(secrets, func(r vault.Revealed) bool { return r.Name == github.SecretName }) {
		s.GitHub = &hibapi.GitHub{Login: profile.Login, ID: profile.ID, Name: profile.Name, Email: profile.Email(),
			Source: profile.Source}
		if githubUser != nil {
			s.GitHub.Scopes, s.GitHub.ScopesKnown = githubUser.Scopes, githubUser.ScopesKnown
		}
	}
	return s, nil
}

// loadStatusSecrets returns every secret in the vault, sorted by name. Only those status checks
// have their values.
func loadStatusSecrets(cfg config) ([]vault.Revealed, error) {
	f, err := vault.Load(cfg.vaultPath)
	if err != nil {
		return nil, err
	}
	secrets := f.Secrets()
	if !slices.ContainsFunc(secrets, checked) {
		out := make([]vault.Revealed, len(secrets))
		for i, s := range secrets {
			out[i] = vault.Revealed{Secret: s}
		}
		return out, nil
	}
	key, err := vault.LoadKey(cfg.vaultKeyPath)
	if err != nil {
		return nil, err
	}
	revealed, err := f.Open(key)
	if err != nil {
		return nil, err
	}
	for i := range revealed {
		if !checked(revealed[i].Secret) {
			revealed[i].Value = ""
		}
	}
	return revealed, nil
}

// checked reports whether status checks s: whether the proxy sends it to GitHub's API.
func checked(s vault.Secret) bool {
	return slices.ContainsFunc(s.Sites, func(site string) bool {
		if rest, ok := strings.CutPrefix(site, "*."); ok {
			return strings.HasSuffix(githubAPIHost, "."+rest)
		}
		return site == githubAPIHost
	})
}

// checkSecrets checks each secret bound to GitHub's API with GET /user, at most maxChecks at
// once. It also returns what GitHub said about secret github, if it accepted it.
func (a *App) checkSecrets(ctx context.Context, secrets []vault.Revealed, profile *github.Profile) ([]hibapi.SecretHealth, *github.User) {
	health := make([]hibapi.SecretHealth, len(secrets))
	var githubUser *github.User
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxChecks)
	for i, s := range secrets {
		health[i] = hibapi.SecretHealth{Name: s.Name, Sites: slices.Clone(s.Sites)}
		if !checked(s.Secret) {
			health[i].State, health[i].Detail = hibapi.SecretUnchecked, "no check for these sites"
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			user, err := github.GetUser(ctx, a.githubClient(), a.githubAPI(), s.Value)
			switch {
			case err == nil:
				health[i].State = hibapi.SecretOK
				if s.Name == github.SecretName {
					mu.Lock()
					githubUser = &user
					mu.Unlock()
				}
			case errors.Is(err, github.ErrUnauthorized):
				health[i].State, health[i].Detail = hibapi.SecretRevoked, revokedDetail(s.Name, profile)
			default:
				health[i].State, health[i].Detail = hibapi.SecretFailed, err.Error()
			}
		})
	}
	wg.Wait()
	return health, githubUser
}

// revokedDetail says how to replace secret name, which its site refused.
func revokedDetail(name string, profile *github.Profile) string {
	if name != github.SecretName {
		return fmt.Sprintf("revoked: run hib secret add %s again", name)
	}
	g := hibapi.GitHub{}
	if profile != nil {
		g.Source = profile.Source
	}
	return "revoked: run " + g.Reconnect() + " again"
}

// checkE2B asks each of E2B's health URLs once, all at once. E2B is down if any fails.
func checkE2B(ctx context.Context, urls []string) hibapi.Health {
	client := &http.Client{Timeout: 5 * time.Second}
	errs := make([]error, len(urls))
	var wg sync.WaitGroup
	for i, url := range urls {
		wg.Go(func() { errs[i] = healthy(ctx, client, url) })
	}
	wg.Wait()
	var failed []string
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return hibapi.Health{Detail: strings.Join(failed, ", ")}
	}
	return hibapi.Health{OK: true}
}
