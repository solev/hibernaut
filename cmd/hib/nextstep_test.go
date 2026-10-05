package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailuresSayWhatToDoNext(t *testing.T) {
	answer := func(command string, code int, reply string) func(*host, *client) {
		return func(h *host, _ *client) {
			h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
				if words[0] == "sudo" {
					words = words[3:]
				}
				if words[0] != command {
					return 0, false
				}
				fmt.Fprint(stdout, reply)
				return code, true
			}
		}
	}
	empty := `{"v":1}`
	for _, tc := range []struct {
		name   string
		azure  bool
		before func(*host, *client)
		args   []string
		code   int
		end    string
	}{
		{"no settings", false, func(_ *host, c *client) { os.RemoveAll(c.dir) }, []string{"ls"}, 1,
			": run hib init --host USER@HOST first"},
		{"broken settings", false, func(_ *host, c *client) { os.WriteFile(filepath.Join(c.dir, "config.json"), []byte("{"), 0o600) },
			[]string{"ls"}, 1, ". Run hib init --host USER@HOST to set this laptop up again"},
		{"unreachable", false, func(h *host, _ *client) { h.sleep() }, []string{"ls"}, 1,
			": see ssh's message above"},
		{"no hibd", false, answer("ls", 127, ""), []string{"ls"}, 1, ": run make deploy"},
		{"hibd fails", false, answer("new", 3, ""), []string{"new", "app"}, 1,
			": check the host with hib status"},
		{"hibd fails in status", false, answer("status", 3, ""), []string{"status"}, 1,
			": try again in a minute"},
		{"no passwordless sudo", false, answer("status", 1, ""), []string{"status"}, 1,
			": run hib init --host USER@HOST with a user that has it"},
		{"unreadable reply", false, answer("ls", 0, "not json"), []string{"ls"}, 1,
			": check the host with hib status"},
		{"no machine in the reply", false, answer("new", 0, empty), []string{"new", "app"}, 1,
			": run hib ls to see your machines"},
		{"no copies in the reply", false, answer("fork", 0, empty), []string{"fork", "app"}, 1,
			": run hib ls to see your machines"},
		{"no image in the reply", false, answer("image", 0, empty), []string{"image", "save", "app", "snap"}, 1,
			": run hib image ls to see your images"},
		{"no status in the reply", false, answer("status", 0, empty), []string{"status"}, 1,
			": run make deploy to update hibd"},
		{"no secret in the reply", false, func(h *host, c *client) {
			c.stdin = strings.NewReader("sk-test\n")
			answer("secret", 0, empty)(h, c)
		}, []string{"secret", "add", "openai", "--env", "OPENAI_API_KEY", "--bind", "api.openai.com"}, 1,
			": run hib secret ls to see your secrets"},
		{"empty secret", false, nil, []string{"secret", "add", "openai", "--env", "OPENAI_API_KEY", "--bind", "api.openai.com"}, 1,
			": type it at the prompt or pipe it on stdin"},
		{"secret without a variable", false, nil, []string{"secret", "add", "openai", "--bind", "api.openai.com"}, 2,
			"\nrun hib help secret add"},
		{"bad secret name", false, nil, []string{"secret", "add", "OPENAI", "--env", "OPENAI_API_KEY", "--bind", "api.openai.com"}, 2,
			"\nrun hib help secret add"},
		{"bad secret name to remove", false, nil, []string{"secret", "rm", "OPENAI"}, 2, "\nrun hib help secret rm"},
		{"ready fails after a wake", true, func(h *host, c *client) {
			h.sleep("VM deallocated")
			answer("ready", 1, "")(h, c)
		}, []string{"ls"}, 1, ": check the host with hib status"},
		{"az fails", true, func(h *host, c *client) {
			h.sleep("VM deallocated")
			c.az = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("az: ERROR: run az login") }
		}, []string{"ls"}, 1, ". Check that az works, for example with az login"},
		{"bridge closes early", false, answer("bridge", 0, ""), []string{"bridge", "app.hib"}, 1,
			": run hib ls to see your machines"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			var c *client
			if tc.azure {
				c = initClient(t, h)
			} else {
				c = initClient(t, h, true)
			}
			if tc.before != nil {
				tc.before(h, c)
			}
			code := c.hib(tc.args...)
			if code != tc.code || !strings.HasSuffix(c.errOut.String(), tc.end+"\n") {
				t.Errorf("hib %s: exit %d, stderr %q; want %d and a message ending %q", strings.Join(tc.args, " "), code, c.errOut, tc.code, tc.end)
			}
		})
	}
}

func TestInitSaysWhatToDoNext(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sshVersion string
		dir        string
		args       []string
		code       int
		end        string
	}{
		{"old OpenSSH", "OpenSSH_9.5p1, OpenSSL 3.0.2", "", nil, 1, ". Install OpenSSH 9.6 or later"},
		{"not OpenSSH", "Dropbear v2022.83", "", nil, 1, ". Install OpenSSH 9.6 or later"},
		{"no identity file", "", "", []string{"-identity", "/nonexistent/id_ed25519"}, 2, "\nrun hib help init"},
		{"settings in an odd place", "", "/nonexistent/hib's", nil, 1, ". Use --config DIR with such a path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			if tc.sshVersion != "" {
				h.sshVersion = tc.sshVersion
			}
			c := newClient(t, h)
			if tc.dir != "" {
				c.dir = tc.dir
			}
			code := c.hib(append([]string{"init", "-host", testHost}, tc.args...)...)
			if code != tc.code || !strings.HasSuffix(c.errOut.String(), tc.end+"\n") {
				t.Errorf("exit %d, stderr %q; want %d and a message ending %q", code, c.errOut, tc.code, tc.end)
			}
		})
	}
}
