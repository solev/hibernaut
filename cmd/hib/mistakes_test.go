package main

import (
	"strings"
	"testing"
)

func TestMistakesHaveOneShape(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "app", "--imag", "x"); code != 2 || c.out.Len() > 0 || c.errOut.String() !=
		"hib new: unknown flag --imag\nusage: hib new NAME [--image IMAGE] [--size SIZE] [--idle 15m]\nrun hib help new\n" {
		t.Errorf("hib new app --imag x = %d, stdout %q, stderr:\n%s", code, c.out, c.errOut)
	}
	for _, tc := range []struct {
		args []string
		path string
		msg  string
	}{
		{[]string{"new"}, "new", "missing NAME"},
		{[]string{"new", "a", "b"}, "new", "unexpected argument b"},
		{[]string{"new", "app", "--idle"}, "new", "--idle needs a value"},
		{[]string{"new", "app", "-idle", "soon"}, "new", `invalid value "soon" for --idle: parse error`},
		{[]string{"new", "App"}, "new", "bad name"},
		{[]string{"new", "app", "--size", "huge"}, "new", `no size "huge" (there is: small, medium, large)`},
		{[]string{"fork", "app", "--count", "many"}, "fork", `invalid value "many" for --count: parse error`},
		{[]string{"fork", "app", "--count", "11"}, "fork", "bad copy count 11: use 1 to 10"},
		{[]string{"rm", "-oProxyCommand=x"}, "rm", "unknown flag --oProxyCommand"},
		{[]string{"rm", "--yes=maybe", "app"}, "rm", `invalid value "maybe" for --yes: parse error`},
		{[]string{"log", "--n", "-1"}, "log", "--n must be non-negative"},
		{[]string{"ls", "extra"}, "ls", "unexpected argument extra"},
		{[]string{"ls", "---x"}, "ls", "bad flag ---x"},
		{[]string{"exec", "app", "id"}, "exec", "unexpected argument id"},
		{[]string{"exec", "app", "--"}, "exec", "missing CMD"},
		{[]string{"exec", "a.b", "--", "id"}, "exec", "bad name"},
		{[]string{"image", "save", "web"}, "image save", "missing NAME"},
		{[]string{"image", "build", "../dev"}, "image build", "bad name"},
		{[]string{"secret", "add"}, "secret add", "missing NAME"},
		{[]string{"bridge", "app"}, "bridge", "want NAME.hib"},
		{[]string{"version", "extra"}, "version", "unexpected argument extra"},
		{[]string{"help", "nwe"}, "help", "unknown command nwe. Did you mean new?"},
		{[]string{"help", "image", "biuld"}, "help", "unknown command biuld. Did you mean build?"},
		{[]string{"status", "--unknown\t\x1b[2J"}, "status", `unknown flag --unknown\x09\x1b[2J`},
		{[]string{"--bogus"}, "", "unknown flag --bogus"},
	} {
		code := c.hib(tc.args...)
		lines := strings.Split(c.errOut.String(), "\n")
		title := strings.TrimSpace("hib " + tc.path)
		if code != 2 || c.out.Len() > 0 || len(lines) != 4 || lines[3] != "" ||
			!strings.HasPrefix(lines[0], title+": ") || !strings.Contains(lines[0], tc.msg) ||
			!strings.HasPrefix(lines[1], "usage: "+title) ||
			lines[2] != strings.TrimSpace("run hib help "+tc.path) {
			t.Errorf("hib %q = %d, stdout %q, stderr:\n%s\nwant 2 and %s: ...%s...", tc.args, code, c.out, c.errOut, title, tc.msg)
		}
		if strings.Contains(c.errOut.String(), "Usage of") || strings.Contains(c.errOut.String(), "\x1b") {
			t.Errorf("hib %q printed Go's usage or a raw escape:\n%s", tc.args, c.errOut)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("mistakes reached the host: %q", calls)
	}
}

func TestUnknownCommandsGetTheNearestOne(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"nwe"}, "hib: unknown command nwe. Did you mean new?\nusage: hib [--config DIR] COMMAND [ARG...]\nrun hib help\n"},
		{[]string{"xyzzy"}, "hib: unknown command xyzzy\nusage: hib [--config DIR] COMMAND [ARG...]\nrun hib help\n"},
		{[]string{"stat"}, "hib: unknown command stat. Did you mean status?\nusage: hib [--config DIR] COMMAND [ARG...]\nrun hib help\n"},
		{[]string{"image", "biuld"}, "hib image: unknown command biuld. Did you mean build?\nusage: hib image build|save|ls|rm\nrun hib help image\n"},
		{[]string{"secret", "sl"}, "hib secret: unknown command sl. Did you mean ls?\nusage: hib secret add|ls|rm\nrun hib help secret\n"},
		{[]string{"connect", "gihtub"}, "hib connect: unknown command gihtub. Did you mean github?\nusage: hib connect github|codex\nrun hib help connect\n"},
		{[]string{"disconnect", "codx"}, "hib disconnect: unknown command codx. Did you mean codex?\nusage: hib disconnect codex|github\nrun hib help disconnect\n"},
		{[]string{"completion", "zhs"}, "hib completion: unknown command zhs. Did you mean zsh?\nusage: hib completion bash|zsh|fish\nrun hib help completion\n"},
		{[]string{"__complet"}, "hib: unknown command __complet\nusage: hib [--config DIR] COMMAND [ARG...]\nrun hib help\n"},
		{[]string{"n\u202ew"}, "hib: unknown command n\\u202ew. Did you mean new?\nusage: hib [--config DIR] COMMAND [ARG...]\nrun hib help\n"},
	} {
		if code := c.hib(tc.args...); code != 2 || c.out.Len() > 0 || c.errOut.String() != tc.want {
			t.Errorf("hib %q = %d, stdout %q, stderr:\n%s\nwant:\n%s", tc.args, code, c.out, c.errOut, tc.want)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("unknown commands reached the host: %q", calls)
	}
}

func TestNewFlagsAreAccepted(t *testing.T) {
	h := imageHost(t)
	c := initClient(t, h, true)
	if code := c.hib("new", "app", "--json"); code != 0 {
		t.Fatalf("new --json = %d: %s", code, c.errOut)
	}
	for _, args := range [][]string{
		{"ls", "--json"}, {"secret", "ls", "--json"}, {"log", "--json"}, {"image", "ls", "--json"},
		{"fork", "app", "--json"}, {"rm", "app-1", "--yes"},
	} {
		if code := c.hib(args...); code != 0 {
			t.Errorf("hib %q = %d: %s", args, code, c.errOut)
		}
	}
	if code := c.hib("status", "--json"); code == 2 {
		t.Errorf("hib status --json = 2: %s", c.errOut)
	}
	if code := c.hib("image", "build", "dev", "--verbose", "--bogus"); code != 2 || !strings.Contains(c.errOut.String(), "unknown flag --bogus") {
		t.Errorf("hib image build --verbose --bogus = %d: %s", code, c.errOut)
	}
}
