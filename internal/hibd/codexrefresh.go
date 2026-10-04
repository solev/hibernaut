package hibd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

const codexRefreshUsage = "codex refresh [-force] [-json]"

func (a *App) runCodex(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "refresh" {
		return a.codexRefresh(ctx, args[1:])
	}
	fmt.Fprintf(a.Stderr, "usage: hibd %s\n", codexRefreshUsage)
	return 2
}

func (a *App) codexRefresh(ctx context.Context, args []string) int {
	fl := a.flags("codex refresh")
	force := fl.Bool("force", false, "refresh even if it isn't due, or the login is stuck")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, codexRefreshUsage); !ok {
		return 2
	}
	if err := a.needRoot("codex refresh"); err != nil {
		return a.fail(*asJSON, "codex refresh", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "codex refresh", err)
	}
	reason := codex.Reason{Kind: codex.Background}
	if *force {
		reason.Kind = codex.Force
	}
	p, err := a.refreshCodex(ctx, cfg, reason)
	if err != nil {
		return a.fail(*asJSON, "codex refresh", err)
	}
	c := p.API()
	if *asJSON {
		return a.reply(hibapi.Response{Codex: &c})
	}
	hibapi.PrintCodex(a.Stdout, c)
	if c.Health != hibapi.CodexOK {
		return 1
	}
	return 0
}

func (a *App) refreshCodex(ctx context.Context, cfg config, reason codex.Reason) (codex.Profile, error) {
	store := a.codexStore(cfg, a.notef)
	before, err := codexLoginFileOf(store)
	if err != nil {
		return codex.Profile{}, codexRefreshError(err)
	}
	_, refreshErr := store.EnsureFresh(ctx, reason)
	if after, err := codexLoginFileOf(store); err == nil && !bytes.Equal(before, after) {
		a.pushSetup(ctx, cfg, "Codex refreshed")
	}
	if refreshErr != nil {
		return codex.Profile{}, codexRefreshError(refreshErr)
	}
	p, err := store.Load()
	if err != nil {
		return codex.Profile{}, codexRefreshError(err)
	}
	return p, nil
}

func codexLoginFileOf(store *codex.Store) ([]byte, error) {
	pub, err := store.Public()
	if err != nil {
		return nil, err
	}
	return codex.RenderAuthFile(pub)
}

func codexRefreshError(err error) error {
	for _, known := range []error{codex.ErrNotConnected, codex.ErrDead, codex.ErrStuck} {
		if errors.Is(err, known) {
			return fmt.Errorf("%w: run hib connect codex", known)
		}
	}
	if errors.Is(err, codex.ErrUnavailable) {
		return fmt.Errorf("%w: try again in a few minutes", codex.ErrUnavailable)
	}
	return err
}

type codexRefresher struct {
	store *codex.Store
	logf  func(format string, args ...any)

	seen codex.Profile
	said string
}

func (r *codexRefresher) run(ctx context.Context, every time.Duration, awake func() error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if awake() == nil {
			r.look(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *codexRefresher) look(ctx context.Context) {
	_, refreshErr := r.store.EnsureFresh(ctx, codex.Reason{Kind: codex.Background})
	if ctx.Err() != nil {
		return
	}
	p, err := r.store.Load()
	if err == nil {
		r.logChanges(p)
	}
	if now := codexHealthLine(p, err, refreshErr); now != r.said {
		if now != "" {
			r.logf("%s", now)
		}
		r.said = now
	}
}

func (r *codexRefresher) logChanges(p codex.Profile) {
	if !p.ConnectedAt.Equal(r.seen.ConnectedAt) {
		r.logf("connected as %s, on the %s plan", termsafe.String(p.Email), termsafe.String(p.Plan))
	}
	if !p.LastRefresh.Equal(r.seen.LastRefresh) && p.LastRefresh.After(p.ConnectedAt) {
		r.logf("refreshed the login. The token expires at %s", p.Expires.UTC().Format(time.RFC3339))
	}
	r.seen = p
}

func codexHealthLine(p codex.Profile, err, refreshErr error) string {
	switch {
	case errors.Is(err, codex.ErrNotConnected):
		return ""
	case err != nil:
		return "reading the login failed: " + termsafe.String(err.Error())
	case p.Health != codex.HealthOK:
		problem := p.Problem
		if problem == "" {
			problem = "run hib connect codex"
		}
		return fmt.Sprintf("the login is %s: %s", p.Health, termsafe.String(problem))
	case p.Failures > 0:
		return "refreshing failed. Trying again after " + p.RetryAt.UTC().Format(time.RFC3339)
	case refreshErr != nil:
		return "refreshing failed: " + termsafe.String(refreshErr.Error())
	}
	return "the login is ok"
}
