package swap_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/egress/swap"
)

func scrubOnlySecrets() []swap.Secret {
	return []swap.Secret{
		{
			Name: "codex-refresh", Placeholder: "rt.hib_place_refresh_aaaaaaaaaaaa", Value: "rt.real_refresh_value",
			Sites: []string{"auth.openai.com"}, ScrubOnly: true,
		},
		{
			Name: "codex-id", Placeholder: "hib_place_id_bbbbbbbbbbbbbbbbbbbb", Value: "real-id-token-value",
			Sites: []string{"auth.openai.com"}, ScrubOnly: true,
		},
		{
			Name: "codex-access", Placeholder: "hib_place_access_cccccccccccccccc", Value: "real-access-token-value",
			Sites: []string{"chatgpt.com"},
		},
	}
}

func TestScrubOnlySecretBindsItsSites(t *testing.T) {
	sw := newSwapper(t, scrubOnlySecrets()[:1])
	if !sw.Bound("auth.openai.com") {
		t.Fatal("a scrub-only secret's site must be bound, so the proxy intercepts it")
	}
	if sw.Bound("chatgpt.com") {
		t.Fatal("chatgpt.com is bound without a secret for it")
	}
}

func TestScrubOnlySecretIsNeverSwapped(t *testing.T) {
	secrets := scrubOnlySecrets()
	sw := newSwapper(t, secrets)
	for _, auth := range []string{
		"Bearer " + secrets[0].Placeholder,
		"Bearer " + secrets[1].Placeholder,
		"Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+secrets[0].Placeholder)),
	} {
		req, err := http.NewRequest(http.MethodPost, "https://auth.openai.com/oauth/token", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		names, err := sw.Request(req, "auth.openai.com")
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 0 || req.Header.Get("Authorization") != auth {
			t.Errorf("Request swapped scrub-only secrets %q, Authorization changed: %v", names, req.Header.Get("Authorization") != auth)
		}
	}

	// A secret that isn't scrub-only is still swapped on its own site.
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secrets[2].Placeholder)
	names, err := sw.Request(req, "chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"codex-access"}) || req.Header.Get("Authorization") != "Bearer "+secrets[2].Value {
		t.Fatalf("Request swapped %q, want codex-access swapped", names)
	}
}

func TestScrubOnlySecretIsScrubbedFromResponses(t *testing.T) {
	secrets := scrubOnlySecrets()
	sw := newSwapper(t, secrets)
	header := http.Header{"X-Token": {"refresh " + secrets[0].Value + " id " + secrets[1].Value}}
	sw.ScrubHeader(header)
	if got, want := header.Get("X-Token"), "refresh "+secrets[0].Placeholder+" id "+secrets[1].Placeholder; got != want {
		t.Errorf("ScrubHeader left a scrub-only value: %v", strings.Contains(got, "real"))
	}
	in := `{"refresh_token":"` + secrets[0].Value + `","id_token":"` + secrets[1].Value + `"}`
	want := `{"refresh_token":"` + secrets[0].Placeholder + `","id_token":"` + secrets[1].Placeholder + `"}`
	got, err := io.ReadAll(sw.ScrubBody(io.NopCloser(strings.NewReader(in))))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("ScrubBody left a scrub-only value: %v", strings.Contains(string(got), "real"))
	}
}
