package task

import (
	"strings"
	"testing"
)

func TestParseRepo(t *testing.T) {
	for _, s := range []string{"O/R", "Acme-Tools/api.v2_test", "o/.github", "o/-",
		strings.Repeat("a", 39) + "/" + strings.Repeat("n", 100)} {
		r, err := ParseRepo(s)
		if err != nil || r.String() != s {
			t.Errorf("ParseRepo(%q) = %+v, %v; want unchanged OWNER/NAME", s, r, err)
		}
	}
	for _, s := range []string{"", "o", "/r", "o/", "o/r/extra", "https://github.com/o/r",
		"git@github.com:o/r.git", "o/r.git", "o/r.GIT", "-o/r", "o-/r", "o--x/r",
		"o_x/r", "o/.", "o/..", "o/r?", "o/r#x", "o/r%20", "o/r x", "o/r\n", "o/r\x00",
		"é/r", "o/é", "o/\xff", strings.Repeat("a", 40) + "/r", "o/" + strings.Repeat("n", 101)} {
		r, err := ParseRepo(s)
		if err == nil || !strings.Contains(err.Error(), "bad repo") || r != (Repo{}) {
			t.Errorf("ParseRepo(%q) = %+v, %v; want bad repo and no parsed value", s, r, err)
		}
	}
}

func TestCheckBase(t *testing.T) {
	for _, base := range []string{"", "main", "release/v1.2", "feature/-flag", "foo./bar", "é",
		strings.Repeat("a", 255), strings.Repeat("é", 127) + "a"} {
		if err := CheckBase(base); err != nil {
			t.Errorf("CheckBase(%q) = %v; want a valid base", base, err)
		}
	}
	for _, base := range []string{"-main", "a..b", "HEAD", "@", "a@{b", "a b", "a\x00b", "a\nb",
		"a\x1fb", "a\x7fb", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`,
		"/main", "main/", "a//b", ".main", "a/.b", "main.", "a.lock", "a.lock/b",
		"bad\xff", strings.Repeat("a", 256), strings.Repeat("é", 128)} {
		if err := CheckBase(base); err == nil || !strings.Contains(err.Error(), "bad base branch") {
			t.Errorf("CheckBase(%q) = %v; want bad base branch", base, err)
		}
	}
}
