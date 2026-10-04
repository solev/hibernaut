package github_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/version"
)

const (
	testClientID = "Ov23liTestClientID01"
	deviceCode   = "DEVICEcode-9f3a1c7e5b2d4086a1b3c5d7e9f0a2b4"
	deviceToken  = "gho_DEVICEtoken7c1e9a3f5b0d2468ace13579bdf"
	lowerCode    = "secret_device_code_only_letters"
	lowerToken   = "gho_secret_token_only_letters"
	description  = "DESCRIPTION-must-not-show"
	codePath     = "/login/device/code"
	tokenPath    = "/login/oauth/access_token"
	hidden       = "GitHub's answer was unreadable"
)

var start = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

type fakeClock struct {
	t       *testing.T
	mu      sync.Mutex
	now     time.Time
	waits   []time.Duration
	onSleep func(n int)
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		c.t.Errorf("Sleep(%v), want a wait longer than 0", d)
	}
	c.mu.Lock()
	c.waits = append(c.waits, d)
	n, hook := len(c.waits), c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.waits)
}

type answer struct {
	status   int
	body     string
	location string
	drop     bool
	hang     bool
}

type request struct {
	method, path, query string
	form                url.Values
	header              http.Header
	at                  time.Time
}

type oauthServer struct {
	t     *testing.T
	srv   *httptest.Server
	clock *fakeClock
	done  chan struct{}

	mu       sync.Mutex
	code     answer
	polls    []answer
	requests []request
	hook     func(request)
}

func newOAuth(t *testing.T, code answer, polls ...answer) *oauthServer {
	t.Helper()
	s := &oauthServer{t: t, clock: &fakeClock{t: t, now: start}, done: make(chan struct{}), code: code, polls: polls}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.srv.StartTLS()
	t.Cleanup(s.srv.Close)
	t.Cleanup(func() { close(s.done) })
	return s
}

func (s *oauthServer) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.t.Errorf("fake GitHub can't parse the form: %v", err)
	}
	req := request{r.Method, r.URL.Path, r.URL.RawQuery, r.PostForm, r.Header.Clone(), s.clock.Now()}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	var a answer
	switch r.URL.Path {
	case codePath:
		a = s.code
	case tokenPath:
		if len(s.polls) == 0 {
			s.t.Error("fake GitHub got a poll it has no answer for")
			a = answer{status: http.StatusNotFound}
			break
		}
		a = s.polls[min(s.countLocked(tokenPath), len(s.polls))-1]
	case "/elsewhere":
		a = answer{body: tokenReply(nil)}
	default:
		a = answer{status: http.StatusNotFound}
	}
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	s.write(w, r, a)
}

func (s *oauthServer) write(w http.ResponseWriter, r *http.Request, a answer) {
	switch {
	case a.drop:
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			s.t.Error(err)
			return
		}
		conn.Close()
		return
	case a.hang:
		select {
		case <-r.Context().Done():
		case <-s.done:
		}
		return
	}
	if a.location != "" {
		w.Header().Set("Location", a.location)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(cmp.Or(a.status, http.StatusOK))
	io.WriteString(w, a.body)
}

func (s *oauthServer) countLocked(path string) int {
	n := 0
	for _, r := range s.requests {
		if r.path == path {
			n++
		}
	}
	return n
}

func (s *oauthServer) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countLocked(path)
}

func (s *oauthServer) all() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *oauthServer) flow() *github.DeviceFlow {
	return &github.DeviceFlow{Client: s.srv.Client(), Base: s.srv.URL, ClientID: testClientID, Now: s.clock.Now, Sleep: s.clock.Sleep}
}

type result struct {
	token   string
	err     error
	prompts []github.DevicePrompt
}

func (s *oauthServer) login(ctx context.Context, f *github.DeviceFlow) result {
	s.t.Helper()
	var r result
	r.token, r.err = f.Login(ctx, func(p github.DevicePrompt) { r.prompts = append(r.prompts, p) })
	if r.err != nil {
		checkNoSecrets(s.t, r.err)
		if r.token != "" {
			s.t.Error("Login returned a token with its error")
		}
	}
	return r
}

func (s *oauthServer) run() result {
	s.t.Helper()
	return s.login(s.t.Context(), s.flow())
}

func checkNoSecrets(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	for _, secret := range []string{deviceCode, deviceToken, lowerCode, lowerToken} {
		if strings.Contains(msg, secret) {
			t.Errorf("the error quotes the device code or the token: %q", strings.ReplaceAll(msg, secret, "<SECRET>"))
		}
	}
	for _, shown := range []string{description, "evil.example"} {
		if strings.Contains(msg, shown) {
			t.Errorf("the error shows GitHub's error_description or error_uri: %q", msg)
		}
	}
}

