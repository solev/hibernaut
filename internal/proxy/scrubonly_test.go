package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/proxy"
	"github.com/solev/hibernaut/internal/vault"
)

func TestVaultSourceKeepsCodexRefreshAndIDScrubOnly(t *testing.T) {
	dir := t.TempDir()
	path, keyPath := filepath.Join(dir, "vault.json"), filepath.Join(dir, "key")
	key, err := vault.CreateKey(keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		codex.AccessName:  "access-real-value-0123456789",
		codex.RefreshName: "refresh-real-value-0123456789",
		codex.IDName:      "id-real-value-0123456789",
	}
	placeholders := map[string]string{}
	for _, name := range []string{codex.AccessName, codex.RefreshName, codex.IDName} {
		s, err := v.Put(key, name, "", []string{codex.TokenSite}, values[name])
		if err != nil {
			t.Fatal(err)
		}
		placeholders[name] = s.Placeholder
	}
	if err := v.Save(path); err != nil {
		t.Fatal(err)
	}
	sw := (&proxy.VaultSource{Path: path, KeyPath: keyPath}).Swapper()
	if sw == nil || !sw.Bound(codex.TokenSite) {
		t.Fatal("codex secrets didn't bind their site")
	}
	for name, value := range values {
		req := httptest.NewRequest(http.MethodPost, "https://"+codex.TokenSite+"/", nil)
		req.Header.Set("Authorization", "Bearer "+placeholders[name])
		swapped, err := sw.Request(req, codex.TokenSite)
		if err != nil {
			t.Fatal(err)
		}
		wantSwapped := !codex.ScrubOnly(name)
		if gotSwapped := req.Header.Get("Authorization") == "Bearer "+value; gotSwapped != wantSwapped || (len(swapped) == 1) != wantSwapped {
			t.Fatalf("%s: swapped = %v, want %v", name, gotSwapped, wantSwapped)
		}
		body, err := io.ReadAll(sw.ScrubBody(io.NopCloser(strings.NewReader("token " + value))))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "token "+placeholders[name] {
			t.Fatalf("%s wasn't scrubbed from a response", name)
		}
	}
}
