package hibd

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/hibapi"
)

const codexRefreshUsage = "codex refresh [-force] [-json]"

// runCodex runs hibd codex's subcommands.
func (a *App) runCodex(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "refresh" {
		return a.codexRefresh(ctx, args[1:])
	}
	fmt.Fprintf(a.Stderr, "usage: hibd %s\n", codexRefreshUsage)
	return 2
}

// codexRefresh refreshes the host's Codex login if it's due, or with -force anyway, writes the
// machine setup into running machines if the login file changed, and reports the login.
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

// refreshCodex calls EnsureFresh for reason, writes the setup into running machines if the
// login file changed, even if the refresh then failed, and returns the profile.
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

// codexLoginFileOf returns the placeholder login file machines get from store.
func codexLoginFileOf(store *codex.Store) ([]byte, error) {
	pub, err := store.Public()
	if err != nil {
		return nil, err
	}
	return codex.RenderAuthFile(pub)
}

// codexRefreshError says what to do about err from the Store.
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