func quote(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func object(fields map[string]string) string {
	parts := make([]string, 0, len(fields))
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		parts = append(parts, quote(k)+":"+fields[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func changed(fields, changes map[string]string) string {
	for k, v := range changes {
		if v == "" {
			delete(fields, k)
		} else {
			fields[k] = v
		}
	}
	return object(fields)
}

func codeReply(changes map[string]string) string {
	return changed(map[string]string{
		"device_code":      quote(deviceCode),
		"user_code":        quote("WDJB-MJHT"),
		"verification_uri": quote(hibapi.GitHubDeviceURL),
		"expires_in":       "900",
		"interval":         "5",
	}, changes)
}

func tokenReply(changes map[string]string) string {
	return changed(map[string]string{
		"access_token": quote(deviceToken),
		"token_type":   quote("bearer"),
		"scope":        quote("repo,read:org,workflow"),
	}, changes)
}

func errorReply(code string, more map[string]string) string {
	return changed(map[string]string{
		"error":             code,
		"error_description": quote(description),
		"error_uri":         quote("https://evil.example/" + description),
	}, more)
}

func refused(status int, code string) answer {
	return answer{status: status, body: errorReply(quote(code), nil)}
}

func pending() answer { return refused(http.StatusOK, "authorization_pending") }

func slowDown(interval string) answer {
	return answer{body: errorReply(quote("slow_down"), map[string]string{"interval": interval})}
}

func code(changes map[string]string) answer { return answer{body: codeReply(changes)} }

func token() answer { return answer{body: tokenReply(nil)} }

func seconds(s ...int) []time.Duration {
	d := make([]time.Duration, len(s))
	for i, n := range s {
		d[i] = time.Duration(n) * time.Second
	}
	return d
}

func wantWaits(t *testing.T, c *fakeClock, want []time.Duration) {
	t.Helper()
	if got := c.slept(); !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}
}

func wantToken(t *testing.T, r result) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("Login error = %v, want the token", r.err)
	}
	if r.token != deviceToken {
		t.Fatalf("Login gave a token that isn't the one GitHub sent (%d bytes)", len(r.token))
	}
}

func TestDeviceScopesIsAFreshCopy(t *testing.T) {
	want := []string{"repo", "read:org", "workflow"}
	scopes := github.DeviceScopes()
	if !slices.Equal(scopes, want) {
		t.Fatalf("DeviceScopes() = %q, want %q", scopes, want)
	}
	scopes[0] = "admin:org"
	if got := github.DeviceScopes(); !slices.Equal(got, want) {
		t.Fatalf("changing what DeviceScopes returned changed the next call: %q", got)
	}
}

func TestDeviceLogin(t *testing.T) {
	s := newOAuth(t, code(nil), token())
	var prompts []github.DevicePrompt
	tok, err := s.flow().Login(t.Context(), func(p github.DevicePrompt) {
		prompts = append(prompts, p)
		if n := len(s.clock.slept()); n != 0 {
			t.Errorf("prompt came after %d waits, want it before the first", n)
		}
		if n := s.count(tokenPath); n != 0 {
			t.Errorf("prompt came after %d polls, want it before the first", n)
		}
	})
	wantToken(t, result{token: tok, err: err})
	if want := []github.DevicePrompt{{UserCode: "WDJB-MJHT", ExpiresIn: 15 * time.Minute}}; !slices.Equal(prompts, want) {
		t.Errorf("prompts = %+v, want %+v", prompts, want)
	}
	wantWaits(t, s.clock, seconds(5))

	reqs := s.all()
	if len(reqs) != 2 {
		t.Fatalf("GitHub got %d requests, want 2", len(reqs))
	}
	for i, want := range []request{
		{method: http.MethodPost, path: codePath, at: start,
			form: url.Values{"client_id": {testClientID}, "scope": {"repo read:org workflow"}}},
		{method: http.MethodPost, path: tokenPath, at: start.Add(5 * time.Second),
			form: url.Values{"client_id": {testClientID}, "device_code": {deviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}},
	} {
		got := reqs[i]
		if got.method != want.method || got.path != want.path || got.query != "" || !got.at.Equal(want.at) {
			t.Errorf("request %d = %s %s?%s at %v, want %s %s at %v", i, got.method, got.path, got.query, got.at, want.method, want.path, want.at)
		}
		if !maps.EqualFunc(got.form, want.form, slices.Equal) {
			t.Errorf("request %d form has the keys %q, want %q", i, slices.Sorted(maps.Keys(got.form)), slices.Sorted(maps.Keys(want.form)))
		}
		for header, value := range map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Accept":       "application/json",
			"User-Agent":   "hibernaut-hibd/" + version.Version,
		} {
			if got := got.header.Get(header); got != value {
				t.Errorf("request %d %s = %q, want %q", i, header, got, value)
			}
		}
		if got.header.Get("Authorization") != "" {
			t.Errorf("request %d has an Authorization header", i)
		}
	}
}

func TestDeviceLoginKeepsPollingWhilePending(t *testing.T) {
	s := newOAuth(t, code(nil), pending(), refused(http.StatusBadRequest, "authorization_pending"), pending(), token())
	wantToken(t, s.run())
	wantWaits(t, s.clock, seconds(5, 5, 5, 5))
	if n := s.count(tokenPath); n != 4 {
		t.Errorf("polls = %d, want 4", n)
	}
}

func TestDeviceLoginAcceptsGoodCodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]string
		prompt  github.DevicePrompt
		waits   []time.Duration
	}{
		{"interval 0 means 5 s", map[string]string{"interval": "0"}, github.DevicePrompt{UserCode: "WDJB-MJHT", ExpiresIn: 15 * time.Minute}, seconds(5)},
		{"no interval means 5 s", map[string]string{"interval": ""}, github.DevicePrompt{UserCode: "WDJB-MJHT", ExpiresIn: 15 * time.Minute}, seconds(5)},
		{"a null interval means 5 s", map[string]string{"interval": "null"}, github.DevicePrompt{UserCode: "WDJB-MJHT", ExpiresIn: 15 * time.Minute}, seconds(5)},
		{"the longest code and interval", map[string]string{"expires_in": "3600", "interval": "900"}, github.DevicePrompt{UserCode: "WDJB-MJHT", ExpiresIn: time.Hour}, seconds(900)},
		{"a short code", map[string]string{"user_code": quote("ab12"), "expires_in": "60", "interval": "1"}, github.DevicePrompt{UserCode: "ab12", ExpiresIn: time.Minute}, seconds(1)},
		{"four parts of 16", map[string]string{"user_code": quote(strings.Repeat("A1b2C3d4E5f6G7h8-", 3) + "A1b2C3d4E5f6G7h8")},
			github.DevicePrompt{UserCode: strings.Repeat("A1b2C3d4E5f6G7h8-", 3) + "A1b2C3d4E5f6G7h8", ExpiresIn: 15 * time.Minute}, seconds(5)},
		{"a long device code", map[string]string{"device_code": quote(strings.Repeat("a", 255))}, github.DevicePrompt{UserCode: "WDJB-MJHT", ExpiresIn: 15 * time.Minute}, seconds(5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, code(tc.changes), token())
			r := s.run()
			wantToken(t, r)
			if want := []github.DevicePrompt{tc.prompt}; !slices.Equal(r.prompts, want) {
				t.Errorf("prompts = %+v, want %+v", r.prompts, want)
			}
			wantWaits(t, s.clock, tc.waits)
		})
	}
}

