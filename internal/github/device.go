package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/version"
)

const (
	// OAuthBase is GitHub's site, where the device flow asks for a code and polls for the token.
	OAuthBase = "https://github.com"
	// ClientID is the client ID of the Hibernaut OAuth App, which the device flow logs in to. It
	// isn't secret, and the device flow needs no client secret.
	ClientID = "Ov23li7DEIl4LNsMzS1o"
)

// DeviceScopes returns the scopes the device flow asks for: repo to clone and push, read:org for
// gh's org commands, and workflow to push changes under .github/workflows. Each call returns a
// new slice.
func DeviceScopes() []string {
	return []string{"repo", "read:org", "workflow"}
}

var (
	// ErrNoClientID means the device flow has no OAuth App client ID, so it can't start.
	ErrNoClientID = errors.New("no GitHub OAuth App client ID")
	// ErrCodeExpired means the code expired before the user approved it.
	ErrCodeExpired = errors.New("the GitHub code expired")
	// ErrAccessDenied means the user denied the login on GitHub.
	ErrAccessDenied = errors.New("the GitHub login was denied")
	// ErrAppRefused means GitHub refused the OAuth App: its Device Flow is off, or its client ID
	// is wrong.
	ErrAppRefused = errors.New("GitHub refused the OAuth App")
	// ErrExpiringToken means GitHub gave a token that expires. hibd can't refresh tokens, so it
	// refuses them.
	ErrExpiringToken = errors.New("GitHub gave a token that expires")
)

// DevicePrompt is what the user needs to approve a device login: the code to enter at
// hibapi.GitHubDeviceURL, and how long it lasts.
type DevicePrompt struct {
	UserCode  string
	ExpiresIn time.Duration
}

