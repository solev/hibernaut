package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSSH(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	for _, tc := range []struct{ args, want []string }{
		{[]string{"ssh", "app"}, []string{"--", "app.hib"}},
		{[]string{"ssh", "app", "--"}, []string{"--", "app.hib"}},
		{[]string{"ssh", "app", "--", "ls", "-la", "--", "-x"}, []string{"--", "app.hib", "ls", "-la", "--", "-x"}},
	} {
		c.execs = nil
		if code := c.hib(tc.args...); code != 0 || len(c.execs) != 1 || !slices.Equal(c.execs[0], tc.want) {
			t.Errorf("hib %q = %d, ran ssh %q, want %q: %s", tc.args, code, c.execs, tc.want, c.errOut)
		}
	}

	c.execs = nil
	for _, tc := range []struct {
		args []string
		msg  string
	}{
		{[]string{"ssh"}, "missing NAME"},
		{[]string{"ssh", "App"}, "bad name"},
		{[]string{"ssh", "a;b"}, "bad name"},
		{[]string{"ssh", "-oProxyCommand=x"}, "unknown flag --oProxyCommand"},
		{[]string{"ssh", "app", "uptime"}, "unexpected argument uptime"},
	} {
		if code := c.hib(tc.args...); code != 2 || !strings.HasPrefix(c.errOut.String(), "hib ssh: ") || !strings.Contains(c.errOut.String(), tc.msg) {
			t.Errorf("hib %q = %d, stderr %q; want 2 and %q", tc.args, code, c.errOut, tc.msg)
		}
	}
	if len(c.execs) != 0 {
		t.Errorf("mistakes ran ssh: %q", c.execs)
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("hib ssh reached the host itself: %q", calls)
	}

	c2 := newClient(t, h)
	own := filepath.Join(t.TempDir(), "my ssh config")
	if code := c2.hib("init", "-host", testHost, "-ssh-config", own); code != 0 {
		t.Fatalf("init exit %d: %s", code, c2.errOut)
	}
	if code := c2.hib("ssh", "app"); code != 0 || len(c2.execs) != 1 || !slices.Equal(c2.execs[0], []string{"-F", own, "--", "app.hib"}) {
		t.Errorf("hib ssh app with an ssh config = %d, ran %q", code, c2.execs)
	}
}
