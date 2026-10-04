package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
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

// lineLog keeps the lines a logf wrote.
type lineLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lineLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

// take returns the lines written since the last take.
func (l *lineLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := l.lines
	l.lines = nil
	return lines
}

func TestCodexRefresherLogsEachChangeOnce(t *testing.T) {
	a, _, _, _ := newCodexApp(t, nil, testCodexTokens())
	var log lineLog
	r := &codexRefresher{store: codexStoreFor(a), logf: log.logf}
	look := func(want ...string) {
		t.Helper()
		r.look(t.Context())
		if got := log.take(); !slices.Equal(got, want) {
			t.Errorf("logged %q, want %q", got, want)
		}
	}
	// connect connects tokens, as hib connect codex would, and points r at a token server for
	// them.
	connect := func(tokens codex.Tokens) *codextest.TokenServer {
		t.Helper()
		server := connectTestCodex(t, a, tokens)
		r.store = codexStoreFor(a)
		return server
	}
	const connected = "connected as dev@example.com, on the plus plan"

	look()
	look()

	server := connect(testCodexTokens())
	look(connected, "the login is ok")
	look()
	if n := server.Requests(); n != 0 {
		t.Errorf("OpenAI got %d refresh requests for a fresh login, want none", n)
	}

	// Another process, such as the proxy, refreshes.
	if _, err := codexStoreFor(a).EnsureFresh(t.Context(), codex.Reason{Kind: codex.Force}); err != nil {
		t.Fatal(err)
	}
	look("refreshed the login. The token expires at " + codexExpiry(t, r.store))
	look()

	server = connect(halfSpentCodexTokens())
	r.look(t.Context())
	if got, want := log.take(), []string{connected, "refreshed the login. The token expires at " + codexExpiry(t, r.store)}; !slices.Equal(got, want) {
		t.Errorf("logged %q, want %q", got, want)
	}
	if n := server.Requests(); n != 1 {
		t.Errorf("OpenAI got %d refresh requests, want 1 once half the lifetime has passed", n)
	}
	look()

	server = connect(halfSpentCodexTokens())
	server.Queue(http.StatusTooManyRequests, `{}`)
	r.look(t.Context())
	got := log.take()
	if len(got) != 2 || got[0] != connected || !strings.HasPrefix(got[1], "refreshing failed. Trying again after ") {
		t.Errorf("logged %q, want the new login, then that refreshing failed and when it tries again", got)
	}
	look()

	server = connect(halfSpentCodexTokens())
	server.Queue(http.StatusUnauthorized, `{"error":"invalid_grant"}`)
	look(connected, "the login is dead: invalid_grant: run hib connect codex")
	look()
	look()
	if n := server.Requests(); n != 1 {
		t.Errorf("OpenAI got %d refresh requests, want 1: a dead login isn't refreshed again", n)
	}
}

// codexExpiry returns when store's token expires, as the refresher logs it.
func codexExpiry(t *testing.T, store *codex.Store) string {
	t.Helper()
	p, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return p.Expires.UTC().Format(time.RFC3339)
}

func TestDaemonRefreshesCodexAndUpdatesMachines(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	tokens := halfSpentCodexTokens()
	a, _, _, _ := newCodexApp(t, guest, tokens)
	server := connectTestCodex(t, a, tokens)
	now := time.Now()
	upgraded := codextest.Tokens("acct-1234", "dev@example.com", "pro", now, now.Add(time.Hour))
	queueCodexTokens(t, server, upgraded)
	a.codexEvery = 10 * time.Millisecond

	before := codexLoginFile(t, a)

	logs, stop := startDaemon(t, a)
	waitFor(t, logs, "the running machine gets the new login file", func() bool {
		now := codexLoginFile(t, a)
		return !bytes.Equal(now, before) && bytes.Equal(guest.codexAuthFile(), now)
	})
	waitFor(t, logs, "the daemon logs the refresh", func() bool { return strings.Contains(logs.String(), "codex: refreshed the login") })
	stop()
	if n := strings.Count(logs.String(), "codex: the login is ok"); n != 1 {
		t.Errorf("the daemon logged the login's health %d times, want once:\n%s", n, logs)
	}
	if n := server.Requests(); n != 1 {
		t.Errorf("OpenAI got %d refresh requests, want 1: the new token is fresh", n)
	}
	wantNoCodexToken(t, tokens, []byte(logs.String()), guest.codexAuthFile())
	wantNoCodexToken(t, upgraded, []byte(logs.String()), guest.codexAuthFile())
}

func TestCodexRefresherLeavesCodexAloneWhileQuiescing(t *testing.T) {
	tokens := halfSpentCodexTokens()
	a, _, _, _ := newCodexApp(t, nil, tokens)
	server := connectTestCodex(t, a, tokens)
	var log lineLog
	r := &codexRefresher{store: codexStoreFor(a), logf: log.logf}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls, whileAsleep := 0, -1
	// awake says the host is going to sleep for three looks, lets the fourth happen, and then
	// stops the refresher.
	awake := func() error {
		calls++
		switch {
		case calls <= 3:
			return errQuiescing
		case calls == 4:
			whileAsleep = server.Requests()
			return nil
		}
		cancel()
		return errQuiescing
	}
	r.run(ctx, time.Millisecond, awake)
	if whileAsleep != 0 {
		t.Errorf("OpenAI got %d refresh requests while the host was going to sleep, want none", whileAsleep)
	}
	if n := server.Requests(); n != 1 {
		t.Errorf("OpenAI got %d refresh requests once the host was awake, want 1", n)
	}
}