// DeviceFlow logs in to an OAuth App with GitHub's device flow. Its zero value has no client ID,
// so its Login fails with ErrNoClientID.
type DeviceFlow struct {
	// Client is the HTTP client. If it's nil, Login uses NewClient(). Login never follows a
	// redirect, even if Client does.
	Client *http.Client
	// Base is GitHub's site. If it's "", Login uses OAuthBase.
	Base string
	// ClientID is the OAuth App's client ID. If it's "", Login fails with ErrNoClientID. It never
	// falls back to the ClientID constant.
	ClientID string
	// Now is the clock. If it's nil, Login uses time.Now.
	Now func() time.Time
	// Sleep waits for d, or until ctx is done, when it returns ctx's error. If it's nil, Login
	// uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Login runs the device flow for DeviceScopes. It asks GitHub for a code, calls prompt once with
// it, then polls until the user approves or denies it, the code expires, or ctx is done. It
// returns a token that passes CheckToken. Its errors wrap ErrNoClientID, ErrCodeExpired,
// ErrAccessDenied, ErrAppRefused, ErrExpiringToken or ctx's error where they apply, and never
// quote the device code or the token.
func (f *DeviceFlow) Login(ctx context.Context, prompt func(DevicePrompt)) (string, error) {
	if f.ClientID == "" {
		return "", ErrNoClientID
	}
	client := f.Client
	if client == nil {
		client = NewClient()
	}
	l := &deviceLogin{
		client:   *client,
		base:     strings.TrimSuffix(cmp.Or(f.Base, OAuthBase), "/"),
		clientID: f.ClientID,
		now:      f.Now,
		sleep:    f.Sleep,
	}
	l.client.CheckRedirect = refuseRedirect
	if l.now == nil {
		l.now = time.Now
	}
	if l.sleep == nil {
		l.sleep = sleep
	}
	token, err := l.run(ctx, prompt)
	if err != nil {
		return "", l.hide(err)
	}
	return token, nil
}

const (
	codePath      = "/login/device/code"
	tokenPath     = "/login/oauth/access_token"
	deviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	firstInterval = 5 * time.Second
	slowerBy      = 5 * time.Second
	maxExpiresIn  = 3600
	maxInterval   = 900
	maxBackoff    = time.Minute
)

var (
	userCodeRE   = regexp.MustCompile(`^[A-Za-z0-9]{1,16}(-[A-Za-z0-9]{1,16}){0,3}$`)
	deviceCodeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
	errorCodeRE  = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

type deviceLogin struct {
	client   http.Client
	base     string
	clientID string
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	secrets  []string
}

type deviceCode struct {
	code     string
	deadline time.Time
	interval time.Duration
}

type oauthReply struct {
	body       []byte
	oauthError bool
	code       string
	interval   json.RawMessage
}

type retryError struct{ err error }

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

func (l *deviceLogin) run(ctx context.Context, prompt func(DevicePrompt)) (string, error) {
	code, p, err := l.askCode(ctx)
	if err != nil {
		return "", err
	}
	prompt(p)
	return l.poll(ctx, code)
}

func (l *deviceLogin) askCode(ctx context.Context) (deviceCode, DevicePrompt, error) {
	r, err := l.post(ctx, codePath, url.Values{"client_id": {l.clientID}, "scope": {strings.Join(DeviceScopes(), " ")}})
	if err != nil {
		return deviceCode{}, DevicePrompt{}, err
	}
	if r.oauthError {
		return deviceCode{}, DevicePrompt{}, refusal(r.code)
	}
	var reply struct {
		DeviceCode      string          `json:"device_code"`
		UserCode        string          `json:"user_code"`
		VerificationURI string          `json:"verification_uri"`
		ExpiresIn       json.RawMessage `json:"expires_in"`
		Interval        json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(r.body, &reply); err != nil {
		return deviceCode{}, DevicePrompt{}, errUnreadable
	}
	now := l.now()
	expiresIn, ok := seconds(reply.ExpiresIn, 1, maxExpiresIn)
	switch {
	case !deviceCodeRE.MatchString(reply.DeviceCode):
		return deviceCode{}, DevicePrompt{}, errors.New("GitHub's answer has a bad device code")
	case !userCodeRE.MatchString(reply.UserCode):
		return deviceCode{}, DevicePrompt{}, errors.New("GitHub's answer has a bad user code")
	case reply.VerificationURI != hibapi.GitHubDeviceURL:
		return deviceCode{}, DevicePrompt{}, errors.New("GitHub's answer has a bad verification URI")
	case !ok:
		return deviceCode{}, DevicePrompt{}, errors.New("GitHub's answer has a bad expires_in")
	}
	interval := firstInterval
	if present(reply.Interval) {
		n, ok := seconds(reply.Interval, 0, maxInterval)
		if !ok {
			return deviceCode{}, DevicePrompt{}, errors.New("GitHub's answer has a bad interval")
		}
		interval = cmp.Or(n, firstInterval)
	}
	code := deviceCode{code: reply.DeviceCode, deadline: now.Add(expiresIn), interval: interval}
	return code, DevicePrompt{UserCode: reply.UserCode, ExpiresIn: expiresIn}, nil
}

func (l *deviceLogin) poll(ctx context.Context, code deviceCode) (string, error) {
	form := url.Values{"client_id": {l.clientID}, "device_code": {code.code}, "grant_type": {deviceGrant}}
	interval, wait := code.interval, code.interval
	var failure error
	for {
		remaining := code.deadline.Sub(l.now())
		if remaining <= 0 {
			return "", expired(failure)
		}
		if err := l.sleep(ctx, min(wait, remaining)); err != nil {
			return "", stopped(cmp.Or(ctx.Err(), err))
		}
		if !l.now().Before(code.deadline) {
			return "", expired(failure)
		}
		r, err := l.post(ctx, tokenPath, form)
		if retry, ok := errors.AsType[*retryError](err); ok {
			failure = retry.err
			wait = max(interval, min(2*wait, maxBackoff))
			continue
		}
		if err != nil {
			return "", err
		}
		failure = nil
		switch {
		case !r.oauthError:
			return l.token(r.body)
		case r.code == "authorization_pending":
		case r.code == "slow_down":
			interval = max(interval+slowerBy, slowerInterval(r.interval))
		default:
			return "", refusal(r.code)
		}
		wait = interval
	}
}

func (l *deviceLogin) token(body []byte) (string, error) {
	var reply struct {
		AccessToken  json.RawMessage `json:"access_token"`
		RefreshToken json.RawMessage `json:"refresh_token"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return "", errUnreadable
	}
	if present(reply.RefreshToken) || present(reply.ExpiresIn) {
		return "", ErrExpiringToken
	}
	var token string
	if json.Unmarshal(reply.AccessToken, &token) != nil || CheckToken(token) != nil {
		return "", errors.New("GitHub's answer has no valid token")
	}
	return token, nil
}

func (l *deviceLogin) post(ctx context.Context, path string, form url.Values) (oauthReply, error) {
	reqCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, l.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthReply{}, fmt.Errorf("bad GitHub URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hibernaut-hibd/"+version.Version)
	resp, err := l.client.Do(req)
	if err != nil {
		return oauthReply{}, l.failed(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return oauthReply{}, l.failed(ctx, err)
	}
	if len(body) > maxBody {
		return oauthReply{}, errors.New("GitHub's answer is bigger than 1 MiB")
	}
	var fields struct {
		Error        json.RawMessage `json:"error"`
		Interval     json.RawMessage `json:"interval"`
		DeviceCode   json.RawMessage `json:"device_code"`
		AccessToken  json.RawMessage `json:"access_token"`
		RefreshToken json.RawMessage `json:"refresh_token"`
	}
	parsed := json.Unmarshal(body, &fields) == nil
	if parsed {
		l.remember(fields.DeviceCode, fields.AccessToken, fields.RefreshToken)
	}
	switch code := resp.StatusCode; {
	case code >= 300 && code < 400:
		return oauthReply{}, fmt.Errorf("GitHub answered %s, a redirect, which hibd doesn't follow", statusText(code))
	case code >= 500 || code == http.StatusTooManyRequests:
		return oauthReply{}, &retryError{fmt.Errorf("GitHub answered %s", statusText(code))}
	}
	// GitHub sends OAuth errors on HTTP 200 as well as 400.
	if parsed && present(fields.Error) {
		return oauthReply{oauthError: true, code: errorCode(fields.Error), interval: fields.Interval}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return oauthReply{}, fmt.Errorf("GitHub answered %s", statusText(resp.StatusCode))
	}
	return oauthReply{body: body}, nil
}

func (l *deviceLogin) failed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return stopped(ctx.Err())
	}
	return &retryError{requestError(err)}
}

func (l *deviceLogin) remember(values ...json.RawMessage) {
	for _, raw := range values {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			l.secrets = append(l.secrets, s)
		}
	}
}

// GitHub could echo a secret as an error code, and errors show error codes.
func (l *deviceLogin) hide(err error) error {
	for _, s := range l.secrets {
		if strings.Contains(err.Error(), s) {
			return errUnreadable
		}
	}
	return err
}

var errUnreadable = errors.New("GitHub's answer was unreadable")

func refusal(code string) error {
	switch code {
	case "expired_token":
		return ErrCodeExpired
	case "access_denied":
		return ErrAccessDenied
	case "device_flow_disabled", "incorrect_client_credentials":
		return fmt.Errorf("%w (%s)", ErrAppRefused, code)
	case "":
		return errors.New("GitHub refused the device login")
	}
	return fmt.Errorf("GitHub refused the device login (%s)", code)
}

func errorCode(raw json.RawMessage) string {
	var code string
	if json.Unmarshal(raw, &code) != nil || !errorCodeRE.MatchString(code) {
		return ""
	}
	return code
}

func expired(failure error) error {
	if failure == nil {
		return ErrCodeExpired
	}
	return fmt.Errorf("%w while hibd was retrying: %w", ErrCodeExpired, failure)
}

func stopped(err error) error {
	return fmt.Errorf("stopped waiting for GitHub: %w", err)
}

func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func seconds(raw json.RawMessage, low, high int64) (time.Duration, bool) {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < low || n > high {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

func slowerInterval(raw json.RawMessage) time.Duration {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(min(n, maxExpiresIn)) * time.Second
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
