package main

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestStatusRunningHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if !slices.Equal(words, []string{"sudo", "-n", "hibd", "status", "--json"}) {
			t.Error("status didn't run sudo hibd status --json")
			return 1, true
		}

		if h.lastStdin != nil {
			t.Error("status sent stdin to ssh")
		}
		return writeTestResponse(stdout, hibapi.Response{Status: &hibapi.Status{
			Version: "v-test", E2B: hibapi.Health{OK: true},
			GitHub: &hibapi.GitHub{Login: "octocat", Email: "1+octocat@users.noreply.github.com"},
			Codex: &hibapi.Codex{Email: "octo@example.com", Plan: "plus", AccountID: "acct-1", Health: hibapi.CodexOK,
				Expires: time.Date(2026, 10, 14, 6, 30, 0, 0, time.UTC)},
			Secrets: []hibapi.SecretHealth{{
				Name: "github", Sites: []string{"api.github.com"}, State: hibapi.SecretOK,
			}},
		}}), true
	}
	if code := c.hib("status"); code != 0 {
		t.Fatalf("status exit %d: %s", code, c.errOut)
	}
	want := "host    " + testHost + "\n" +
		"hibd    v-test\n" +
		"E2B     ok\n" +
		"GitHub  octocat, commits as 1+octocat@users.noreply.github.com\n" +
		"Codex   octo@example.com, plus plan, token expires 2026-10-14 06:30 UTC\n\n" +
		"SECRET  SITES           STATE  DETAIL\n" +
		"github  api.github.com  ok     -\n"
	if c.out.String() != want || c.errOut.Len() != 0 {
		t.Errorf("status output = %q, want %q", c.out.String(), want)
	}
	calls := h.takeCalls()
	if len(calls) != 2 || !slices.Equal(calls[0], []string{
		"az", "vm", "get-instance-view", "-g", testGroup, "-n", testVM,
		"--query", powerStateQuery, "-o", "tsv",
	}) {
		t.Errorf("status calls = %q, expected Azure before ssh", calls)
	}
	if got := remoteCommands(calls); !slices.Equal(got, []string{"sudo -n hibd 'status' '--json'"}) {
		t.Errorf("status remote commands = %q", got)
	}
}

func TestStatusAsleepHostNeverUsesSSHOrStartsAzure(t *testing.T) {
	for _, tc := range []struct {
		name, state, printed string
	}{
		{"deallocated", "VM deallocated", "VM deallocated"},
		{"stopped", "VM stopped", "VM stopped"},
		{"stopping", "VM stopping", "VM stopping"},
		{"deallocating", "VM deallocating", "VM deallocating"},
		{"starting", "VM starting", "VM starting"},
		{"terminal controls", "VM deallocated\x1b[2J\nforged", "VM deallocated\\x1b[2J forged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			h.sleep(tc.state)
			code := c.hib("status")
			want := "host    " + testHost + ": asleep (" + tc.printed + "). Using a machine wakes it.\n"
			if code != 0 || c.out.String() != want || c.errOut.Len() != 0 {
				t.Errorf("exit %d, stdout %q, stderr %q; want %q", code, c.out.String(), c.errOut.String(), want)
			}
			calls := h.takeCalls()
			if len(calls) != 1 || calls[0][0] != "az" || calls[0][2] != "get-instance-view" {
				t.Errorf("asleep status calls = %q, want only az get-instance-view", calls)
			}
			if strings.Count(c.out.String(), "\n") != 1 {
				t.Error("asleep status didn't print exactly one host line")
			}
		})
	}
}

func TestStatusReportsHealthAndProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response hibapi.Response
		code     int
		detail   string
	}{
		{"revoked GitHub", hibapi.Response{Status: &hibapi.Status{
			Version: "v-test", E2B: hibapi.Health{OK: true},
			Secrets: []hibapi.SecretHealth{{
				Name: "github", Sites: []string{"api.github.com"}, State: hibapi.SecretRevoked,
				Detail: "revoked: run hib connect github --from-gh again",
			}},
		}}, 1, "revoked: run hib connect github --from-gh again"},
		{"E2B down", hibapi.Response{Status: &hibapi.Status{
			Version: "v-test", E2B: hibapi.Health{Detail: "API unavailable"},
		}}, 1, "down: API unavailable"},
		{"secret check failed", hibapi.Response{Status: &hibapi.Status{
			E2B: hibapi.Health{OK: true}, Secrets: []hibapi.SecretHealth{{
				Name: "github", State: hibapi.SecretFailed, Detail: "GitHub didn't answer",
			}},
		}}, 1, "GitHub didn't answer"},
		{"unchecked secret", hibapi.Response{Status: &hibapi.Status{
			E2B: hibapi.Health{OK: true}, Secrets: []hibapi.SecretHealth{{
				Name: "custom", State: hibapi.SecretUnchecked, Detail: "no check for this site",
			}},
		}}, 0, "no check for this site"},
		{"stuck Codex", hibapi.Response{Status: &hibapi.Status{
			E2B: hibapi.Health{OK: true}, Codex: &hibapi.Codex{Email: "octo@example.com", Plan: "plus", Health: hibapi.CodexStuck,
				Problem: "a refresh was interrupted: run hib connect codex"},
		}}, 1, "stuck: a refresh was interrupted: run hib connect codex"},
		{"dead Codex", hibapi.Response{Status: &hibapi.Status{
			E2B: hibapi.Health{OK: true}, Codex: &hibapi.Codex{Email: "octo@example.com", Plan: "plus", Health: hibapi.CodexDead},
		}}, 1, "dead: run hib connect codex"},
		{"missing status", hibapi.Response{}, 1, "hibd didn't report a status"},
		{"hibd error", hibapi.Response{Error: "repair the vault\x1b[2J"}, 1, "repair the vault\\x1b[2J"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h, true)
			h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
				return writeTestResponse(stdout, tc.response), true
			}
			code := c.hib("status")
			if code != tc.code || !strings.Contains(c.out.String()+c.errOut.String(), tc.detail) {
				t.Errorf("exit %d, stdout %q, stderr %q; expected exit %d and %q",
					code, c.out.String(), c.errOut.String(), tc.code, tc.detail)
			}
			if tc.response.Status != nil && !strings.HasPrefix(c.out.String(), "host") {
				t.Error("status omitted the host line without Azure config")
			}
			if calls := h.takeCalls(); len(calls) != 1 || calls[0][0] == "az" {
				t.Errorf("status without Azure config calls = %q, want only ssh", calls)
			}
		})
	}
}

