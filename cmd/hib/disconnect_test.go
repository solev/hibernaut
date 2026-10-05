package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibd"
)

func TestDisconnect(t *testing.T) {
	for _, tc := range []struct{ provider, said string }{
		{"codex", "Codex is not connected on this host. Machines keep placeholders in ~/.codex/auth.json."},
		{"github", "GitHub is not connected on this host. Machines keep placeholders."},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			h.sudoApp = func(*hibd.App) {}
			h.env["HIBD_VAULT_KEY"] = filepath.Join(h.env["HIBD_STATE_DIR"], "vault.key")
			h.env["HIBD_AUDIT_LOG"] = filepath.Join(h.env["HIBD_STATE_DIR"], "audit.jsonl")
			h.sleep("VM deallocated")
			if code := c.hib("disconnect", tc.provider); code != 0 {
				t.Fatalf("exit %d: %s", code, c.errOut)
			}
			if c.out.Len() > 0 || !strings.Contains(c.errOut.String(), tc.said) {
				t.Errorf("stdout %q, stderr %q, want hibd's %q on stderr", c.out, c.errOut, tc.said)
			}
			calls := h.takeCalls()
			if !slices.ContainsFunc(calls, func(call []string) bool { return slices.Equal(call[:3], []string{"az", "vm", "start"}) }) {
				t.Errorf("hib ran %q, want it to wake the host", calls)
			}
			want := []string{"hibd version --json", "hibd ready --timeout 5m", "hibd ready --timeout 5m", "hibd ready --timeout 5m",
				"hibd version --json", "sudo -n hibd 'disconnect' '" + tc.provider + "' '--json'"}
			if got := remoteCommands(calls); !slices.Equal(got, want) {
				t.Errorf("remote commands = %q, want %q", got, want)
			}
		})
	}
}

func TestDisconnectWithAnOlderHibd(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if words[0] != "version" {
			t.Errorf("hib ran hibd %q on an older host", words)
			return 2, true
		}
		fmt.Fprintln(stdout, `{"v":1,"version":"0.4.0"}`)
		return 0, true
	}
	for _, provider := range []string{"codex", "github"} {
		if code := c.hib("disconnect", provider); code != 1 || c.out.Len() > 0 ||
			c.errOut.String() != "hib: the host's hibd is too old for disconnect: run make up to update it\n" {
			t.Errorf("disconnect %s = %d, stdout %q, stderr %q", provider, code, c.out, c.errOut)
		}
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json", "hibd version --json"}; !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want only %q", got, want)
	}
}

func TestDisconnectCtrlC(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.sudoApp = func(*hibd.App) {}
	ctx, cancel := context.WithCancelCause(t.Context())
	interruptOn(h, cancel, "disconnect")
	if code := c.hibContext(ctx, "disconnect", "codex"); code != 130 {
		t.Errorf("exit %d, want 130", code)
	}
	if want := "hib: stopped waiting. The host may still remove the login: check hib status\n"; !strings.HasSuffix(c.errOut.String(), want) {
		t.Errorf("stderr %q, want %q", c.errOut, want)
	}
}
