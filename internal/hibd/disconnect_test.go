package hibd

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/proxy"
	"github.com/solev/hibernaut/internal/vault"
)

func TestDisconnectIsIdempotentAndReloadsProxy(t *testing.T) {
	for _, provider := range []string{"codex", "github"} {
		t.Run(provider, func(t *testing.T) {
			a, out, errOut, gh := newGitHubApp(t, fake.New())
			files := []string{github.ProfileFile}
			secretName, site, revocation := github.SecretName, "api.github.com", "authorized OAuth Apps"
			if provider == "github" {
				gh.set(testToken, octocat)
				if code, _ := connectGitHub(t, a, out, testToken, "-source", "token"); code != 0 {
					t.Fatalf("connect exit %d: %s", code, errOut)
				}
			} else {
				secretName, site, revocation = codex.AccessName, "chatgpt.com", "ChatGPT's settings"
				files = []string{codex.ProfileFile, codex.RetiredFile}
				if _, err := codexStoreFor(a).CommitLogin(t.Context(), testCodexTokens()); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(a.Getenv("HIBD_STATE_DIR"), codex.RetiredFile), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			var placeholder, other string
			if err := vault.Update(cfg.vaultPath, cfg.vaultKeyPath, func(f *vault.File, key vault.Key) error {
				for _, s := range f.Secrets() {
					if s.Name == secretName {
						placeholder = s.Placeholder
					}
				}
				kept, err := f.Put(key, "other", "OTHER", []string{"example.com"}, "otherSecretAAAAAAAAAAAAAAAAAAAAAAAAAA")
				other = kept.Placeholder
				return err
			}); err != nil {
				t.Fatal(err)
			}
			source := &proxy.VaultSource{Path: cfg.vaultPath, KeyPath: cfg.vaultKeyPath}
			swapRequest := func() string {
				t.Helper()
				r := httptest.NewRequest(http.MethodGet, "https://"+site+"/", nil)
				r.Header.Set("Authorization", "Bearer "+placeholder)
				if sw := source.Swapper(); sw != nil {
					if _, err := sw.Request(r, site); err != nil {
						t.Fatal(err)
					}
				}
				return r.Header.Get("Authorization")
			}
			if got := swapRequest(); got == "Bearer "+placeholder {
				t.Fatal("login was not swapped before disconnect")
			}
			out.Reset()
			errOut.Reset()
			if code := a.Run(t.Context(), []string{"disconnect", provider, "-json"}); code != 0 || out.String() != "{\"v\":1}\n" {
				t.Fatalf("disconnect = %d, stdout %s, stderr %s", code, out, errOut)
			}
			if !strings.Contains(errOut.String(), "removed") || !strings.Contains(errOut.String(), "placeholders") ||
				!strings.Contains(errOut.String(), "stops working") || !strings.Contains(errOut.String(), revocation) {
				t.Fatalf("missing consequences or revocation advice: %s", errOut)
			}
			if got := swapRequest(); got != "Bearer "+placeholder {
				t.Fatal("removed login was still swapped after disconnect")
			}
			for _, file := range files {
				if _, err := os.Lstat(filepath.Join(cfg.stateDir, file)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s survived: %v", file, err)
				}
			}
			secrets := loadVault(t, a)
			if len(secrets) != 1 || secrets[0].Name != "other" || secrets[0].Placeholder != other {
				t.Errorf("disconnect changed other secrets: %+v", secrets)
			}
			for _, lock := range []string{provider + "-login.lock", "vault.json.lock"} {
				if _, err := os.Stat(filepath.Join(cfg.stateDir, lock)); err != nil {
					t.Errorf("lock %s removed: %v", lock, err)
				}
			}
			if provider == "codex" {
				if _, err := codexStoreFor(a).EnsureFresh(t.Context(), codex.Reason{Kind: codex.Background}); !errors.Is(err, codex.ErrNotConnected) {
					t.Errorf("background refresh after disconnect: %v", err)
				}
				if _, err := os.Stat(filepath.Join(cfg.stateDir, codex.LockFile)); err != nil {
					t.Errorf("refresh lock removed: %v", err)
				}
			}
			out.Reset()
			errOut.Reset()
			if code := a.Run(t.Context(), []string{"disconnect", provider}); code != 0 || out.Len() != 0 ||
				!strings.Contains(errOut.String(), "not connected") {
				t.Fatalf("second disconnect = %d, stdout %s, stderr %s", code, out, errOut)
			}
		})
	}
}

func TestDisconnectAdvertisedAndRejectsWrongUsage(t *testing.T) {
	a, out, errOut := newApp(t, fake.New())
	for _, args := range [][]string{{"disconnect"}, {"disconnect", "other"}, {"disconnect", "codex", "extra"}} {
		if code := a.Run(t.Context(), args); code != 2 || out.Len() != 0 {
			t.Fatalf("args %v exit %d", args, code)
		}
	}
	code, reply, err := runJSON(t, a, out, "version", "-json")
	if code != 0 || err != nil || !slices.Contains(reply.Features, hibapi.FeatureDisconnect) {
		t.Fatalf("missing disconnect feature: %d, %+v, %v, stderr %s", code, reply, err, errOut)
	}
}

func TestDisconnectKeepsMachinePlaceholders(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	tokens := testCodexTokens()
	a, out, errOut, _ := newCodexApp(t, guest, tokens)
	if code := a.Run(t.Context(), []string{"connect", "codex", "-json"}); code != 0 {
		t.Fatalf("connect exit %d: %s", code, errOut)
	}
	before := guest.codexAuthFile()
	if len(before) == 0 {
		t.Fatal("machine received no placeholder file")
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"disconnect", "codex", "-json"}); code != 0 {
		t.Fatalf("disconnect exit %d: %s", code, errOut)
	}
	if after := guest.codexAuthFile(); !bytes.Equal(before, after) {
		t.Fatal("disconnect changed the machine's placeholder file")
	}
	wantNoCodexToken(t, tokens, before, out.Bytes(), errOut.Bytes())
}
