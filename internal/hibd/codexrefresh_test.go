package hibd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/codex/codextest"
	"github.com/solev/hibernaut/internal/hibapi"
)

// codexLoginFile returns the placeholder login file a's Store gives machines now.
func codexLoginFile(t *testing.T, a *App) []byte {
	t.Helper()
	pub, err := codexStoreFor(a).Public()
	if err != nil {
		t.Fatal(err)
	}
	file, err := codex.RenderAuthFile(pub)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

// halfSpentCodexTokens returns tokens past half their lifetime, so a Background refresh is due.
func halfSpentCodexTokens() codex.Tokens {
	now := time.Now()
	return codextest.Tokens("acct-1234", "dev@example.com", "plus", now.Add(-6*24*time.Hour), now.Add(4*24*time.Hour))
}

// queueCodexTokens makes server's next answer next's tokens.
func queueCodexTokens(t *testing.T, server *codextest.TokenServer, next codex.Tokens) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"access_token": next.AccessToken, "id_token": next.IDToken, "refresh_token": next.RefreshToken})
	if err != nil {
		t.Fatal(err)
	}
	server.Queue(http.StatusOK, string(body))
}

func TestCodexRefreshForce(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	tokens := testCodexTokens()
	a, out, errOut, _ := newCodexApp(t, guest, tokens)
	server := connectTestCodex(t, a, tokens)
	before := codexLoginFile(t, a)
	now := time.Now()
	// A new plan changes the JWTs' placeholders, and so the login file.
	upgraded := codextest.Tokens("acct-1234", "dev@example.com", "pro", now, now.Add(time.Hour))
	queueCodexTokens(t, server, upgraded)

	code, r := runJSONReply(t, a, out, "codex", "refresh", "-force", "-json")
	if code != 0 || r.Codex == nil {
		t.Fatalf("codex refresh exit %d, reply %+v; stderr %s", code, r, errOut)
	}
	if n := server.Requests(); n != 1 {
		t.Errorf("OpenAI got %d refresh requests, want 1", n)
	}
	if r.Codex.Email != "dev@example.com" || r.Codex.Plan != "pro" || r.Codex.Health != hibapi.CodexOK ||
		!r.Codex.Expires.Equal(now.Add(time.Hour).Truncate(time.Second)) {
		t.Errorf("reply = %+v, want the refreshed login on pro, ok, with the new token's expiry", r.Codex)
	}
	after := codexLoginFile(t, a)
	if bytes.Equal(before, after) {
		t.Fatal("the refresh didn't change the login file, so this test can't see it pushed")
	}
	if got := guest.codexAuthFile(); !bytes.Equal(got, after) {
		t.Errorf("the running machine's auth.json isn't the new login file:\n%s", got)
	}
	if errOut.String() != "codex refreshed\n" {
		t.Errorf("stderr = %q, want only that Codex refreshed", errOut)
	}
	wantNoCodexToken(t, tokens, out.Bytes(), errOut.Bytes(), guest.codexAuthFile())
	wantNoCodexToken(t, upgraded, out.Bytes(), errOut.Bytes(), guest.codexAuthFile())
}

func TestCodexRefreshOnlyWhenDue(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	tokens := testCodexTokens()
	a, out, errOut, _ := newCodexApp(t, guest, tokens)
	server := connectTestCodex(t, a, tokens)
	code, r := runJSONReply(t, a, out, "codex", "refresh", "-json")
	if code != 0 || r.Codex == nil || r.Codex.Health != hibapi.CodexOK {
		t.Fatalf("codex refresh exit %d, reply %+v; stderr %s", code, r, errOut)
	}
	if n := server.Requests(); n != 0 {
		t.Errorf("OpenAI got %d refresh requests for a fresh login, want none", n)
	}
	if got := guest.codexAuthFile(); got != nil {
		t.Errorf("codex refresh wrote auth.json without a change:\n%s", got)
	}
}

