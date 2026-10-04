package vault_test

import (
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/vault"
)

func TestSecretsWithoutEnvironmentVariables(t *testing.T) {
	key, err := vault.NewKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := new(vault.File)
	for _, name := range []string{"codex-access", "codex-refresh", "codex-id"} {
		if _, err := f.Put(key, name, "", []string{"auth.openai.com"}, name+strings.Repeat("Az", 20)); err != nil {
			t.Fatal(err)
		}
	}
	if err := vault.CheckSecret("manual", "", []string{"example.com"}); err == nil {
		t.Fatal("manual secrets still need an environment variable")
	}
}

func TestRefreshTokenRenewalKeepsPrefixAndPlaceholder(t *testing.T) {
	key, err := vault.NewKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := new(vault.File)
	first, err := f.Put(key, "refresh", "REFRESH", []string{"example.com"}, "rt."+strings.Repeat("Ab", 24))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Put(key, "refresh", "REFRESH", []string{"example.com"}, "rt."+strings.Repeat("Cd", 30))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.Placeholder, "rt.") || first.Placeholder != second.Placeholder {
		t.Fatal("renewing a refresh token lost its prefix or placeholder")
	}
}
