package idledeallocate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fakeHibd = `#!/bin/sh
echo "hibd $*" >>"$CALLS"
case "$1" in
ls) printf '%s' "$HIBD_LS_OUT"; exit "${HIBD_LS_EXIT:-0}" ;;
esac
exit 2
`

const fakeQuiesce = `#!/bin/sh
echo quiesce >>"$CALLS"
exit "${QUIESCE_EXIT:-0}"
`

const fakeDeallocate = `#!/bin/sh
echo "deallocate $*" >>"$CALLS"
`

type host struct {
	t          *testing.T
	dir        string
	env        map[string]string
	lastActive int64
}

func newHost(t *testing.T) *host {
	t.Helper()
	for _, tool := range []string{"bash", "pgrep", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	dir := t.TempDir()
	h := &host{t: t, dir: dir, lastActive: 0}
	h.write("hibd", fakeHibd, 0o755)
	h.write("quiesce", fakeQuiesce, 0o755)
	h.write("deallocate", fakeDeallocate, 0o755)
	h.write("loadavg", "0.05 0.03 0.01 1/200 1234\n", 0o644)
	h.env = map[string]string{
		"IDLE_MINUTES":        "30",
		"MAX_LOAD":            "1.0",
		"STATE_FILE":          filepath.Join(dir, "state", "last-active"),
		"HIBD":                filepath.Join(dir, "hibd"),
		"QUIESCE":             filepath.Join(dir, "quiesce"),
		"DEALLOCATE":          filepath.Join(dir, "deallocate"),
		"LOADAVG_FILE":        filepath.Join(dir, "loadavg"),
		"SSH_SESSION_PATTERN": "^hib-no-such-process-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"CALLS":               filepath.Join(dir, "calls"),
		"PATH":                os.Getenv("PATH"),
	}
	return h
}

func (h *host) write(name, content string, mode os.FileMode) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, name), []byte(content), mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) run() (int, []string) {
	h.t.Helper()
	state := h.env["STATE_FILE"]
	if h.lastActive >= 0 {
		if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
			h.t.Fatal(err)
		}
		h.write("state/last-active", fmt.Sprintln(time.Now().Unix()-h.lastActive), 0o644)
	}
	cmd := exec.Command("bash", "idle-deallocate")
	for k, v := range h.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			h.t.Fatalf("idle-deallocate: %v", err)
		}
		code = ee.ExitCode()
	}
	h.t.Logf("output: %s", out)
	calls, err := os.ReadFile(h.env["CALLS"])
	if err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	if len(calls) == 0 {
		return code, nil
	}
	return code, strings.Split(strings.TrimSpace(string(calls)), "\n")
}

func (h *host) secondsSinceActive() int64 {
	h.t.Helper()
	data, err := os.ReadFile(h.env["STATE_FILE"])
	if err != nil {
		h.t.Fatal(err)
	}
	at, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		h.t.Fatal(err)
	}
	return time.Now().Unix() - at
}

func TestIdleLongEnoughQuiescesThenDeallocates(t *testing.T) {
	h := newHost(t)
	h.lastActive = 31 * 60
	code, calls := h.run()

	if code != 0 || len(calls) != 3 ||
		calls[0] != "hibd ls --state running -q" ||
		calls[1] != "quiesce" ||
		!strings.HasPrefix(calls[2], "deallocate idle for 186") ||
		!strings.HasSuffix(calls[2], "s: no SSH session, no running machine, load 0.05") {
		t.Errorf("exit %d, calls %q; want 0, the machine check, quiesce, then the deallocation", code, calls)
	}
}

func TestNotIdleLongEnough(t *testing.T) {
	h := newHost(t)
	h.lastActive = 29 * 60
	code, calls := h.run()
	if code != 0 || len(calls) != 1 || calls[0] != "hibd ls --state running -q" {
		t.Errorf("exit %d, calls %q; want only the machine check", code, calls)
	}
	if s := h.secondsSinceActive(); s < 29*60 {
		t.Errorf("last active %ds ago, want the old time kept", s)
	}
}

