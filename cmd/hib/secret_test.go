package main

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

func writeTestResponse(w io.Writer, r hibapi.Response) int {
	r.V = hibapi.Version
	if err := json.NewEncoder(w).Encode(r); err != nil {
		return 1
	}
	if r.Error != "" {
		return 1
	}
	return 0
}

func TestSudoHibdArgsQuoteRemoteArguments(t *testing.T) {
	cfg := &config{Host: testHost}
	got, err := cfg.sudoHibdArgs("secret", "add", "github", "--env", "GH_TOKEN", "--bind", "*.github.com", "--json")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(got, "--")
	if i < 0 || i+2 >= len(got) {
		t.Fatalf("sudo args = %q", got)
	}
	cmd := strings.Join(got[i+2:], " ")
	want := "sudo -n hibd 'secret' 'add' 'github' '--env' 'GH_TOKEN' '--bind' '*.github.com' '--json'"
	if cmd != want {
		t.Fatalf("remote command = %q, want %q", cmd, want)
	}
}

func TestSecretAddUsesSudoAndStdin(t *testing.T) {
	h := newHost(t)
	var gotStdin string
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if len(words) == 0 || words[0] != "sudo" {
			return 0, false
		}
		if len(words) < 4 || words[0] != "sudo" || words[1] != "-n" || words[2] != "hibd" {
			return 127, true
		}
		if slices.Contains(words, "gho_realSecretValue1234567890") {
			t.Fatalf("secret value reached argv: %q", words)
		}
		data, err := io.ReadAll(h.lastStdin)
		if err != nil {
			t.Fatal(err)
		}
		gotStdin = string(data)
		return writeTestResponse(stdout, hibapi.Response{Secret: &hibapi.Secret{
			Name: "github", Env: "GH_TOKEN", Sites: []string{"*.github.com"}, Placeholder: "gho_hibplaceholder1234567890",
		}}), true
	}
	c := initClient(t, h)
	c.app.stdin = strings.NewReader("gho_realSecretValue1234567890\n")
	if code := c.hib("secret", "add", "github", "--env", "GH_TOKEN", "--bind", "*.github.com"); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, c.errOut)
	}
	if gotStdin != "gho_realSecretValue1234567890" {
		t.Fatalf("stdin = %q", gotStdin)
	}
	if !strings.Contains(c.out.String(), "stored github") {
		t.Fatalf("stdout = %q", c.out.String())
	}
}

func TestSecretAddSendsFullStdinAfterWakeRetry(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.states = []string{"VM deallocated"}
	value := strings.Repeat("x", 60<<10)
	var calls int
	var first, second string
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if len(words) == 0 || words[0] != "sudo" {
			return 0, false
		}
		calls++
		data, err := io.ReadAll(h.lastStdin)
		if err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			first = string(data)
			return 255, true
		case 2:
			second = string(data)
			return writeTestResponse(stdout, hibapi.Response{Secret: &hibapi.Secret{
				Name: "github", Env: "GH_TOKEN", Sites: []string{"github.com"}, Placeholder: "gho_hibplaceholder1234567890",
			}}), true
		}
		t.Fatalf("unexpected sudo call %d", calls)
		return 1, true
	}
	c.app.stdin = strings.NewReader(value)
	if code := c.hib("secret", "add", "github", "--env", "GH_TOKEN", "--bind", "github.com"); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, c.errOut)
	}
	if first != value || second != value {
		t.Fatalf("stdin lengths first=%d second=%d, want %d", len(first), len(second), len(value))
	}
}

func TestSecretListShowsReturnedMetadata(t *testing.T) {
	h := newHost(t)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if !slices.Equal(words, []string{"secret", "ls", "--json"}) {
			return 0, false
		}
		return writeTestResponse(stdout, hibapi.Response{Secrets: []hibapi.Secret{{
			Name: "github", Env: "GH_TOKEN", Sites: []string{"github.com", "*.github.com"},
			Placeholder: "gho_hibplaceholder1234567890",
		}}}), true
	}
	c := initClient(t, h)
	if code := c.hib("secret", "ls"); code != 0 {
		t.Fatalf("secret ls exit %d, stderr %s", code, c.errOut)
	}
	want := []string{"NAME", "ENV", "SITES", "PLACEHOLDER", "github", "GH_TOKEN",
		"github.com,*.github.com", "gho_hibplaceholder1234567890"}
	if got := strings.Fields(c.out.String()); !slices.Equal(got, want) {
		t.Fatalf("secret ls fields = %q, want %q", got, want)
	}
}

