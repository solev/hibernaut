package hibd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

func (a *App) secret(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, "usage: hibd secret add NAME -env VAR -bind SITE[,SITE...] [-json] | secret ls [-json] | secret rm NAME [-json]")
		return 2
	}
	switch args[0] {
	case "add":
		return a.secretAdd(ctx, args[1:])
	case "ls":
		return a.secretLS(args[1:])
	case "rm":
		return a.secretRM(ctx, args[1:])
	}
	fmt.Fprintf(a.Stderr, "hibd secret: unknown command %q\n", args[0])
	return 2
}

func (a *App) secretAdd(ctx context.Context, args []string) int {
	fl := a.flags("secret add")
	env := fl.String("env", "", "environment variable to set in machines")
	bind := fl.String("bind", "", "comma-separated bound sites")
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "secret add NAME -env VAR -bind SITE[,SITE...] [-json]")
	if !ok {
		return 2
	}
	sites := vault.SplitSites(*bind)
	if err := vault.CheckSecret(names[0], *env, sites); err != nil {
		return a.fail(*asJSON, "secret add", err)
	}
	value, err := vault.ReadValue(a.Stdin)
	if err != nil {
		return a.fail(*asJSON, "secret add", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "secret add", err)
	}
	var saved vault.Secret
	err = vault.Update(cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, key vault.Key) error {
		var err error
		saved, err = f.Put(key, names[0], *env, sites, value)
		return err
	})
	if err != nil {
		return a.fail(*asJSON, "secret add", sudoHint(err))
	}
	a.pushSecrets(ctx, cfg)
	secret := apiSecret(saved)
	if *asJSON {
		return a.reply(hibapi.Response{Secret: &secret})
	}
	hibapi.PrintSecrets(a.Stdout, []hibapi.Secret{secret}, "")
	return 0
}

func (a *App) secretRM(ctx context.Context, args []string) int {
	fl := a.flags("secret rm")
	asJSON := fl.Bool("json", false, "print JSON")
	names, ok := a.parse(fl, args, 1, "secret rm NAME [-json]")
	if !ok {
		return 2
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "secret rm", err)
	}
	err = vault.Update(cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, _ vault.Key) error {
		return f.Remove(names[0])
	})
	if errors.Is(err, vault.ErrNotFound) {
		err = fmt.Errorf("no secret %s", names[0])
	}
	if err != nil {
		return a.fail(*asJSON, "secret rm", sudoHint(err))
	}
	a.pushSecrets(ctx, cfg)
	if *asJSON {
		return a.reply(hibapi.Response{})
	}
	return 0
}

func (a *App) secretLS(args []string) int {
	fl := a.flags("secret ls")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "secret ls [-json]"); !ok {
		return 2
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "secret ls", err)
	}
	f, err := vault.Load(cfg.vaultPath)
	if err != nil {
		return a.fail(*asJSON, "secret ls", err)
	}
	secrets := apiSecrets(f.Secrets())
	if *asJSON {
		return a.reply(hibapi.Response{Secrets: secrets})
	}
	hibapi.PrintSecrets(a.Stdout, secrets, "")
	return 0
}

func loadSecretBlock(path string) ([]byte, error) {
	f, err := vault.Load(path)
	if err != nil {
		return nil, err
	}
	return machines.BlockFromSecrets(f.Secrets())
}

func (a *App) pushSecrets(ctx context.Context, cfg config) {
	if _, err := os.Stat(cfg.quiescing); err == nil {
		return
	}
	block, err := loadSecretBlock(cfg.vaultPath)
	if err != nil {
		fmt.Fprintf(a.Stderr, "warning: secrets saved, but loading placeholders failed: %s\n", termsafe.String(err.Error()))
		return
	}
	e, err := a.engine()
	if err != nil {
		fmt.Fprintf(a.Stderr, "warning: secrets saved, but listing machines failed: %s\n", termsafe.String(err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ms, err := e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	if err != nil {
		fmt.Fprintf(a.Stderr, "warning: secrets saved, but listing machines failed: %s\n", termsafe.String(err.Error()))
		return
	}
	for _, m := range ms {
		if machines.Name(m) == "" {
			continue
		}
		if err := machines.WriteBlock(ctx, e, m.ID, block); err != nil {
			name := termsafe.String(machines.Name(m))
			fmt.Fprintf(a.Stderr, "warning: secrets saved, but updating %s failed: %s\n", name, termsafe.String(err.Error()))
		}
	}
}

func sudoHint(err error) error {
	if os.Geteuid() == 0 {
		return err
	}
	msg := err.Error()
	if strings.Contains(msg, "vault key") || strings.Contains(msg, "permission denied") {
		return fmt.Errorf("%w (try: sudo -n hibd secret ...)", err)
	}
	return err
}

func apiSecrets(secrets []vault.Secret) []hibapi.Secret {
	out := make([]hibapi.Secret, len(secrets))
	for i, s := range secrets {
		out[i] = apiSecret(s)
	}
	slices.SortFunc(out, func(a, b hibapi.Secret) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func apiSecret(s vault.Secret) hibapi.Secret {
	return hibapi.Secret{Name: s.Name, Env: s.Env, Sites: slices.Clone(s.Sites), Placeholder: s.Placeholder}
}
