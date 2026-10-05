package main

import (
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
)

func TestColourOnlyOnTerminalsThatWantIt(t *testing.T) {
	for _, tc := range []struct {
		terminal bool
		env      map[string]string
		want     bool
	}{
		{true, map[string]string{"TERM": "xterm-256color"}, true},
		{true, nil, true},
		{false, map[string]string{"TERM": "xterm-256color"}, false},
		{true, map[string]string{"TERM": "dumb"}, false},
		{true, map[string]string{"NO_COLOR": "1"}, false},
		{true, map[string]string{"NO_COLOR": "yes"}, false},
		{true, map[string]string{"NO_COLOR": "false"}, false},
		{true, map[string]string{"NO_COLOR": ""}, true},
	} {
		if got := colourOn(tc.terminal, func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("terminal %v, env %v: colour %v, want %v", tc.terminal, tc.env, got, tc.want)
		}
	}
}

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plainAndColoured(t *testing.T, c *client, args ...string) (plain, coloured string) {
	t.Helper()
	outputs := make([]string, 2)
	for i, colour := range []bool{false, true} {
		c.colour = colour
		c.hib(args...)
		outputs[i] = c.out.String()
	}
	if strings.Contains(outputs[0], "\x1b") {
		t.Errorf("hib %s without colour printed escapes: %q", strings.Join(args, " "), outputs[0])
	}
	if got := ansiEscape.ReplaceAllString(outputs[1], ""); got != outputs[0] {
		t.Errorf("hib %s with colour, escapes removed:\n%s\nwant it the same as without colour:\n%s", strings.Join(args, " "), got, outputs[0])
	}
	return outputs[0], outputs[1]
}

func styled(t *testing.T, out, word string) string {
	t.Helper()
	m := regexp.MustCompile("(\x1b\\[[0-9;]*m)" + regexp.QuoteMeta(word) + "\x1b\\[m").FindStringSubmatch(out)
	if m == nil {
		t.Errorf("%q isn't styled in %q", word, out)
		return ""
	}
	return m[1]
}

func TestLsColoursStatesWithoutMovingColumns(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for name, state := range map[string]engine.State{"web": engine.Running, "nap": engine.Paused} {
		h.engine.Add(engine.Machine{ID: fake.NewID(), State: state, StartedAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour),
			Metadata: map[string]string{"hib.name": name, "hib.image": "dev"}})
	}

	_, coloured := plainAndColoured(t, c, "ls")
	running, paused := styled(t, coloured, "running"), styled(t, coloured, "paused")
	if running == paused {
		t.Errorf("running and paused look the same: %q", running)
	}
}

func TestStatusColoursHealthWithoutMovingColumns(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	status := hibapi.Status{Version: "v-test", E2B: hibapi.Health{OK: true},
		Codex: &hibapi.Codex{Email: "octo@example.com", Plan: "plus", Health: hibapi.CodexDead, Problem: "the login was revoked"},
		Secrets: []hibapi.SecretHealth{
			{Name: "github", Sites: []string{"api.github.com"}, State: hibapi.SecretOK},
			{Name: "openai", Sites: []string{"api.openai.com"}, State: hibapi.SecretRevoked, Detail: "401"},
		}}
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{Status: &status}), true
	}

	_, coloured := plainAndColoured(t, c, "status")
	ok, dead, revoked := styled(t, coloured, "ok"), styled(t, coloured, "dead"), styled(t, coloured, "revoked")
	if ok == dead || dead != revoked {
		t.Errorf("ok %q, dead %q and revoked %q: want failing states alike and unlike ok", ok, dead, revoked)
	}

	status.E2B = hibapi.Health{Detail: "connection refused"}
	_, coloured = plainAndColoured(t, c, "status")
	if down := styled(t, coloured, "down"); down != dead {
		t.Errorf("E2B down %q, want it styled as failing %q", down, dead)
	}
}

func TestJSONIsNeverColoured(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	c.colour = true
	for _, args := range [][]string{{"new", "web", "--json"}, {"ls", "--json"}, {"status", "--json"}} {
		c.hib(args...)
		if strings.Contains(c.out.String(), "\x1b") || strings.Contains(c.out.String(), "\\u001b") {
			t.Errorf("hib %s printed colour: %q", strings.Join(args, " "), c.out)
		}
	}
}