func TestDeviceLoginSlowsDown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		polls []answer
		waits []time.Duration
	}{
		{"without an interval", []answer{slowDown(""), pending(), token()}, seconds(5, 10, 10)},
		{"to GitHub's longer interval", []answer{slowDown("20"), pending(), token()}, seconds(5, 20, 20)},
		{"by at least 5 s", []answer{slowDown("7"), pending(), token()}, seconds(5, 10, 10)},
		{"twice", []answer{slowDown(""), slowDown(""), token()}, seconds(5, 10, 15)},
		{"past a minute", []answer{slowDown("120"), token()}, seconds(5, 120)},
		{"on HTTP 400", []answer{{status: http.StatusBadRequest, body: slowDown("").body}, token()}, seconds(5, 10)},
		{"with a string interval", []answer{slowDown(`"20"`), token()}, seconds(5, 10)},
		{"with a negative interval", []answer{slowDown("-20"), token()}, seconds(5, 10)},
		{"with a fractional interval", []answer{slowDown("20.5"), token()}, seconds(5, 10)},
		{"with a null interval", []answer{slowDown("null"), token()}, seconds(5, 10)},
		{"with a huge interval", []answer{slowDown("99999999999999999999"), token()}, seconds(5, 10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, code(nil), tc.polls...)
			wantToken(t, s.run())
			wantWaits(t, s.clock, tc.waits)
		})
	}
}

