// Package github is what hibd knows about GitHub: the secret that holds the token, the one API
// call that checks a token and says whose it is, the checks on what GitHub says, and the
// connected account's profile file. It never puts a token in an error.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/version"
)

const (
	// SecretName is the name of the vault secret that holds the token.
	SecretName = "github"
	// Env is the variable that holds the token's placeholder in machines. gh reads it.
	Env = "GH_TOKEN"
	// APIBase is GitHub's API.
	APIBase = "https://api.github.com"
	// Timeout is how long a call to GitHub's API may take.
	Timeout = 10 * time.Second
)

// Sites returns the sites the token is bound to: git, the API, uploads, and the Copilot CLI's
// API hosts, which take the token itself. Each call returns a new slice.
func Sites() []string {
	return []string{"github.com", "api.github.com", "uploads.github.com", "*.githubcopilot.com"}
}

const maxBody = 1 << 20

// ErrUnauthorized means GitHub refused the token (401): it was revoked or has expired.
var ErrUnauthorized = errors.New("GitHub refused the token")

var (
	tokenRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)
	loginRE = regexp.MustCompile(`^[A-Za-z0-9](-?[A-Za-z0-9]){0,38}$`)
)

// CheckToken returns an error unless token looks like a GitHub token: 1 to 255 letters, digits
// and underscores, as every GitHub token format is. The error never quotes the token.
func CheckToken(token string) error {
	if !tokenRE.MatchString(token) {
		return errors.New("that isn't a GitHub token: want 1 to 255 letters, digits and underscores")
	}
	return nil
}

// CheckLogin returns an error unless login can be a GitHub login: 1 to 39 letters, digits and
// single hyphens, starting and ending with a letter or digit.
func CheckLogin(login string) error {
	if !loginRE.MatchString(login) {
		return errors.New("bad GitHub login: want 1 to 39 letters, digits and single hyphens, starting and ending with a letter or digit")
	}
	return nil
}

// Noreply returns the noreply address of the account with id and login, which links commits to
// it without its real address.
func Noreply(id int64, login string) string {
	return fmt.Sprintf("%d+%s@users.noreply.github.com", id, login)
}

const maxName = 255

// CleanName makes a profile's name fit for git's user.name. It drops control characters and
// invalid UTF-8, trims spaces, and cuts the name to 255 bytes. If nothing is left, or the name
// starts with "-", which a command could take for an option, it returns login instead.
func CleanName(name, login string) string {
	var b strings.Builder
	for len(name) > 0 {
		r, size := utf8.DecodeRuneInString(name)
		if !(r == utf8.RuneError && size == 1) && !unicode.IsControl(r) {
			b.WriteString(name[:size])
		}
		name = name[size:]
	}
	s := strings.TrimSpace(b.String())
	if len(s) > maxName {
		cut := maxName
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
	}
	if s == "" || strings.HasPrefix(s, "-") {
		return login
	}
	return s
}

// ParseScopes returns the OAuth scopes in an answer's X-OAuth-Scopes header, and whether the
// header says. Without it GitHub isn't saying, as for fine-grained and app tokens. An empty one
// means a token without scopes.
func ParseScopes(header http.Header) (scopes []string, known bool) {
	values := header.Values("X-OAuth-Scopes")
	if len(values) == 0 {
		return nil, false
	}
	for _, v := range values {
		for s := range strings.SplitSeq(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopes = append(scopes, s)
			}
		}
	}
	return scopes, true
}

// User is the account a token belongs to, as GetUser checked it.
type User struct {
	Login string
	ID    int64
	// Name is the profile's name, cleaned by CleanName.
	Name string
	// Scopes are the token's OAuth scopes, if ScopesKnown.
	Scopes      []string
	ScopesKnown bool
}

// NewClient returns the HTTP client for GitHub's API: it gives up after Timeout, and doesn't
// follow redirects.
func NewClient() *http.Client {
	return &http.Client{Timeout: Timeout, CheckRedirect: refuseRedirect}
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// GetUser asks GitHub's API at baseURL whose token this is, with GET /user. It returns an error
// wrapping ErrUnauthorized if GitHub refuses the token. It never follows a redirect, even if
// client does, gives up after Timeout, and reads at most 1 MiB. A nil client means NewClient().
// Its errors never quote the token or a request header.
func GetUser(ctx context.Context, client *http.Client, baseURL, token string) (User, error) {
	if err := CheckToken(token); err != nil {
		return User{}, err
	}
	u, err := getUser(ctx, client, baseURL, token)
	if err != nil && strings.Contains(err.Error(), token) {
		return User{}, errors.New("GitHub's answer was unreadable")
	}
	return u, err
}

func getUser(ctx context.Context, client *http.Client, baseURL, token string) (User, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/user", nil)
	if err != nil {
		return User{}, fmt.Errorf("bad GitHub API URL: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "hibernaut-hibd/"+version.Version)
	if client == nil {
		client = NewClient()
	}
	c := *client
	c.CheckRedirect = refuseRedirect
	resp, err := c.Do(req)
	if err != nil {
		return User{}, requestError(err)
	}
	defer resp.Body.Close()
	switch code := resp.StatusCode; {
	case code == http.StatusUnauthorized:
		return User{}, fmt.Errorf("%w (%s)", ErrUnauthorized, statusText(code))
	case code >= 300 && code < 400:
		return User{}, fmt.Errorf("GitHub answered %s, a redirect, which hibd doesn't follow", statusText(code))
	case code != http.StatusOK:
		return User{}, fmt.Errorf("GitHub answered %s", statusText(code))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return User{}, requestError(err)
	}
	if len(body) > maxBody {
		return User{}, errors.New("GitHub's answer is bigger than 1 MiB")
	}
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return User{}, fmt.Errorf("GitHub's answer was unreadable: %w", err)
	}
	if err := CheckLogin(user.Login); err != nil {
		return User{}, fmt.Errorf("GitHub's answer has a %w", err)
	}
	if user.ID <= 0 {
		return User{}, fmt.Errorf("GitHub's answer has a bad ID: %d", user.ID)
	}
	scopes, known := ParseScopes(resp.Header)
	return User{Login: user.Login, ID: user.ID, Name: CleanName(user.Name, user.Login), Scopes: scopes, ScopesKnown: known}, nil
}

func requestError(err error) error {
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return errors.New("GitHub didn't answer in time")
	}
	if uerr, ok := errors.AsType[*url.Error](err); ok {
		err = uerr.Err
	}
	return fmt.Errorf("can't reach GitHub: %w", err)
}

func statusText(code int) string {
	return strings.TrimSpace(fmt.Sprintf("%d %s", code, http.StatusText(code)))
}
