package codex_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
)

func TestPinnedCodexReadsPlaceholderLogin(t *testing.T) {
	bin := os.Getenv("HIB_CODEX")
	if bin == "" {
		bin, _ = exec.LookPath("codex")
	}
	require := os.Getenv("HIB_REQUIRE_CODEX") == "1"
	if bin == "" {
		if require {
			t.Fatal("pinned Codex is required: set HIB_CODEX")
		}
		t.Skip("pinned Codex is unavailable: set HIB_CODEX")
	}
	home := t.TempDir()
	env := []string{"HOME=" + home, "CODEX_HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	run := func(output func(*exec.Cmd) ([]byte, error), args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		cmd.Dir = home
		return output(cmd)
	}
	// The version is stdout alone. The pinned Codex warns on stderr when CODEX_HOME is under /tmp,
	// where t.TempDir is on Linux.
	version, err := run((*exec.Cmd).Output, "--version")
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli "+codex.Version {
		if require || os.Getenv("HIB_CODEX") != "" {
			t.Fatal("HIB_CODEX must be the pinned Codex release")
		}
		t.Skip("Codex on PATH is not the pinned release: set HIB_CODEX")
	}
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	s, _ := newStore(t, now)
	tokens := codextest.Tokens("account", "person@example.com", "plus", now, now.Add(time.Hour))
	if _, err := s.CommitLogin(context.Background(), tokens); err != nil {
		t.Fatal(err)
	}
	p, err := s.Public()
	if err != nil {
		t.Fatal(err)
	}
	b, err := codex.RenderAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "auth.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// The pinned CLI's run_login_status loads file auth locally. It does not contact OpenAI.
	out, err := run((*exec.Cmd).CombinedOutput, "login", "status", "-c", `cli_auth_credentials_store="file"`)
	if err != nil || !bytes.Contains(out, []byte("Logged in using ChatGPT")) {
		t.Fatal("pinned Codex did not recognize the placeholder ChatGPT login")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, b) {
		t.Fatal("the read-only login check changed the placeholder file")
	}
}