func TestDeviceLoginExpires(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]string
		polls   []answer
		waits   []time.Duration
		polled  int
	}{
		{"when GitHub says so", nil, []answer{refused(http.StatusOK, "expired_token")}, seconds(5), 1},
		{"when GitHub says so on HTTP 400", nil, []answer{refused(http.StatusBadRequest, "expired_token")}, seconds(5), 1},
		{"at the deadline", map[string]string{"expires_in": "12"}, []answer{pending()}, seconds(5, 5, 2), 2},
		{"at the deadline, after slow_down asks for longer", map[string]string{"expires_in": "60"}, []answer{slowDown("3600")}, seconds(5, 55), 1},
		{"at the deadline, after slow_down asks for longer than a code lasts", map[string]string{"expires_in": "60"}, []answer{slowDown("7200")}, seconds(5, 55), 1},
		{"before the first poll", map[string]string{"expires_in": "3"}, []answer{token()}, seconds(3), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, code(tc.changes), tc.polls...)
			r := s.run()
			if !errors.Is(r.err, github.ErrCodeExpired) || r.err.Error() != "the GitHub code expired" {
				t.Fatalf("Login error = %v, want ErrCodeExpired alone", r.err)
			}
			wantWaits(t, s.clock, tc.waits)
			if n := s.count(tokenPath); n != tc.polled {
				t.Errorf("polls = %d, want %d", n, tc.polled)
			}
			deadline := start.Add(15 * time.Minute)
			if tc.changes != nil {
				d, _ := time.ParseDuration(tc.changes["expires_in"] + "s")
				deadline = start.Add(d)
			}
			for _, req := range s.all() {
				if !req.at.Before(deadline) {
					t.Errorf("GitHub got %s at %v, at or after the deadline %v", req.path, req.at, deadline)
				}
			}
		})
	}
}

func TestDeviceLoginRefusals(t *testing.T) {
	for _, tc := range []struct {
		code string
		is   error
		want string
	}{
		{quote("access_denied"), github.ErrAccessDenied, ""},
		{quote("device_flow_disabled"), github.ErrAppRefused, ""},
		{quote("incorrect_client_credentials"), github.ErrAppRefused, ""},
		{quote("incorrect_device_code"), nil, "GitHub refused the device login (incorrect_device_code)"},
		{quote("unsupported_grant_type"), nil, "GitHub refused the device login (unsupported_grant_type)"},
		{quote("brand_new_code"), nil, "GitHub refused the device login (brand_new_code)"},
		{quote("Bad-Code"), nil, "GitHub refused the device login"},
		{quote("bad\u001b[2Jcode"), nil, "GitHub refused the device login"},
		{quote(strings.Repeat("a", 65)), nil, "GitHub refused the device login"},
		{quote(""), nil, "GitHub refused the device login"},
		{"42", nil, "GitHub refused the device login"},
		{`{"code":"access_denied"}`, nil, "GitHub refused the device login"},
	} {
		for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
			t.Run(tc.code+" "+http.StatusText(status), func(t *testing.T) {
				s := newOAuth(t, code(nil), answer{status: status, body: errorReply(tc.code, map[string]string{"access_token": quote(deviceToken)})})
				r := s.run()
				checkRefusal(t, r.err, tc.is, tc.want)
				if n := s.count(tokenPath); n != 1 {
					t.Errorf("polls = %d, want 1", n)
				}
				wantWaits(t, s.clock, seconds(5))
			})
		}
	}
}

func checkRefusal(t *testing.T, err, is error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("Login succeeded, want an error")
	}
	if is != nil && !errors.Is(err, is) {
		t.Errorf("Login error = %v, want %v", err, is)
	}
	if want != "" && err.Error() != want {
		t.Errorf("Login error = %q, want %q", err, want)
	}
	if errors.Is(err, github.ErrCodeExpired) && is != github.ErrCodeExpired {
		t.Errorf("Login error = %v, want one that isn't ErrCodeExpired", err)
	}
}

func TestDeviceLoginCodeRequestRefusals(t *testing.T) {
	for _, tc := range []struct {
		code string
		is   error
		want string
	}{
		{"device_flow_disabled", github.ErrAppRefused, ""},
		{"incorrect_client_credentials", github.ErrAppRefused, ""},
		{"access_denied", github.ErrAccessDenied, ""},
		{"authorization_pending", nil, "GitHub refused the device login (authorization_pending)"},
		{"slow_down", nil, "GitHub refused the device login (slow_down)"},
		{"brand_new_code", nil, "GitHub refused the device login (brand_new_code)"},
		{"Bad Code", nil, "GitHub refused the device login"},
	} {
		for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
			t.Run(tc.code+" "+http.StatusText(status), func(t *testing.T) {
				s := newOAuth(t, refused(status, tc.code))
				r := s.run()
				checkRefusal(t, r.err, tc.is, tc.want)
				checkNoPrompt(t, s, r)
			})
		}
	}
}

func checkNoPrompt(t *testing.T, s *oauthServer, r result) {
	t.Helper()
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %+v, want none", r.prompts)
	}
	if n := s.count(tokenPath); n != 0 {
		t.Errorf("polls = %d, want none", n)
	}
	if n := s.count(codePath); n != 1 {
		t.Errorf("code requests = %d, want 1", n)
	}
	wantWaits(t, s.clock, nil)
}

