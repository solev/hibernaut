package hibd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/vault"
)

type secretGuest struct {
	mu       sync.Mutex
	env      []byte
	requests []engine.ExecRequest
	hostKey  ssh.PublicKey
	// git is git's identity, as the setup wrote it.
	git *machines.GitIdentity
	// gitExit, if set, is the exit code of setting git's identity.
	gitExit int
	// noGit, if set, runs the setup's git script for real, in a shell whose PATH is this empty
	// directory, as in an image without git.
	noGit string
}

func (g *secretGuest) exec(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, req)
	switch req.Cmd {
	case images.MachineInit:
		if g.hostKey == nil {
			return engine.ExecResult{ExitCode: 127}, nil
		}
		return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(g.hostKey) + "\n")}, nil
	case "cat":
		return engine.ExecResult{Stdout: append([]byte(nil), g.env...)}, nil
	case "sh":
		if len(req.Args) == 5 && req.Args[2] == "hibd-git" {
			return g.setGit(req)
		}
		if len(req.Args) == 5 && req.Args[2] == "hibd-env" {
			if got := sha256.Sum256(g.env); req.Args[3] != hexHash(got[:]) {
				return engine.ExecResult{ExitCode: 75}, nil
			}
			next, err := base64.StdEncoding.DecodeString(req.Args[4])
			if err != nil {
				return engine.ExecResult{ExitCode: 64, Stderr: []byte(err.Error())}, nil
			}
			g.env = next
			return engine.ExecResult{}, nil
		}
	}
	return engine.ExecResult{ExitCode: 127}, nil
}

// setGit plays the setup's git script. g.mu is held.
func (g *secretGuest) setGit(req engine.ExecRequest) (engine.ExecResult, error) {
	if req.User != "root" {
		return engine.ExecResult{ExitCode: 1, Stderr: []byte("not root")}, nil
	}
	if g.noGit != "" {
		cmd := exec.Command("/bin/sh", req.Args...)
		cmd.Env = []string{"PATH=" + g.noGit}
		out, err := cmd.CombinedOutput()
		if _, ok := errors.AsType[*exec.ExitError](err); err != nil && !ok {
			return engine.ExecResult{}, err
		}
		return engine.ExecResult{ExitCode: cmd.ProcessState.ExitCode(), Stderr: out}, nil
	}
	if g.gitExit != 0 {
		return engine.ExecResult{ExitCode: g.gitExit, Stderr: []byte("error: could not lock config file /etc/gitconfig")}, nil
	}
	g.git = &machines.GitIdentity{Name: req.Args[3], Email: req.Args[4]}
	return engine.ExecResult{}, nil
}

func (g *secretGuest) identity() *machines.GitIdentity {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.git == nil {
		return nil
	}
	id := *g.git
	return &id
}

func (g *secretGuest) envString() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return string(g.env)
}

func (g *secretGuest) allRequests() []engine.ExecRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.requests)
}

func hexHash(sum []byte) string {
	const hex = "0123456789abcdef"
	var out strings.Builder
	for _, b := range sum {
		out.WriteByte(hex[b>>4])
		out.WriteByte(hex[b&0xf])
	}
	return out.String()
}

