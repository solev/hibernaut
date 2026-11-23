package task

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	repoOwner = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

func ParseRepo(s string) (Repo, error) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || len(owner) > 39 || !repoOwner.MatchString(owner) ||
		len(name) > 100 || !repoName.MatchString(name) || name == "." || name == ".." ||
		strings.HasSuffix(strings.ToLower(name), ".git") {
		return Repo{}, fmt.Errorf("bad repo %q: want GitHub OWNER/NAME, not a URL or .git suffix", s)
	}
	return Repo{Owner: owner, Name: name}, nil
}

func CheckBase(base string) error {
	if base == "" {
		return nil
	}
	bad := len(base) > 255 || !utf8.ValidString(base) || base[0] == '-' ||
		base == "HEAD" || base == "@" || strings.HasSuffix(base, ".") ||
		strings.Contains(base, "..") || strings.Contains(base, "@{") ||
		strings.ContainsAny(base, `~^:?*[\`)
	for _, c := range []byte(base) {
		bad = bad || c <= ' ' || c == 127
	}
	for part := range strings.SplitSeq(base, "/") {
		bad = bad || part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock")
	}
	if bad {
		return fmt.Errorf("bad base branch %q: want a Git branch name of at most 255 bytes", base)
	}
	return nil
}