func TestDeviceLoginRetries(t *testing.T) {
	s := newOAuth(t, code(nil),
		answer{status: http.StatusInternalServerError, body: "<html>oops</html>"},
		answer{status: http.StatusServiceUnavailable},
		answer{status: http.StatusTooManyRequests, body: `{"message":"slow down"}`},
		answer{drop: true},
		answer{status: http.StatusBadGateway},
		pending(),
		answer{status: http.StatusInternalServerError},
		answer{status: http.StatusServiceUnavailable, body: pending().body},
		token(),
	)
	wantToken(t, s.run())
	wantWaits(t, s.clock, seconds(5, 10, 20, 40, 60, 60, 5, 10, 5))
	if n := s.count(tokenPath); n != 9 {
		t.Errorf("polls = %d, want 9", n)
	}
}

func TestDeviceLoginBacksOffFromALongInterval(t *testing.T) {
	s := newOAuth(t, code(map[string]string{"interval": "90"}), answer{status: http.StatusInternalServerError}, token())
	wantToken(t, s.run())
	wantWaits(t, s.clock, seconds(90, 90))
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type firstPollTimesOut struct {
	next  http.RoundTripper
	timed atomic.Bool
}

func (rt *firstPollTimesOut) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == tokenPath && rt.timed.CompareAndSwap(false, true) {
		return nil, timeoutError{}
	}
	return rt.next.RoundTrip(r)
}

func TestDeviceLoginRetriesATimeout(t *testing.T) {
	s := newOAuth(t, code(nil), token())
	f := s.flow()
	client := *f.Client
	client.Transport = &firstPollTimesOut{next: client.Transport}
	f.Client = &client
	wantToken(t, s.login(t.Context(), f))
	wantWaits(t, s.clock, seconds(5, 10))
}

func TestDeviceLoginExpiresWhileRetrying(t *testing.T) {
	s := newOAuth(t, code(map[string]string{"expires_in": "30"}), answer{status: http.StatusServiceUnavailable, body: deviceCode})
	r := s.run()
	if !errors.Is(r.err, github.ErrCodeExpired) {
		t.Fatalf("Login error = %v, want ErrCodeExpired", r.err)
	}
	if want := "the GitHub code expired while hibd was retrying: GitHub answered 503 Service Unavailable"; r.err.Error() != want {
		t.Errorf("Login error = %q, want %q", r.err, want)
	}
	wantWaits(t, s.clock, seconds(5, 10, 15))
	if n := s.count(tokenPath); n != 2 {
		t.Errorf("polls = %d, want 2", n)
	}
}

func TestDeviceLoginForgetsAFailureAfterAnAnswer(t *testing.T) {
	s := newOAuth(t, code(map[string]string{"expires_in": "30"}), answer{status: http.StatusServiceUnavailable}, pending())
	r := s.run()
	if !errors.Is(r.err, github.ErrCodeExpired) || r.err.Error() != "the GitHub code expired" {
		t.Fatalf("Login error = %v, want ErrCodeExpired alone", r.err)
	}
	wantWaits(t, s.clock, seconds(5, 10, 5, 5, 5))
}

func TestDeviceLoginDoesntRetryTheCodeRequest(t *testing.T) {
	for name, a := range map[string]answer{
		"500":                    {status: http.StatusInternalServerError},
		"503":                    {status: http.StatusServiceUnavailable, body: codeReply(nil)},
		"429":                    {status: http.StatusTooManyRequests},
		"a closed connection":    {drop: true},
		"a 404 without an error": {status: http.StatusNotFound, body: "Not Found"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newOAuth(t, a)
			r := s.run()
			if r.err == nil {
				t.Fatal("Login succeeded, want an error")
			}
			checkNoPrompt(t, s, r)
		})
	}
}

func TestDeviceLoginFinalAnswers(t *testing.T) {
	big := tokenReply(map[string]string{"padding": quote(strings.Repeat(deviceCode, 1<<20/len(deviceCode)+1))})
	for _, tc := range []struct {
		name string
		poll answer
		is   error
		want string
	}{
		{"302 to a token", answer{status: http.StatusFound, location: "/elsewhere"}, nil, "redirect"},
		{"307 to a token", answer{status: http.StatusTemporaryRedirect, location: "/elsewhere"}, nil, "redirect"},
		{"404 without an error", answer{status: http.StatusNotFound, body: "Not Found"}, nil, "404"},
		{"400 without an error", answer{status: http.StatusBadRequest, body: "<html>" + deviceCode + "</html>"}, nil, "400"},
		{"an error and a token", answer{body: errorReply(quote("access_denied"), map[string]string{"access_token": quote(deviceToken)})}, github.ErrAccessDenied, ""},
		{"500 with an error", refused(http.StatusInternalServerError, "access_denied"), github.ErrAccessDenied, ""},
		{"302 with an error", answer{status: http.StatusFound, location: "/elsewhere", body: errorReply(quote("access_denied"), nil)}, github.ErrAccessDenied, ""},
		{"not JSON", answer{body: "access_token=" + deviceToken}, nil, "unreadable"},
		{"a JSON list", answer{body: `["` + deviceToken + `"]`}, nil, "unreadable"},
		{"an empty body", answer{}, nil, "unreadable"},
		{"over 1 MiB", answer{body: big}, nil, "1 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, code(nil), tc.poll, token())
			r := s.run()
			if r.err == nil {
				t.Fatal("Login succeeded, want an error")
			}
			if tc.is != nil && !errors.Is(r.err, tc.is) {
				t.Errorf("Login error = %v, want %v", r.err, tc.is)
			}
			if !strings.Contains(r.err.Error(), tc.want) {
				t.Errorf("Login error = %q, want it to say %q", r.err, tc.want)
			}
			if n := s.count(tokenPath); n != 1 {
				t.Errorf("polls = %d, want 1", n)
			}
			if n := s.count("/elsewhere"); n != 0 {
				t.Errorf("Login followed a redirect")
			}
		})
	}
}