func TestFirstRunStartsTheClock(t *testing.T) {
	h := newHost(t)
	h.lastActive = -1
	code, calls := h.run()
	if code != 0 || len(calls) != 1 {
		t.Errorf("exit %d, calls %q; want no deallocation", code, calls)
	}
	if s := h.secondsSinceActive(); s > 5 {
		t.Errorf("last active %ds ago, want now", s)
	}
}

func TestActivity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*host) func()
	}{
		{"ssh session", func(h *host) func() {
			name := "hib-idle-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			h.env["SSH_SESSION_PATTERN"] = "^" + name
			cmd := exec.Command("bash", "-c", `exec -a "$0" sleep 60`, name)
			if err := cmd.Start(); err != nil {
				h.t.Fatal(err)
			}
			// pgrep can miss the process until exec has renamed it.
			for i := 0; i < 50 && exec.Command("pgrep", "-f", "^"+name).Run() != nil; i++ {
				time.Sleep(20 * time.Millisecond)
			}
			return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
		}},
		{"load", func(h *host) func() {
			h.write("loadavg", "1.00 0.50 0.20 2/200 1234\n", 0o644)
			return func() {}
		}},
		{"running machine", func(h *host) func() {
			h.env["HIBD_LS_OUT"] = "ib3k2vbwbn4bnwpmdvkq\n"
			return func() {}
		}},
		{"E2B not answering", func(h *host) func() {
			h.env["HIBD_LS_EXIT"] = "1"
			return func() {}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			h.lastActive = 45 * 60
			defer tc.setup(h)()
			code, calls := h.run()
			if code != 0 {
				t.Errorf("exit %d", code)
			}
			for _, c := range calls {
				if strings.HasPrefix(c, "deallocate") || strings.HasPrefix(c, "quiesce") {
					t.Errorf("calls %q; want no quiesce or deallocation", calls)
				}
			}
			if s := h.secondsSinceActive(); s > 5 {
				t.Errorf("last active %ds ago, want now", s)
			}
		})
	}
}

func TestQuiesceFailureKeepsTheVM(t *testing.T) {
	h := newHost(t)
	h.lastActive = 31 * 60
	h.env["QUIESCE_EXIT"] = "1"
	code, calls := h.run()
	if code != 1 || len(calls) != 2 || calls[1] != "quiesce" {
		t.Errorf("exit %d, calls %q; want 1 and no deallocation after quiesce", code, calls)
	}
}

func TestWithoutHibdSkipsTheMachineCheck(t *testing.T) {
	h := newHost(t)
	h.lastActive = 31 * 60
	h.env["HIBD"] = filepath.Join(h.dir, "not-installed")
	code, calls := h.run()
	if code != 0 || len(calls) != 2 || calls[0] != "quiesce" || !strings.HasPrefix(calls[1], "deallocate idle for ") {
		t.Errorf("exit %d, calls %q; want quiesce, then the deallocation", code, calls)
	}
}

func TestSSHSessionPattern(t *testing.T) {
	if _, err := exec.LookPath("grep"); err != nil {
		t.Skip("grep not found")
	}
	out, err := exec.Command("bash", "-c",
		`eval "$(grep -E '^(default_ssh_session_pattern|SSH_SESSION_PATTERN)=' idle-deallocate)"
		printf %s "$SSH_SESSION_PATTERN"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	pattern := string(out)
	if pattern == "" {
		t.Fatal("can't read the default SSH_SESSION_PATTERN from the script")
	}
	// OpenSSH 9.6 process titles, plus OpenSSH 9.8's sshd-session.
	for title, want := range map[string]bool{
		"sshd: azureuser@pts/0":                                   true,
		"sshd: azureuser@notty":                                   true,
		"sshd: azureuser":                                         true, // ssh -N
		"sshd-session: azureuser@pts/1":                           true,
		"sshd: azureuser [priv]":                                  false,
		"sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups": false,
		"sshd: unknown [net]":                                     false,
		"/usr/sbin/sshd -D":                                       false,
	} {
		cmd := exec.Command("grep", "-qE", pattern)
		cmd.Stdin = strings.NewReader(title + "\n")
		if got := cmd.Run() == nil; got != want {
			t.Errorf("pattern %q matches %q = %v, want %v", pattern, title, got, want)
		}
	}
}