func storeTestSecret(t *testing.T, a *App, name, env, site, value string) vault.Secret {
	t.Helper()
	var saved vault.Secret
	err := vault.Update(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json"), a.Getenv("HIBD_VAULT_KEY"), func(f *vault.File, key vault.Key) error {
		var err error
		saved, err = f.Put(key, name, env, []string{site}, value)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func withSecretConfig(t *testing.T, a *App) {
	t.Helper()
	state := a.Getenv("HIBD_STATE_DIR")
	old := a.Getenv
	a.Getenv = func(k string) string {
		switch k {
		case "HIBD_VAULT_KEY":
			return filepath.Join(state, "vault.key")
		case "HIBD_AUDIT_LOG":
			return filepath.Join(state, "audit.jsonl")
		}
		return old(k)
	}
}

func TestSecretAddStoresMetadataAndPushesPlaceholdersOnly(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n"), hostKey: newKey(t)}
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now(),
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev"}})
	a, out, errOut := newApp(t, e)
	withSecretConfig(t, a)
	const value = "gho_realSecretValue1234567890"
	a.Stdin = strings.NewReader(value + "\n")
	code, r, err := runJSON(t, a, out, "secret", "add", "github", "-env", "GH_TOKEN", "-bind", "github.com,*.github.com", "-json")
	if code != 0 || err != nil {
		t.Fatalf("secret add exit %d, response %#v, err %v, stderr %s", code, r, err, errOut.String())
	}
	if r.Secret == nil || r.Secret.Name != "github" || r.Secret.Env != "GH_TOKEN" || !strings.Contains(guest.envString(), "GH_TOKEN="+r.Secret.Placeholder) {
		t.Fatalf("response %#v, env %q", r.Secret, guest.envString())
	}
	if strings.Contains(guest.envString(), value) || strings.Contains(out.String(), value) || strings.Contains(errOut.String(), value) {
		t.Fatalf("secret value leaked: stdout %q stderr %q env %q", out.String(), errOut.String(), guest.envString())
	}
	for _, req := range guest.allRequests() {
		if strings.Contains(strings.Join(req.Args, " "), value) {
			t.Fatalf("secret value reached argv: %+v", req)
		}
	}
}

func TestSecretListAndLogOutputAreTerminalSafe(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	withSecretConfig(t, a)
	a.Stdin = strings.NewReader("gho_realSecretValue1234567890")
	if code := a.Run(t.Context(), []string{"secret", "add", "gh\x1b", "-env", "GH_TOKEN", "-bind", "github.com"}); code != 1 {
		t.Fatalf("invalid secret name exit %d, want 1", code)
	}
	a.Stdin = strings.NewReader("gho_realSecretValue1234567890")
	code, r, err := runJSON(t, a, out, "secret", "add", "github", "-env", "GH_TOKEN", "-bind", "github.com", "-json")
	if code != 0 || err != nil || r.Secret == nil {
		t.Fatalf("secret add exit %d, response %#v, err %v, stderr %s", code, r, err, errOut)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"secret", "ls"}); code != 0 {
		t.Fatalf("secret ls exit %d: %s", code, errOut)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "realSecretValue") {
		t.Fatalf("unsafe or secret output: %q", out.String())
	}
	want := []string{"NAME", "ENV", "SITES", "PLACEHOLDER", "github", "GH_TOKEN", "github.com", r.Secret.Placeholder}
	if got := strings.Fields(out.String()); !slices.Equal(got, want) {
		t.Fatalf("secret ls fields = %q, want %q", got, want)
	}
	logPath := a.Getenv("HIBD_AUDIT_LOG")
	l, err := audit.Open(logPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Write(audit.Entry{Time: time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC), Machine: "app\x1b", Site: "api.github.com", Method: "GET", Path: "/user\x1b[2J", Secrets: []string{"github"}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"log", "-n", "1"}); code != 0 {
		t.Fatalf("log exit %d: %s", code, errOut)
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "app?") {
		t.Fatalf("log output was not terminal-safe: %q", out.String())
	}
}

func TestSecretAddRejectsOversizedStdin(t *testing.T) {
	a, _, errOut := newApp(t, fake.New())
	withSecretConfig(t, a)
	a.Stdin = bytes.NewReader(append(bytes.Repeat([]byte{'x'}, 64<<10), 'y'))
	if code := a.Run(t.Context(), []string{"secret", "add", "big", "-env", "BIG_TOKEN", "-bind", "example.com"}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "64 KiB") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestNewWritesSecretBlockBeforeItReturns(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n"), hostKey: newKey(t)}
	e := fake.New(fake.WithTemplates(images.Template("dev")), fake.WithExec(guest.exec))
	a, _, errOut := newApp(t, e)
	withSecretConfig(t, a)
	secret := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
	if code := a.Run(t.Context(), []string{"new", "app"}); code != 0 {
		t.Fatalf("new exit %d: %s", code, errOut)
	}
	if !strings.Contains(guest.envString(), fmt.Sprintf("GH_TOKEN=%s\n", secret.Placeholder)) {
		t.Fatalf("new did not write placeholders before returning: %q", guest.envString())
	}
}

func TestDaemonRedeliversAfterSecretChangeAndRestart(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now(),
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev", "hib.idle": "0s"}})
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	first := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
	logs, stop := startDaemon(t, a)
	waitFor(t, logs, "the daemon writes the first placeholder", func() bool {
		return strings.Contains(guest.envString(), first.Placeholder)
	})
	second := storeTestSecret(t, a, "openai", "OPENAI_API_KEY", "api.openai.com", "sk-proj-realSecretValue1234567890")
	block := "# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n" +
		"GH_TOKEN=" + first.Placeholder + "\nOPENAI_API_KEY=" + second.Placeholder + "\n# END hibernaut secrets\n"
	waitFor(t, logs, "the daemon writes the changed block", func() bool {
		return guest.envString() == "PATH=/usr/bin\n"+block
	})
	stop()
	guest.env = []byte("PATH=/usr/bin\nLOCAL_SETTING=preserved\n")
	logs, stop = startDaemon(t, a)
	waitFor(t, logs, "the daemon restores both placeholders and preserves local settings after restart", func() bool {
		return guest.envString() == "PATH=/usr/bin\nLOCAL_SETTING=preserved\n"+block
	})
	stop()
}

func TestRootOwnedVaultFilesDoNotBreakStateDirChecks(t *testing.T) {
	a, _, _ := newApp(t, newEngine(newKey(t)))
	withSecretConfig(t, a)
	state := a.Getenv("HIBD_STATE_DIR")
	for _, name := range []string{"vault.json", "vault.json.lock"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code := a.Run(t.Context(), []string{"ls", "-json"}); code != 0 {
		t.Fatalf("ls with vault files in state dir exit %d", code)
	}
}

var _ = hibapi.Secret{}

func TestSecretAddAndRmRefuseCodexNames(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	withSecretConfig(t, a)
	vaultPath := filepath.Join(a.Getenv("HIBD_STATE_DIR"), "vault.json")
	for _, name := range []string{"codex-access", "codex-refresh", "codex-mine"} {
		a.Stdin = strings.NewReader("sk-realSecretValue1234567890")
		code, r, err := runJSON(t, a, out, "secret", "add", name, "-env", "MY_TOKEN", "-bind", "example.com", "-json")
		var remote *hibapi.RemoteError
		if code != 1 || !errors.As(err, &remote) || !strings.Contains(remote.Msg, "hib connect codex") {
			t.Errorf("secret add %s: exit %d, reply %+v, %v; want 1 and an error naming hib connect codex", name, code, r, err)
		}
		if _, err := os.Stat(vaultPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("secret add %s touched the vault: %v", name, err)
		}
	}
	storeTestSecret(t, a, "codex-access", "CODEX_TEST", "chatgpt.com", "sk-realSecretValue1234567890")
	errOut.Reset()
	if code := a.Run(t.Context(), []string{"secret", "rm", "codex-access"}); code != 1 || !strings.Contains(errOut.String(), "hib connect codex") {
		t.Errorf("secret rm codex-access: exit %d, stderr %q; want 1 and an error naming hib connect codex", code, errOut)
	}
	if s := loadVault(t, a); len(s) != 1 {
		t.Errorf("vault = %+v, want codex-access kept", s)
	}
}
