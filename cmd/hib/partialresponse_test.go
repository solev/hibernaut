package main

import (
	"io"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestFailedRepliesAreNotTreatedAsSuccess(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{
			Error:    "operation failed: try again",
			Machine:  &hibapi.Machine{Name: "web", ID: "id", State: "running"},
			Machines: []hibapi.Machine{{Name: "web-1", ID: "copy1", State: "running"}},
			Image:    &hibapi.Image{Name: "myimg", Kind: hibapi.KindSaved}, Images: []hibapi.Image{{Name: "myimg"}},
			Secret: &hibapi.Secret{Name: "key"}, Secrets: []hibapi.Secret{{Name: "key"}},
			GitHub: &hibapi.GitHub{Login: "octocat"}, Codex: &hibapi.Codex{Email: "a@example.com"},
			Status: &hibapi.Status{}, Template: "template", Version: "dev",
			Logs: []hibapi.LogEntry{{Machine: "web"}},
		}), true
	}
	for _, args := range [][]string{
		{"new", "web"}, {"ls"}, {"rm", "web", "--yes"}, {"pause", "web"},
		{"image", "build", "dev"}, {"image", "save", "web", "myimg"},
		{"image", "ls"}, {"image", "rm", "myimg"},
		{"secret", "add", "key", "-env", "KEY", "-bind", "example.com"},
		{"secret", "ls"}, {"secret", "rm", "key"}, {"log"},
		{"connect", "github", "--token"}, {"connect", "codex"}, {"status"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c.stdin = strings.NewReader("test-value-123456789\n")
			if code := c.hib(args...); code != 1 || c.out.Len() != 0 ||
				!strings.Contains(c.errOut.String(), "operation failed: try again") {
				t.Fatalf("exit %d; stdout %q; stderr %q", code, c.out, c.errOut)
			}
		})
	}
}