func TestDeviceLoginRefusesARedirectForTheCode(t *testing.T) {
	s := newOAuth(t, answer{status: http.StatusFound, location: "/elsewhere"})
	r := s.run()
	if r.err == nil || !strings.Contains(r.err.Error(), "redirect") {
		t.Fatalf("Login error = %v, want a refused redirect", r.err)
	}
	checkNoPrompt(t, s, r)
	if n := s.count("/elsewhere"); n != 0 {
		t.Errorf("Login followed a redirect")
	}
}

func TestDeviceLoginChecksTheCode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]string
		want    string
	}{
		{"no device code", map[string]string{"device_code": ""}, "device code"},
		{"an empty device code", map[string]string{"device_code": `""`}, "device code"},
		{"a device code with a space", map[string]string{"device_code": quote(deviceCode + " x")}, "device code"},
		{"a device code with a slash", map[string]string{"device_code": quote(deviceCode + "/x")}, "device code"},
		{"a device code with a newline", map[string]string{"device_code": quote(deviceCode + "\n")}, "device code"},
		{"a long device code", map[string]string{"device_code": quote(strings.Repeat("a", 256))}, "device code"},
		{"a numeric device code", map[string]string{"device_code": "12345"}, "unreadable"},
		{"no user code", map[string]string{"user_code": ""}, "user code"},
		{"an empty user code", map[string]string{"user_code": `""`}, "user code"},
		{"a user code with an underscore", map[string]string{"user_code": quote("WDJB_MJHT")}, "user code"},
		{"a user code ending in a dash", map[string]string{"user_code": quote("WDJB-")}, "user code"},
		{"a user code starting with a dash", map[string]string{"user_code": quote("-WDJB")}, "user code"},
		{"a user code with two dashes", map[string]string{"user_code": quote("WDJB--MJHT")}, "user code"},
		{"a user code in five parts", map[string]string{"user_code": quote("A-B-C-D-E")}, "user code"},
		{"a user code part of 17", map[string]string{"user_code": quote(strings.Repeat("A", 17))}, "user code"},
		{"a user code with a space", map[string]string{"user_code": quote("WDJB MJHT")}, "user code"},
		{"a user code with an escape", map[string]string{"user_code": quote("WDJB\u001b[2J")}, "user code"},
		{"a user code with an accent", map[string]string{"user_code": quote("WDJB-MJHÉ")}, "user code"},
		{"no verification URI", map[string]string{"verification_uri": ""}, "verification URI"},
		{"another site", map[string]string{"verification_uri": quote("https://evil.example/login/device")}, "verification URI"},
		{"http", map[string]string{"verification_uri": quote("http://github.com/login/device")}, "verification URI"},
		{"a trailing slash", map[string]string{"verification_uri": quote(hibapi.GitHubDeviceURL + "/")}, "verification URI"},
		{"a query", map[string]string{"verification_uri": quote(hibapi.GitHubDeviceURL + "?code=WDJB-MJHT")}, "verification URI"},
		{"an upper-case scheme", map[string]string{"verification_uri": quote("HTTPS://github.com/login/device")}, "verification URI"},
		{"no expires_in", map[string]string{"expires_in": ""}, "expires_in"},
		{"expires_in 0", map[string]string{"expires_in": "0"}, "expires_in"},
		{"a negative expires_in", map[string]string{"expires_in": "-1"}, "expires_in"},
		{"expires_in over an hour", map[string]string{"expires_in": "3601"}, "expires_in"},
		{"a fractional expires_in", map[string]string{"expires_in": "900.5"}, "expires_in"},
		{"expires_in as a string", map[string]string{"expires_in": `"900"`}, "expires_in"},
		{"expires_in with an exponent", map[string]string{"expires_in": "9e2"}, "expires_in"},
		{"a huge expires_in", map[string]string{"expires_in": "99999999999999999999"}, "expires_in"},
		{"a null expires_in", map[string]string{"expires_in": "null"}, "expires_in"},
		{"a negative interval", map[string]string{"interval": "-1"}, "interval"},
		{"an interval over 15 minutes", map[string]string{"interval": "901"}, "interval"},
		{"a fractional interval", map[string]string{"interval": "5.5"}, "interval"},
		{"an interval as a string", map[string]string{"interval": `"5"`}, "interval"},
		{"an interval with an exponent", map[string]string{"interval": "5e0"}, "interval"},
		{"a huge interval", map[string]string{"interval": "99999999999999999999"}, "interval"},
		{"a boolean interval", map[string]string{"interval": "true"}, "interval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, code(tc.changes), token())
			r := s.run()
			if r.err == nil || !strings.Contains(r.err.Error(), tc.want) {
				t.Fatalf("Login error = %v, want one about the %s", r.err, tc.want)
			}
			checkNoPrompt(t, s, r)
		})
	}
}