func TestStatusUnreachableRunningHostDoesNotWakeOrRetry(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.states = []string{"VM running", "VM deallocated"}
	h.override = func(_ []string, _ io.Writer, stderr io.Writer) (int, bool) {
		io.WriteString(stderr, "ssh: refused\x1b[2J\n")
		return 255, true
	}
	code := c.hib("status")
	if code != 1 || c.out.Len() != 0 ||
		c.errOut.String() != "ssh: refused\\x1b[2J\nhib: couldn't reach "+testHost+": see ssh's message above\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, c.out.String(), c.errOut.String())
	}
	calls := h.takeCalls()
	if len(calls) != 2 || calls[0][0] != "az" || calls[0][2] != "get-instance-view" ||
		len(remoteCommands(calls)) != 1 {
		t.Errorf("unreachable status calls = %q, expected one Azure check and one ssh attempt", calls)
	}
}

func TestStatusAzureFailureDoesNotUseSSH(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	var checks int
	c.az = func(_ context.Context, _ ...string) ([]byte, error) {
		checks++
		return nil, errors.New("run az login\x1b[2J")
	}
	code := c.hib("status")
	if code != 1 || checks != 1 || c.out.Len() != 0 ||
		!strings.Contains(c.errOut.String(), "run az login\\x1b[2J") {
		t.Errorf("exit %d, Azure checks %d, stderr %q", code, checks, c.errOut.String())
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("failed Azure check ran ssh: %q", calls)
	}
}

func TestStatusRejectsArgumentsSafelyBeforeCheckingHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	code := c.hib("status", "--unknown\x1b[2J")
	if code != 2 || strings.Contains(c.errOut.String(), "\x1b") ||
		!strings.Contains(c.errOut.String(), "\\x1b") {
		t.Errorf("exit %d, expected a terminal-safe flag error", code)
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("invalid status ran host commands: %q", calls)
	}
}

func TestStatusSaysWhatsNext(t *testing.T) {
	none, two := 0, 2
	github := &hibapi.GitHub{Login: "octocat", Email: "1+octocat@users.noreply.github.com"}
	codex := &hibapi.Codex{Email: "octo@example.com", Plan: "plus", Health: hibapi.CodexOK}
	for _, tc := range []struct {
		name   string
		status hibapi.Status
		next   string
	}{
		{"fresh host", hibapi.Status{Machines: &none}, "Next: hib connect github, hib connect codex (optional), hib new NAME"},
		{"older hibd", hibapi.Status{}, "Next: hib connect github, hib connect codex (optional)"},
		{"no machines yet", hibapi.Status{GitHub: github, Codex: codex, Machines: &none}, "Next: hib new NAME"},
		{"no Codex", hibapi.Status{GitHub: github, Machines: &two}, "Next: hib connect codex (optional)"},
		{"all done", hibapi.Status{GitHub: github, Codex: codex, Machines: &two}, ""},
		{"all done with an older hibd", hibapi.Status{GitHub: github, Codex: codex}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			tc.status.Version, tc.status.E2B = "v-test", hibapi.Health{OK: true}
			h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
				return writeTestResponse(stdout, hibapi.Response{Status: &tc.status}), true
			}
			if code := c.hib("status"); code != 0 {
				t.Fatalf("exit %d: %s", code, c.errOut)
			}
			out := c.out.String()
			health, next, found := strings.Cut(out, "secrets  none\n")
			if !strings.HasPrefix(health, "host     "+testHost+"\nhibd     v-test\n") {
				t.Errorf("status output %q is missing the health lines", out)
			}
			want := ""
			if tc.next != "" {
				want = "\n" + tc.next + "\n"
			}
			if !found || next != want {
				t.Errorf("after the health lines %q, want %q", next, want)
			}
		})
	}
}

func TestInitPointsAtStatus(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	if code := c.hib("init", "-host", testHost); code != 0 {
		t.Fatalf("init exit %d: %s", code, c.errOut)
	}
	if !strings.HasSuffix(c.out.String(), "\nrun hib status to see what's next\n") {
		t.Errorf("init output %q doesn't end by pointing at hib status", c.out)
	}
}
