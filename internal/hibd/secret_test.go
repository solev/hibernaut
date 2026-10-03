package hibd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
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
	"github.com/solev/hibernaut/internal/vault"
)

type secretGuest struct {
	mu       sync.Mutex
	env      []byte
	requests []engine.ExecRequest
	reads    int
	hostKey  ssh.PublicKey
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
		g.reads++
		return engine.ExecResult{Stdout: append([]byte(nil), g.env...)}, nil
	case "sh":
		if len(req.Args) == 5 {
			if got := sha256.Sum256(g.env); req.Args[3] != base64Hash(got[:]) {
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

func (g *secretGuest) envString() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return string(g.env)
}

func (g *secretGuest) readCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reads
}

func (g *secretGuest) allRequests() []engine.ExecRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.requests)
}

func base64Hash(sum []byte) string {
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
	if code := a.Run(t.Context(), []string{"secret", "add", "github", "-env", "GH_TOKEN", "-bind", "github.com"}); code != 0 {
		t.Fatalf("secret add exit %d: %s", code, errOut)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"secret", "ls"}); code != 0 {
		t.Fatalf("secret ls exit %d: %s", code, errOut)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "realSecretValue") {
		t.Fatalf("unsafe or secret output: %q", out.String())
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
	before := guest.readCount()
	second := storeTestSecret(t, a, "openai", "OPENAI_API_KEY", "api.openai.com", "sk-proj-realSecretValue1234567890")
	waitFor(t, logs, "the daemon writes the changed block", func() bool {
		return strings.Contains(guest.envString(), second.Placeholder)
	})
	stop()
	if guest.readCount() <= before {
		t.Fatalf("daemon did not re-read /etc/environment after secret change")
	}
	readsBeforeRestart := guest.readCount()
	logs, stop = startDaemon(t, a)
	waitFor(t, logs, "the daemon re-checks after restart", func() bool { return guest.readCount() > readsBeforeRestart })
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