func TestDeviceLoginChecksTheCodeReply(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"not JSON", "device_code=" + deviceCode, "unreadable"},
		{"a JSON list", `["` + deviceCode + `"]`, "unreadable"},
		{"empty", "", "unreadable"},
		{"over 1 MiB", codeReply(map[string]string{"padding": quote(strings.Repeat("x", 1<<20))}), "1 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, answer{body: tc.body})
			r := s.run()
			if r.err == nil || !strings.Contains(r.err.Error(), tc.want) {
				t.Fatalf("Login error = %v, want it to say %q", r.err, tc.want)
			}
			checkNoPrompt(t, s, r)
		})
	}
}

func TestDeviceLoginChecksTheToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]string
		is      error
	}{
		{"no token", map[string]string{"access_token": ""}, nil},
		{"an empty token", map[string]string{"access_token": `""`}, nil},
		{"a null token", map[string]string{"access_token": "null"}, nil},
		{"a numeric token", map[string]string{"access_token": "12345"}, nil},
		{"a token with a space", map[string]string{"access_token": quote(deviceToken + " x")}, nil},
		{"a token with a newline", map[string]string{"access_token": quote(deviceToken + "\n")}, nil},
		{"a long token", map[string]string{"access_token": quote(strings.Repeat("a", 256))}, nil},
		{"a refresh token", map[string]string{"refresh_token": quote("ghr_refresh")}, github.ErrExpiringToken},
		{"expires_in", map[string]string{"expires_in": "28800"}, github.ErrExpiringToken},
		{"both", map[string]string{"refresh_token": quote("ghr_refresh"), "expires_in": "28800", "refresh_token_expires_in": "15897600"}, github.ErrExpiringToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, code(nil), answer{body: tokenReply(tc.changes)})
			r := s.run()
			if r.err == nil {
				t.Fatal("Login succeeded, want an error")
			}
			if tc.is != nil && !errors.Is(r.err, tc.is) {
				t.Errorf("Login error = %v, want %v", r.err, tc.is)
			}
			if tc.is == nil && !strings.Contains(r.err.Error(), "token") {
				t.Errorf("Login error = %q, want it to be about the token", r.err)
			}
			if n := s.count(tokenPath); n != 1 {
				t.Errorf("polls = %d, want 1", n)
			}
		})
	}
}

func TestDeviceLoginWithoutAClientID(t *testing.T) {
	s := newOAuth(t, code(nil), token())
	f := s.flow()
	f.ClientID = ""
	r := s.login(t.Context(), f)
	if !errors.Is(r.err, github.ErrNoClientID) {
		t.Fatalf("Login error = %v, want ErrNoClientID", r.err)
	}
	if n := len(s.all()); n != 0 || len(r.prompts) != 0 {
		t.Errorf("Login made %d requests and %d prompts, want none", n, len(r.prompts))
	}
	if _, err := (&github.DeviceFlow{}).Login(t.Context(), func(github.DevicePrompt) { t.Error("prompted") }); !errors.Is(err, github.ErrNoClientID) {
		t.Errorf("the zero DeviceFlow's Login error = %v, want ErrNoClientID", err)
	}
}

