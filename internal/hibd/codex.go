package hibd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

const (
	connectCodexUsage = "connect codex [-json]"

	codexBinary = "/usr/local/bin/codex"

	codexLoginPrefix = "codex-login-"

	codexLoginLock = "codex-login.lock"

	maxCodexAuthFile = 1 << 20
)

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

func (a *App) warnf(format string, args ...any) {
	fmt.Fprintf(a.Stderr, "warning: %s\n", termsafe.String(fmt.Sprintf(format, args...)))
}

func (a *App) notef(format string, args ...any) {
	fmt.Fprintf(a.Stderr, "%s\n", termsafe.String(fmt.Sprintf(format, args...)))
}

func (a *App) codexBin() string {
	if a.CodexBin != "" {
		return a.CodexBin
	}
	return codexBinary
}

func (a *App) connectCodex(ctx context.Context, args []string) int {
	fl := a.flags("connect codex")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, connectCodexUsage); !ok {
		return 2
	}
	if err := a.needRoot("connect codex"); err != nil {
		return a.fail(*asJSON, "connect codex", err)
	}
	p, err := a.loginCodex(ctx)
	if err != nil {
		return a.fail(*asJSON, "connect codex", err)
	}
	c := p.API()
	if *asJSON {
		return a.reply(hibapi.Response{Codex: &c})
	}
	fmt.Fprintf(a.Stdout, "connected Codex as %s, on the %s plan\n", termsafe.String(c.Email), termsafe.String(c.Plan))
	return 0
}

func (a *App) loginCodex(ctx context.Context) (codex.Profile, error) {
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return codex.Profile{}, err
	}
	unlock, ok, err := lockCodexLogins(cfg.stateDir)
	if err != nil {
		return codex.Profile{}, err
	}
	if !ok {
		return codex.Profile{}, errors.New("another hib connect codex is running: finish it, or stop it, then try again")
	}
	defer unlock()
	removeCodexLogins(cfg.stateDir, a.warnf)
	dir, err := os.MkdirTemp(cfg.stateDir, codexLoginPrefix+"*")
	if err != nil {
		return codex.Profile{}, fmt.Errorf("making a directory for the login: %w", err)
	}
	defer os.RemoveAll(dir)
	tokens, err := a.runCodexLogin(ctx, dir)
	if err != nil {
		return codex.Profile{}, err
	}
	p, err := a.codexStore(cfg, a.warnf).CommitLogin(ctx, tokens)
	if err != nil {
		return codex.Profile{}, fmt.Errorf("keeping the login failed: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		a.warnf("deleting the login directory %s failed: %v", dir, err)
	}
	a.pushSetup(ctx, cfg, "Codex connected")
	return p, nil
}

func (a *App) runCodexLogin(ctx context.Context, dir string) (codex.Tokens, error) {
	bin := a.codexBin()
	if err := checkCodexVersion(ctx, bin, dir); err != nil {
		return codex.Tokens{}, err
	}
	cmd := codexCommand(ctx, bin, dir, "login", "--device-auth", "-c", `cli_auth_credentials_store="file"`)
	out := termsafe.NewWriter(a.Stderr)
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	_ = out.Flush()
	if ctx.Err() != nil {
		return codex.Tokens{}, errors.New("the codex login was cancelled. Nothing was stored")
	}
	if err != nil {
		return codex.Tokens{}, fmt.Errorf("codex login failed: %v. If ChatGPT didn't offer or accept the code, "+
			"turn on device code login in ChatGPT, under Settings, Security, then run hib connect codex again", err)
	}
	data, err := readCodexAuth(filepath.Join(dir, "auth.json"))
	if err != nil {
		return codex.Tokens{}, err
	}
	return codex.ParseAuthFile(data)
}

func checkCodexVersion(ctx context.Context, bin, home string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := codexCommand(ctx, bin, home, "--version")
	out := &limitedBuffer{max: 4 << 10}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s --version failed: %v. Install codex-cli %s there: run make deploy", bin, err, codex.Version)
	}
	want := "codex-cli " + codex.Version
	if got := string(bytes.TrimSpace(out.data)); got != want {
		return fmt.Errorf("%s is %q, but hibd needs %s. Install it: run make deploy", bin, got, want)
	}
	return nil
}

func codexCommand(ctx context.Context, bin, home string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Dir = home
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func readCodexAuth(path string) ([]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("codex login finished without saving a login: run hib connect codex again")
	}
	if err != nil {
		return nil, fmt.Errorf("reading the login codex saved: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCodexAuthFile+1))
	if err != nil {
		return nil, fmt.Errorf("reading the login codex saved: %w", err)
	}
	if len(data) > maxCodexAuthFile {
		return nil, errors.New("the login codex saved is too big: run hib connect codex again")
	}
	return data, nil
}

func lockCodexLogins(stateDir string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(filepath.Join(stateDir, codexLoginLock), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("opening the codex login lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("locking the codex login: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

func removeCodexLogins(stateDir string, logf func(format string, args ...any)) {
	found, err := filepath.Glob(filepath.Join(stateDir, codexLoginPrefix+"*"))
	if err != nil {
		return
	}
	for _, dir := range found {
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			logf("deleting the codex login directory %s failed: %v", dir, err)
		}
	}
}

func removeLeftoverCodexLogins(stateDir string, logf func(format string, args ...any)) {
	unlock, ok, err := lockCodexLogins(stateDir)
	if err != nil {
		logf("%v", err)
		return
	}
	if !ok {
		return
	}
	defer unlock()
	removeCodexLogins(stateDir, logf)
}

type limitedBuffer struct {
	data []byte
	max  int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.data); room > 0 {
		b.data = append(b.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
