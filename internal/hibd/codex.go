package hibd

import (
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/termsafe"
)

// codexStore returns the Store that keeps the host's Codex login, with cfg's paths and the
// App's test seams. Every part of hibd that needs the login uses one from here.
func (a *App) codexStore(cfg config, logf func(format string, args ...any)) *codex.Store {
	return codex.NewStore(codex.Config{
		StateDir:  cfg.stateDir,
		VaultPath: cfg.vaultPath,
		KeyPath:   cfg.vaultKeyPath,
		TokenURL:  a.CodexTokenURL,
		Client:    a.CodexClient,
		Now:       time.Now,
		Logf:      logf,
	})
}

// warnf prints a warning on stderr, made safe for a terminal.
func (a *App) warnf(format string, args ...any) {
	fmt.Fprintf(a.Stderr, "warning: %s\n", termsafe.String(fmt.Sprintf(format, args...)))
}
