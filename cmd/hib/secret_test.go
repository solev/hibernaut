package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
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

func TestSecretAddDoesNotPrintAReflectedValue(t *testing.T) {
	values := map[string]string{
		"plain":             "s3cret-value-1234567890",
		"control character": "tok\x1b\x01en\tvalue-1234",
		"pem": "-----BEGIN PRIVATE KEY-----\n" +
			"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\n" +
			"MzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\n" +
			"NMoSfm76oqFvAp8Gy0iz5sxjZmSnXyCdPEovGhLa0VzMaQ8s+CLOyS56YyCFGeJZ\n" +
			"-----END PRIVATE KEY-----",
	}
	modes := []string{"ssh stderr", "hibd error", "unreadable reply", "stored line", "ssh runner error"}
	for valueName, value := range values {
		for _, mode := range modes {
			t.Run(valueName+"/"+mode, func(t *testing.T) {
				h := newHost(t)
				c := initClient(t, h)
				h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
					stored := &hibapi.Secret{Name: "api", Env: "API_KEY", Sites: []string{"api.example.com"}, Placeholder: "ph"}
					switch mode {
					case "ssh stderr":
						io.WriteString(stderr, "hibd: storing "+value[:5])
						io.WriteString(stderr, value[5:]+"\n")
					case "hibd error":
						return writeTestResponse(stdout, hibapi.Response{Error: "refused " + value}), true
					case "unreadable reply":
						io.WriteString(stdout, "not JSON: "+value)
						return 1, true
					case "stored line":
						stored.Name, stored.Env, stored.Sites = value, value, []string{value}
					}
					return writeTestResponse(stdout, hibapi.Response{Secret: stored}), true
				}
				if mode == "ssh runner error" {
					c.ssh = func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
						h.record(args)
						return -1, errors.New("ssh broke on " + value)
					}
				}
				c.app.stdin = strings.NewReader(value)
				code := c.hib("secret", "add", "api", "--env", "API_KEY", "--bind", "api.example.com")
				if want := map[bool]int{true: 0, false: 1}[mode == "ssh stderr" || mode == "stored line"]; code != want {
					t.Errorf("exit %d, want %d; stderr %q", code, want, c.errOut)
				}
				output := c.out.String() + c.errOut.String()
				for _, form := range append([]string{value, termsafe.String(value), termsafe.Line(value)}, strings.Split(value, "\n")[1:]...) {
					if strings.Contains(output, form) {
						t.Errorf("the value reached the output as %q: %q", form, output)
					}
				}
				if mode == "unreadable reply" {
					if !strings.Contains(output, "unreadable reply from hibd") || strings.Contains(output, "not JSON") {
						t.Errorf("an unreadable reply to a secret was shown: %q", output)
					}
				} else if !strings.Contains(output, "[redacted]") {
					t.Errorf("no redaction marker in %q", output)
				}
			})
		}
	}
}

func jsonQuoted(t *testing.T, value string, escapeHTML bool) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(value); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func TestSecretAddRedactsQuotedForms(t *testing.T) {
	const value = "sk-review\"q\\b\t<x>&\u00e9\x01\ntail-7f3a9c"
	quotings := map[string]string{
		"go":                strconv.Quote(value),
		"json":              jsonQuoted(t, value, true),
		"json without html": jsonQuoted(t, value, false),
	}
	if distinct := slices.Compact(slices.Sorted(maps.Values(quotings))); len(distinct) != len(quotings) {
		t.Fatalf("the quotings must differ to test each one: %q", distinct)
	}
	for quoting, quoted := range quotings {
		messages := map[string]string{
			"text error": "token refused: " + quoted,
			"json error": `{"error":{"message":"bad token","token":` + quoted + `}}`,
		}
		for shape, message := range messages {
			for _, channel := range []string{"stdout", "stderr"} {
				t.Run(quoting+"/"+shape+"/"+channel, func(t *testing.T) {
					h := newHost(t)
					c := initClient(t, h)
					h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
						if channel == "stdout" {
							return writeTestResponse(stdout, hibapi.Response{Error: message}), true
						}
						for i := 1; i < len(message); i++ {
							io.WriteString(stderr, message[:i])
							io.WriteString(stderr, message[i:]+"\n")
						}
						return writeTestResponse(stdout, hibapi.Response{Secret: &hibapi.Secret{
							Name: "api", Env: "API_KEY", Sites: []string{"api.example.com"}, Placeholder: "ph",
						}}), true
					}
					c.app.stdin = strings.NewReader(value)
					c.hib("secret", "add", "api", "--env", "API_KEY", "--bind", "api.example.com")
					output := c.out.String() + c.errOut.String()
					for _, leak := range []string{"review", "7f3a9c"} {
						if strings.Contains(output, leak) {
							t.Fatalf("part of the value reached the output: %q", output)
						}
					}
					want := 1
					if channel == "stderr" {
						want = len(message) - 1
					}
					if got := strings.Count(output, "[redacted]"); got != want {
						t.Errorf("%d redactions, want %d: %q", got, want, output)
					}
				})
			}
		}
	}
}

