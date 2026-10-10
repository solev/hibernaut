package hibapi_test

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestPrintSecrets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets []hibapi.Secret
		want    string
	}{
		{
			name: "metadata and terminal escapes",
			secrets: []hibapi.Secret{
				{Name: "gh", Env: "GH_TOKEN", Sites: []string{"a.co", "b.co"}, Placeholder: "gho_hib-placeholder"},
				{Name: "oa\x1b", Env: "KEY\x00", Sites: []string{"c.co\x7f"}, Placeholder: "sk-hib\x1bvalue"},
			},
			want: "NAME    ENV       SITES      PLACEHOLDER\n" +
				"gh      GH_TOKEN  a.co,b.co  gho_hib-placeholder\n" +
				"oa\\x1b  KEY\\x00   c.co\\x7f   sk-hib\\x1bvalue\n",
		},
		{
			name: "tabs, newlines and carriage returns stay in their cells",
			secrets: []hibapi.Secret{
				{Name: "gh", Env: "GH_TOKEN", Sites: []string{"a.co"}, Placeholder: "ph"},
				{Name: "a\tb", Env: "K\nE", Sites: []string{"c.co\r"}, Placeholder: "p\tq"},
			},
			want: "NAME    ENV       SITES     PLACEHOLDER\n" +
				"gh      GH_TOKEN  a.co      ph\n" +
				"a\\x09b  K\\x0aE    c.co\\x0d  p\\x09q\n",
		},
		{
			name: "no secrets",
			want: "NAME  ENV  SITES  PLACEHOLDER\n",
		},
		{
			name:    "empty fields",
			secrets: []hibapi.Secret{{}},
			want: "NAME  ENV  SITES  PLACEHOLDER\n" +
				"-     -    -      -\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			hibapi.PrintSecrets(&out, tc.secrets)
			if got := out.String(); got != tc.want {
				t.Errorf("PrintSecrets = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrintPorts(t *testing.T) {
	one := 1
	for _, tc := range []struct {
		name string
		all  []hibapi.Ports
		want string
	}{
		{
			name: "terminal escapes stay in their cells",
			all: []hibapi.Ports{
				{Machine: "a\tb\x1b", Addr: "198.18.0.1\n", Published: []hibapi.PublishedPort{{Port: 80, Open: &one}}},
			},
			want: "NAME        HOST            IP              PORTS  OPEN\n" +
				"a\\x09b\\x1b  a\\x09b\\x1b.hib  198.18.0.1\\x0a  80     1\n",
		},
		{
			name: "empty fields",
			all:  []hibapi.Ports{{Machine: "web", Published: []hibapi.PublishedPort{{Port: 3000}}}},
			want: "NAME  HOST     IP  PORTS  OPEN\n" +
				"web   web.hib  -   3000   -\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			hibapi.PrintPorts(&out, tc.all)
			if got := out.String(); got != tc.want {
				t.Errorf("PrintPorts = %q, want %q", got, tc.want)
			}
		})
	}
}

var colourRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func red(word string) string { return "\x1b[31m" + word + "\x1b[0m" }

func styled(s string) []string {
	var words []string
	for _, m := range regexp.MustCompile("\x1b\\[31m(.*?)\x1b\\[0m").FindAllStringSubmatch(s, -1) {
		words = append(words, m[1])
	}
	return words
}

func TestTableStylesStatesWithoutMovingColumns(t *testing.T) {
	table := func(style hibapi.Style) string {
		var out bytes.Buffer
		tw := hibapi.NewTable(&out, style)
		fmt.Fprintln(tw, "NAME\tSTATE\tIMAGE")
		fmt.Fprintf(tw, "app\t%s\tdev\n", tw.State("running"))
		fmt.Fprintf(tw, "a-much-longer-name\t%s\tdev\n", tw.State("paused"))
		fmt.Fprintf(tw, "x\t%s\t%s\n", tw.State("ok"), tw.State("p\\x1b"))
		if err := tw.Flush(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	plain := table(nil)
	want := "NAME                STATE    IMAGE\n" +
		"app                 running  dev\n" +
		"a-much-longer-name  paused   dev\n" +
		"x                   ok       p\\x1b\n"
	if plain != want {
		t.Errorf("without a style:\n%s\nwant\n%s", plain, want)
	}
	coloured := table(red)
	if got := colourRE.ReplaceAllString(coloured, ""); got != plain {
		t.Errorf("with a style, uncoloured:\n%s\nwant\n%s", got, plain)
	}
	if got, want := styled(coloured), []string{"running", "paused", "ok", "p\\x1b"}; !slices.Equal(got, want) {
		t.Errorf("styled %q, want %q", got, want)
	}
	if strings.Contains(coloured, "\xff") {
		t.Errorf("table leaked its markers: %q", coloured)
	}
}

func TestPrintStatusStylesStates(t *testing.T) {
	status := hibapi.Status{
		Version: "0.5.0",
		E2B:     hibapi.Health{Detail: "502 Bad Gateway"},
		Codex:   &hibapi.Codex{Email: "octo@example.com", Plan: "pro", Health: hibapi.CodexDead},
		Secrets: []hibapi.SecretHealth{
			{Name: "github", Sites: []string{"github.com"}, State: hibapi.SecretOK},
			{Name: "openai", Sites: []string{"api.openai.com"}, State: hibapi.SecretRevoked, Detail: "revoked"},
		},
	}
	var plain, coloured bytes.Buffer
	hibapi.PrintStatus(&plain, "host", status, nil)
	hibapi.PrintStatus(&coloured, "host", status, red)
	if got := colourRE.ReplaceAllString(coloured.String(), ""); got != plain.String() {
		t.Errorf("with a style, uncoloured:\n%s\nwant\n%s", got, plain.String())
	}
	if got, want := styled(coloured.String()), []string{"down", "dead", "ok", "revoked"}; !slices.Equal(got, want) {
		t.Errorf("styled %q, want %q", got, want)
	}

	status.E2B = hibapi.Health{OK: true}
	coloured.Reset()
	hibapi.PrintStatus(&coloured, "", status, red)
	if got := styled(coloured.String()); len(got) == 0 || got[0] != "ok" {
		t.Errorf("E2B ok styled %q, want ok first", got)
	}

	coloured.Reset()
	hibapi.PrintCodex(&coloured, *status.Codex, red)
	if got := styled(coloured.String()); !slices.Equal(got, []string{"dead"}) {
		t.Errorf("PrintCodex styled %q, want dead", got)
	}
}
