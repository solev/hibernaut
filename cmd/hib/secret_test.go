package main

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

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