func TestDeviceLoginStopsWithItsContext(t *testing.T) {
	t.Run("during a wait", func(t *testing.T) {
		s := newOAuth(t, code(nil), pending())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s.clock.onSleep = func(n int) {
			if n == 2 {
				cancel()
			}
		}
		r := s.login(ctx, s.flow())
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("Login error = %v, want context.Canceled", r.err)
		}
		if n := s.count(tokenPath); n != 1 {
			t.Errorf("polls = %d, want 1", n)
		}
	})
	t.Run("during a poll", func(t *testing.T) {
		s := newOAuth(t, code(nil), answer{hang: true}, token())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s.hook = func(r request) {
			if r.path == tokenPath {
				cancel()
			}
		}
		r := s.login(ctx, s.flow())
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("Login error = %v, want context.Canceled", r.err)
		}
		if n := s.count(tokenPath); n != 1 {
			t.Errorf("polls = %d, want 1", n)
		}
		wantWaits(t, s.clock, seconds(5))
	})
	t.Run("during the code request", func(t *testing.T) {
		s := newOAuth(t, answer{hang: true})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s.hook = func(request) { cancel() }
		r := s.login(ctx, s.flow())
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("Login error = %v, want context.Canceled", r.err)
		}
		checkNoPrompt(t, s, r)
	})
	t.Run("before it starts", func(t *testing.T) {
		s := newOAuth(t, code(nil), token())
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		r := s.login(ctx, s.flow())
		if !errors.Is(r.err, context.Canceled) || len(r.prompts) != 0 {
			t.Fatalf("Login error = %v after %d prompts, want context.Canceled and none", r.err, len(r.prompts))
		}
	})
}

func TestDeviceLoginErrorsNeverQuoteSecrets(t *testing.T) {
	echo := map[string]string{"access_token": quote(deviceToken), "device_code": quote(deviceCode), "refresh_token": quote(deviceToken)}
	for _, tc := range []struct {
		name  string
		code  answer
		polls []answer
		want  string
	}{
		{"a bad device code", code(map[string]string{"device_code": quote(deviceCode + "!")}), nil, ""},
		{"a bad user code", code(map[string]string{"user_code": quote(deviceCode + "!")}), nil, ""},
		{"a bad verification URI", code(map[string]string{"verification_uri": quote("https://evil.example/" + deviceCode)}), nil, ""},
		{"a bad expires_in", code(map[string]string{"expires_in": quote(deviceCode)}), nil, ""},
		{"a bad interval", code(map[string]string{"interval": quote(deviceCode)}), nil, ""},
		{"a code request refused", answer{status: http.StatusBadRequest, body: errorReply(quote("brand_new_code"), echo)}, nil, ""},
		{"a code request failed", answer{status: http.StatusInternalServerError, body: deviceCode}, nil, ""},
		{"an unknown code", code(nil), []answer{{body: errorReply(quote("brand_new_code"), echo)}}, ""},
		{"a malformed code", code(nil), []answer{{body: errorReply(quote(deviceCode), echo)}}, ""},
		{"a malformed code that's the token", code(nil), []answer{{body: errorReply(quote(deviceToken), echo)}}, ""},
		{"access_denied", code(nil), []answer{{body: errorReply(quote("access_denied"), echo)}}, ""},
		{"device_flow_disabled", code(nil), []answer{{body: errorReply(quote("device_flow_disabled"), echo)}}, ""},
		{"expired_token", code(nil), []answer{{body: errorReply(quote("expired_token"), echo)}}, ""},
		{"a 404", code(nil), []answer{{status: http.StatusNotFound, body: deviceCode + deviceToken}}, ""},
		{"a redirect", code(nil), []answer{{status: http.StatusFound, location: "/elsewhere?" + deviceCode, body: deviceToken}}, ""},
		{"a bad token", code(nil), []answer{{body: tokenReply(map[string]string{"access_token": quote(deviceToken + "!")})}}, ""},
		{"an expiring token", code(nil), []answer{{body: tokenReply(map[string]string{"refresh_token": quote(deviceToken), "expires_in": "28800"})}}, ""},
		{"not JSON", code(nil), []answer{{body: "access_token=" + deviceToken + "&device_code=" + deviceCode}}, ""},
		{"over 1 MiB", code(nil), []answer{{body: tokenReply(map[string]string{"padding": quote(strings.Repeat(deviceToken, 1<<20/len(deviceToken)+1))})}}, ""},
		{"expiry after failures", code(map[string]string{"expires_in": "20"}), []answer{{status: http.StatusServiceUnavailable, body: deviceCode}, {drop: true}}, ""},
		{"a device code that looks like an error code", code(map[string]string{"device_code": quote(lowerCode)}),
			[]answer{{body: errorReply(quote(lowerCode), nil)}}, hidden},
		{"a token that looks like an error code", code(nil),
			[]answer{{body: errorReply(quote(lowerToken), map[string]string{"access_token": quote(lowerToken)})}}, hidden},
		{"a refresh token that looks like an error code", code(nil),
			[]answer{{body: errorReply(quote(lowerToken), map[string]string{"refresh_token": quote(lowerToken)})}}, hidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newOAuth(t, tc.code, tc.polls...)
			r := s.run()
			if r.err == nil {
				t.Fatal("Login succeeded, want an error")
			}
			if tc.want != "" && r.err.Error() != tc.want {
				t.Errorf("Login error = %q, want %q", strings.ReplaceAll(strings.ReplaceAll(r.err.Error(), lowerCode, "<CODE>"), lowerToken, "<TOKEN>"), tc.want)
			}
		})
	}
}
