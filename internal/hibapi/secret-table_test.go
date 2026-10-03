package hibapi_test

import (
	"bytes"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestPrintSecrets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets []hibapi.Secret
		empty   string
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
			name: "no secrets",
			want: "NAME  ENV  SITES  PLACEHOLDER\n",
		},
		{
			name:    "client empty fields",
			secrets: []hibapi.Secret{{}},
			empty:   "-",
			want: "NAME  ENV  SITES  PLACEHOLDER\n" +
				"-     -    -      -\n",
		},
		{
			name:    "daemon empty fields",
			secrets: []hibapi.Secret{{}},
			want: "NAME  ENV  SITES  PLACEHOLDER\n" +
				"                  \n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			hibapi.PrintSecrets(&out, tc.secrets, tc.empty)
			if got := out.String(); got != tc.want {
				t.Errorf("PrintSecrets = %q, want %q", got, tc.want)
			}
		})
	}
}