func TestAPlainSecretKeepsOneRedactingWriterPerStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}
	hidden, flush := a.hidingSecret("abc123XYZ")
	for _, got := range []struct {
		w    io.Writer
		next io.Writer
	}{{hidden.stdout, &stdout}, {hidden.stderr, &stderr}} {
		w, ok := got.w.(*secretWriter)
		if !ok || w.w != got.next {
			t.Errorf("a plain value got a chain of writers: %#v", got.w)
			continue
		}
		if len(w.forms) != 1 || string(w.forms[0]) != "abc123XYZ" {
			t.Errorf("a plain value has the forms %q, want just the value", w.forms)
		}
	}
	io.WriteString(hidden.stdout, `token "abc123XYZ" and abc123XYZ`)
	flush()
	if want := `token "[redacted]" and [redacted]`; stdout.String() != want {
		t.Errorf("printed %q, want %q", stdout.String(), want)
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

func TestLogKeepsEachEntryOnOneAlignedRow(t *testing.T) {
	h := newHost(t)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{Logs: []hibapi.LogEntry{
			{Time: time.Unix(0, 0), Machine: "dev\tx", Site: "a.co\nb", Method: "GE\rT", Path: "/p\tq", Secrets: []string{"one\n", "two"}},
			{Time: time.Unix(0, 0), Machine: "app", Site: "api.github.com", Method: "GET", Path: "/user", Secrets: []string{"github"}},
		}}), true
	}
	c := initClient(t, h)
	if code := c.hib("log"); code != 0 {
		t.Fatalf("hib log exit %d, stderr %s", code, c.errOut)
	}
	lines := strings.Split(strings.TrimSuffix(c.out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("hib log printed %d lines, want a header and one row per entry: %q", len(lines), c.out)
	}
	if want := `dev\x09x`; !strings.Contains(lines[1], want) || !strings.Contains(lines[1], `a.co\x0ab`) ||
		!strings.Contains(lines[1], `GE\x0dT`) || !strings.Contains(lines[1], `/p\x09q`) || !strings.Contains(lines[1], `one\x0a,two`) {
		t.Errorf("row %q, want each cell's tab, newline and carriage return escaped", lines[1])
	}
	cell := regexp.MustCompile(`\S+`)
	header := cell.FindAllStringIndex(lines[0], -1)
	for _, line := range lines[1:] {
		row := cell.FindAllStringIndex(line, -1)
		if len(row) != len(header) {
			t.Fatalf("row %q has %d cells, want %d", line, len(row), len(header))
		}
		for i := range row {
			if row[i][0] != header[i][0] {
				t.Errorf("row %q: cell %d starts at %d, want %d under its header", line, i, row[i][0], header[i][0])
			}
		}
	}
}