func TestCodexRefreshWhenHalfTheLifetimeHasPassed(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	tokens := halfSpentCodexTokens()
	a, out, errOut, _ := newCodexApp(t, guest, tokens)
	server := connectTestCodex(t, a, tokens)
	if code, r := runJSONReply(t, a, out, "codex", "refresh", "-json"); code != 0 || r.Codex == nil {
		t.Fatalf("codex refresh exit %d, reply %+v; stderr %s", code, r, errOut)
	}
	if n := server.Requests(); n != 1 {
		t.Errorf("OpenAI got %d refresh requests, want 1", n)
	}
	// The same account and plan keep every placeholder, so machines need nothing new.
	if got := guest.codexAuthFile(); got != nil {
		t.Errorf("codex refresh wrote auth.json though no placeholder changed:\n%s", got)
	}
}

func TestCodexRefreshSaysWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		connect bool
		answer  int
		want    string
	}{
		{name: "not connected", want: "codex is not connected: run hib connect codex"},
		{name: "dead", connect: true, answer: http.StatusUnauthorized, want: "the codex login has ended: run hib connect codex"},
		{name: "stuck", connect: true, answer: http.StatusBadGateway, want: "a codex refresh has an unknown outcome: run hib connect codex"},
		{name: "unavailable", connect: true, answer: http.StatusTooManyRequests, want: "codex can't refresh right now: try again in a few minutes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens := testCodexTokens()
			a, out, _, _ := newCodexApp(t, nil, tokens)
			if tc.connect {
				server := connectTestCodex(t, a, tokens)
				server.Queue(tc.answer, `{"error":"invalid_grant"}`)
			}
			code, r := runJSONReply(t, a, out, "codex", "refresh", "-force", "-json")
			if code != 1 || r.Error != tc.want {
				t.Errorf("exit %d, error %q; want 1 and %q", code, r.Error, tc.want)
			}
		})
	}
}

func TestCodexRefreshPrintsTheLogin(t *testing.T) {
	tokens := testCodexTokens()
	a, out, errOut, _ := newCodexApp(t, nil, tokens)
	connectTestCodex(t, a, tokens)
	if code := a.Run(t.Context(), []string{"codex", "refresh"}); code != 0 {
		t.Fatalf("exit %d; stderr %s", code, errOut)
	}
	if !strings.HasPrefix(out.String(), "Codex  dev@example.com, plus plan, token expires ") || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("stdout = %q, want the Codex line hib status shows", out)
	}

	setCodexHealth(t, a, hibapi.CodexDead, "invalid_grant: run hib connect codex")
	out.Reset()
	if code := a.Run(t.Context(), []string{"codex", "refresh"}); code != 1 {
		t.Errorf("exit %d, want 1 while the login is dead", code)
	}
	if !strings.Contains(out.String(), "dead: invalid_grant: run hib connect codex") {
		t.Errorf("stdout = %q, want it to say the login is dead", out)
	}
}

func TestCodexRefreshNeedsRoot(t *testing.T) {
	tokens := testCodexTokens()
	a, out, _, _ := newCodexApp(t, nil, tokens)
	server := connectTestCodex(t, a, tokens)
	a.EUID = func() int { return 1000 }
	code, r := runJSONReply(t, a, out, "codex", "refresh", "-force", "-json")
	if code != 1 || !strings.Contains(r.Error, "sudo hibd codex refresh") {
		t.Errorf("exit %d, error %q; want 1, saying to run it as root", code, r.Error)
	}
	if n := server.Requests(); n != 0 {
		t.Errorf("OpenAI got %d refresh requests, want none", n)
	}
}

func TestCodexUsage(t *testing.T) {
	for _, args := range [][]string{{"codex"}, {"codex", "nope"}, {"codex", "refresh", "extra"}} {
		a, _, errOut, _ := newCodexApp(t, nil, testCodexTokens())
		if code := a.Run(t.Context(), args); code != 2 || !strings.Contains(errOut.String(), "codex refresh [-force] [-json]") {
			t.Errorf("hibd %q exit %d, stderr %q; want 2 and the usage", args, code, errOut)
		}
	}
}