func TestLogAsksForTheNewestEntries(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		args []string
		n    string
	}{
		{nil, "50"},
		{[]string{"-n", "5"}, "5"},
		{[]string{"-n", "0"}, "0"},
	} {
		t.Run(strings.Join(append([]string{"log"}, tc.args...), " "), func(t *testing.T) {
			h := newHost(t)
			var got []string
			h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
				if len(words) == 0 || words[0] != "log" {
					return 0, false
				}
				got = words
				return writeTestResponse(stdout, hibapi.Response{Logs: []hibapi.LogEntry{{
					Time: when, Machine: "dev", Site: "api.github.com", Method: "GET", Path: "/user",
					Secrets: []string{"github", "npm"},
				}}}), true
			}
			c := initClient(t, h)
			if code := c.hib(append([]string{"log"}, tc.args...)...); code != 0 {
				t.Fatalf("hib log exit %d, stderr %s", code, c.errOut)
			}
			if want := []string{"log", "--n", tc.n, "--json"}; !slices.Equal(got, want) {
				t.Fatalf("remote command = %q, want %q", got, want)
			}
			want := []string{"TIME", "MACHINE", "SITE", "METHOD", "PATH", "SECRETS",
				when.Local().Format(time.RFC3339), "dev", "api.github.com", "GET", "/user", "github,npm"}
			if fields := strings.Fields(c.out.String()); !slices.Equal(fields, want) {
				t.Fatalf("hib log fields = %q, want %q", fields, want)
			}
			if c.errOut.Len() != 0 {
				t.Fatalf("hib log stderr = %q, want nothing", c.errOut)
			}
		})
	}
}

func TestLogWarnsAboutCorruptLinesAndEscapesEntries(t *testing.T) {
	h := newHost(t)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if len(words) == 0 || words[0] != "log" {
			return 0, false
		}
		return writeTestResponse(stdout, hibapi.Response{Corrupt: 2, Logs: []hibapi.LogEntry{{
			Time: time.Unix(0, 0), Machine: "dev\x1b]0;owned\x07", Site: "example.com", Method: "GET",
			Path: "/\x1b[2J", Secrets: []string{"token"},
		}}}), true
	}
	c := initClient(t, h)
	if code := c.hib("log"); code != 0 {
		t.Fatalf("hib log exit %d, stderr %s", code, c.errOut)
	}
	if got, want := c.errOut.String(), "hib: skipped 2 corrupt audit log line(s)\n"; got != want {
		t.Fatalf("hib log stderr = %q, want %q", got, want)
	}
	if strings.ContainsAny(c.out.String(), "\x1b\x07") {
		t.Fatalf("hib log printed control characters: %q", c.out.String())
	}
	if !strings.Contains(c.out.String(), "example.com") {
		t.Fatalf("hib log stdout = %q, want the entry", c.out.String())
	}
}

func TestLogRefusesBadArgumentsWithoutAskingTheHost(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-n", "-1"}, "hib log: -n must be non-negative"},
		{[]string{"extra"}, "log [-n N]"},
		{[]string{"-n", "many"}, "invalid value"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			h := newHost(t)
			h.override = func(words []string, _, _ io.Writer) (int, bool) {
				if len(words) > 0 && words[0] == "log" {
					t.Errorf("hib log asked the host with bad arguments: %q", words)
				}
				return 0, false
			}
			c := initClient(t, h)
			if code := c.hib(append([]string{"log"}, tc.args...)...); code != 2 {
				t.Fatalf("hib log %q exit %d, want 2", tc.args, code)
			}
			if !strings.Contains(c.errOut.String(), tc.want) {
				t.Fatalf("hib log %q stderr = %q, want %q", tc.args, c.errOut, tc.want)
			}
		})
	}
}

func TestLogReportsHostErrors(t *testing.T) {
	h := newHost(t)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if len(words) == 0 || words[0] != "log" {
			return 0, false
		}
		return writeTestResponse(stdout, hibapi.Response{Error: "cannot read the audit log. Run sudo hibd doctor"}), true
	}
	c := initClient(t, h)
	if code := c.hib("log"); code != 1 {
		t.Fatalf("hib log exit %d, want 1", code)
	}
	if got, want := c.errOut.String(), "hib: cannot read the audit log. Run sudo hibd doctor\n"; got != want {
		t.Fatalf("hib log stderr = %q, want %q", got, want)
	}
	if c.out.Len() != 0 {
		t.Fatalf("hib log stdout = %q, want nothing", c.out)
	}
}