func TestLogRefusesBadArgumentsWithoutAskingTheHost(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-n", "-1"}, "hib log: --n must be non-negative"},
		{[]string{"extra"}, "log [--n N]"},
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

// writeLinkLog points h's hibd at a link log of entries and one corrupt line.
func writeLinkLog(t *testing.T, h *host, entries ...audit.LinkEntry) {
	t.Helper()
	var data []byte
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(h.env["HIBD_LINK_LOG"], append(data, "{not json\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLogLinksShowsLinksByMachineName(t *testing.T) {
	h, c, _ := portHost(t)
	api, web := machineID(t, h, "api"), machineID(t, h, "web")
	at := time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC)
	writeLinkLog(t, h,
		audit.LinkEntry{Time: at, Event: "open", From: web, FromID: web, To: api, ToID: api, Port: 8080, Woke: true},
		audit.LinkEntry{Time: at.Add(time.Hour), Event: "close", From: web, FromID: web, To: api, ToID: api, Port: 8080,
			Reason: "revoked", BytesIn: 10, BytesOut: 20, SecondsOpen: 3600},
		audit.LinkEntry{Time: at.Add(2 * time.Hour), Event: "refused", From: "?", To: "db\x1b]0;owned\x07", ToID: "db1", Port: 22,
			Reason: "not published", Count: 7},
	)
	if code := c.hib("log", "--links", "--n", "5"); code != 0 {
		t.Fatalf("hib log --links exit %d: %s", code, c.errOut)
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json", "hibd log --n 5 --links --json"}; !slices.Equal(got, want) {
		t.Errorf("remote commands %q, want %q", got, want)
	}
	if got, want := c.errOut.String(), "hib: skipped 1 corrupt link log line(s)\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	local := func(d time.Duration) string { return at.Add(d).Local().Format(time.RFC3339) }
	want := [][]string{
		{"TIME", "MACHINE", "TO", "PORT", "RESULT", "WOKE", "BYTES"},
		{local(0), "web", "api", "8080", "open", "yes", "-"},
		{local(time.Hour), "web", "api", "8080", "closed after 1h: revoked", "-", "10 sent, 20 received"},
		{local(2 * time.Hour), "?", `db\x1b]0;owned\x07`, "22", "refused: not published (7 times)", "-", "-"},
	}
	lines := strings.Split(strings.TrimSuffix(c.out.String(), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("hib log --links printed\n%s", c.out)
	}
	for i, line := range lines {
		if got := regexp.MustCompile(`\s{2,}`).Split(line, -1); !slices.Equal(got, want[i]) {
			t.Errorf("line %d = %q, want %q", i, got, want[i])
		}
	}

	if code := c.hib("log", "--links", "--json"); code != 0 {
		t.Fatalf("hib log --links --json exit %d: %s", code, c.errOut)
	}
	r, err := hibapi.DecodeResponse(c.out.Bytes())
	if err != nil || len(r.Links) != 3 || r.Links[0].From != "web" || r.Links[0].To != "api" || r.Corrupt != 1 {
		t.Errorf("reply %+v, %v, want the three entries by name and one corrupt line", r, err)
	}
	if c.errOut.Len() != 0 {
		t.Errorf("stderr %q, want nothing with --json", c.errOut)
	}
}

func TestLogLinksWithNoLinkLog(t *testing.T) {
	_, c, _ := portHost(t)
	if code := c.hib("log", "--links"); code != 0 {
		t.Fatalf("hib log --links exit %d: %s", code, c.errOut)
	}
	if got := strings.Fields(c.out.String()); !slices.Equal(got, []string{"TIME", "MACHINE", "TO", "PORT", "RESULT", "WOKE", "BYTES"}) {
		t.Errorf("stdout %q, want the header alone", c.out)
	}
	if code := c.hib("log", "--links", "--json"); code != 0 {
		t.Fatalf("hib log --links --json exit %d: %s", code, c.errOut)
	}
	if got, want := c.out.String(), `{"v":1,"links":[]}`+"\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
}

func TestLogLinksNeedsANewEnoughHibd(t *testing.T) {
	h := newHost(t)
	hibdVersion(h, `{"v":1,"version":"old","features":["build-events","disconnect"]}`)
	c := initClient(t, h)
	if code := c.hib("log", "--links"); code != 1 {
		t.Errorf("hib log --links = %d, want 1", code)
	}
	if got, want := c.errOut.String(), "hib: this host's hibd is too old for hib log --links. Upgrade it\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json"}; !slices.Equal(got, want) {
		t.Errorf("hib log --links ran %q, want only %q", got, want)
	}
}
