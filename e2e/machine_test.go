//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

// TestMachine checks hib new, reaching the machine over ssh NAME.hib as the
// unprivileged user, run commands with their exit codes, see it in hib ls, pause and resume it
// with its files and processes intact, and remove it.
func TestMachine(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx)

	start := time.Now()
	if got := sshMachine(t, ctx, name, "true; whoami"); strings.TrimSpace(got) != "user" {
		t.Errorf("whoami = %q, want user", strings.TrimSpace(got))
	}
	t.Logf("first ssh after hib new took %v", time.Since(start).Round(time.Millisecond))

	if out := hib(t, ctx, "exec", name, "--", "uname", "-s"); strings.TrimSpace(out) != "Linux" {
		t.Errorf("uname -s = %q, want Linux", strings.TrimSpace(out))
	}
	if _, _, code := runHib(ctx, "exec", name, "--", "exit 3"); code != 3 {
		t.Errorf("hib exec of `exit 3` returned %d, want 3", code)
	}

	ls := hib(t, ctx, "ls")
	if row := nameRow(ls, name); row == "" {
		t.Errorf("hib ls doesn't list %s:\n%s", name, ls)
	} else if !strings.Contains(row, "running") || !strings.Contains(row, "dev") {
		t.Errorf("hib ls row for %s = %q, want it running on dev", name, row)
	}

	t.Run("state survives pause", func(t *testing.T) { survivesPause(t, ctx, name) })

	t.Run("exec wakes a paused machine", func(t *testing.T) {
		hib(t, ctx, "pause", name)
		start := time.Now()
		if out := hib(t, ctx, "exec", name, "--", "echo", "woke"); strings.TrimSpace(out) != "woke" {
			t.Errorf("hib exec on a paused machine printed %q, want woke", out)
		}
		t.Logf("hib exec on a paused machine took %v", time.Since(start).Round(time.Millisecond))
	})

	hib(t, ctx, "rm", name)
	if row := nameRow(hib(t, ctx, "ls"), name); row != "" {
		t.Errorf("hib ls still lists %s after rm: %q", name, row)
	}
	if ms, err := vm.engine.List(ctx, engine.ListOptions{Metadata: map[string]string{"hib.name": name}}); err != nil {
		t.Fatalf("List: %v", err)
	} else if len(ms) != 0 {
		t.Errorf("the engine still has %d machines named %s after rm", len(ms), name)
	}
	if _, err := output(sshMachineCmd(ctx, nil, name, "true")); err == nil {
		t.Errorf("ssh %s.hib still works after rm", name)
	}
}

func survivesPause(t *testing.T, ctx context.Context, name string) {
	sshMachine(t, ctx, name, "echo kept > /home/user/marker; tmux new -d -s keep 'sleep 100000'")

	hib(t, ctx, "pause", name)
	if row := nameRow(hib(t, ctx, "ls"), name); !strings.Contains(row, "paused") {
		t.Fatalf("hib ls row for %s = %q, want it paused", name, row)
	}

	start := time.Now()
	if got := sshMachine(t, ctx, name, "cat /home/user/marker"); strings.TrimSpace(got) != "kept" {
		t.Errorf("after resume the marker = %q, want kept", strings.TrimSpace(got))
	}
	t.Logf("resume on ssh took %v", time.Since(start).Round(time.Millisecond))
	if _, err := output(sshMachineCmd(ctx, nil, name, "tmux has-session -t keep")); err != nil {
		t.Errorf("the tmux session didn't survive the pause: %v", err)
	}
}

func nameRow(ls, name string) string {
	for _, line := range strings.Split(ls, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == name {
			return line
		}
	}
	return ""
}

// TestMachineSecurity checks the ssh boundary: only the unprivileged user with a key may log in,
// the machine can't borrow the laptop's agent, X11 or a reverse tunnel, and each machine's host
// key is pinned so a swapped key is caught.
func TestMachineSecurity(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx)
	sshMachine(t, ctx, name, "true")

	if _, stderr, err := sshResult(ctx, []string{"-o", "PreferredAuthentications=none"}, name, "true"); err == nil {
		t.Error("ssh with no auth methods connected, want it refused")
	} else if !strings.Contains(stderr, "publickey") {
		t.Errorf("the server didn't say it wants publickey:\n%s", stderr)
	}

	for _, user := range []string{"root", "hib-ws"} {
		if _, _, err := sshResult(ctx, []string{"-l", user}, name, "true"); err == nil {
			t.Errorf("ssh -l %s connected, want it refused", user)
		}
	}

	if got := mustSSHMachine(t, ctx, []string{"-A"}, name, `printf %s "${SSH_AUTH_SOCK:-unset}"`); got != "unset" {
		t.Errorf("SSH_AUTH_SOCK in the machine = %q, want unset", got)
	}
	if got := mustSSHMachine(t, ctx, []string{"-X"}, name, `printf %s "${DISPLAY:-unset}"`); got != "unset" {
		t.Errorf("DISPLAY in the machine = %q, want unset", got)
	}

	if _, _, err := sshResult(ctx, []string{"-o", "ExitOnForwardFailure=yes", "-R", "127.0.0.1:0:127.0.0.1:22"}, name, "true"); err == nil {
		t.Error("ssh -R connected, want remote forwarding refused")
	}

	if lap, host := laptopPinKey(t, name), hostPinKey(t, ctx, machineID(t, ctx, name)); lap != host {
		t.Errorf("the laptop pinned %q but the host pinned %q", lap, host)
	}

	other := newHibMachine(t, ctx)
	sshMachine(t, ctx, other, "true")
	if laptopPinKey(t, name) == laptopPinKey(t, other) {
		t.Error("two machines were pinned the same host key")
	}

	t.Run("a swapped pin is caught", func(t *testing.T) { swappedPinIsCaught(t, ctx, name) })
}

func swappedPinIsCaught(t *testing.T, ctx context.Context, name string) {
	id := machineID(t, ctx, name)
	original := hostPinKey(t, ctx, id)
	t.Cleanup(func() { setHostPin(t, ctx, id, original) })

	setHostPin(t, ctx, id, wrongHostKey(t))
	if _, stderr, err := sshResult(ctx, nil, name, "true"); err == nil {
		t.Error("ssh connected though the host pinned the wrong key, want it refused")
	} else if !strings.Contains(stderr, "Host key verification failed") {
		t.Errorf("ssh didn't report a host-key failure:\n%s", stderr)
	}
}

// TestMachineNetwork checks the sandbox firewall: a machine reaches the internet but not the
// Azure platform endpoints a sandbox must never touch.
func TestMachineNetwork(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx)

	if code := curlCode(t, ctx, name, "https://github.com"); !strings.HasPrefix(code, "2") && !strings.HasPrefix(code, "3") {
		t.Errorf("curl https://github.com from the machine = %q, want a 2xx or 3xx", code)
	}

	for _, url := range []string{"http://168.63.129.16:80/", "http://168.63.129.16:32526/"} {
		if code := curlCode(t, ctx, name, url); code != "000" {
			t.Errorf("curl %s from the machine = %q, want 000 (blocked)", url, code)
		}
	}
}

// TestMachineTerminal checks that full-screen programs work from the laptop's terminal.
// For a terminal the machine still doesn't know, the login shell falls back to xterm-256color.
func TestMachineTerminal(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx)

	// -tt gives the command a terminal, and SetEnv TERM replaces the TERM ssh sends with it.
	// tput clear fails with "unknown terminal" when the machine has no terminfo for TERM.
	for _, term := range []string{"xterm-ghostty", "xterm-kitty", "wezterm", "alacritty", "xterm-256color"} {
		out, stderr, err := sshResult(ctx, []string{"-tt", "-o", "SetEnv=TERM=" + term}, name,
			`tput clear >/dev/null && printf 'term=%s\n' "$TERM"`)
		if err != nil || !strings.Contains(out, "term="+term) {
			t.Errorf("tput clear with TERM=%s: %v\n%s\n%s", term, err, out, stderr)
		}
	}

	cmd := sshMachineCmd(ctx, []string{"-tt", "-o", "SetEnv=TERM=hib-no-such-terminal"}, name, "")
	cmd.Stdin = strings.NewReader("printf 'term=%s\\n' \"$TERM\"; tput clear >/dev/null && echo cleared''ok\nexit\n")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "term=xterm-256color") || !strings.Contains(string(out), "clearedok") {
		t.Errorf("logging in with an unknown TERM: %v\n%s", err, out)
	}
}

func sshResult(ctx context.Context, extra []string, name, command string) (stdout, stderr string, err error) {
	cmd := sshMachineCmd(ctx, extra, name, command)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return strings.TrimSpace(o.String()), strings.TrimSpace(e.String()), err
}

func mustSSHMachine(t *testing.T, ctx context.Context, extra []string, name, command string) string {
	t.Helper()
	stdout, stderr, err := sshResult(ctx, extra, name, command)
	if err != nil {
		t.Fatalf("ssh %v %s %q: %v\n%s", extra, name, command, err, stderr)
	}
	return stdout
}

func curlCode(t *testing.T, ctx context.Context, name, url string) string {
	t.Helper()
	out, stderr, _ := sshResult(ctx, nil, name, fmt.Sprintf("curl -s -m 8 -o /dev/null -w '%%{http_code}' %s", url))
	if len(out) != 3 {
		t.Fatalf("curl to %s gave no status (out=%q stderr=%q)", url, out, stderr)
	}
	return out
}

func machineID(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	ms, err := vm.engine.List(ctx, engine.ListOptions{Metadata: map[string]string{"hib.name": name}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("want one machine named %s, found %d", name, len(ms))
	}
	return ms[0].ID
}

func laptopPinKey(t *testing.T, name string) string {
	t.Helper()
	f := strings.Fields(readFile(t, filepath.Join(hibDir, "known_hosts", name+".hib")))
	if len(f) < 3 {
		t.Fatalf("the pin for %s isn't a known_hosts line: %q", name, f)
	}
	return f[1] + " " + f[2]
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func hostPinKey(t *testing.T, ctx context.Context, id string) string {
	t.Helper()
	out, err := vm.ssh(ctx, "sudo cat /var/lib/hibernaut/host-keys/"+id)
	if err != nil {
		t.Fatalf("reading the host's pin for %s: %v", id, err)
	}
	return strings.TrimSpace(out)
}

func setHostPin(t *testing.T, ctx context.Context, id, key string) {
	t.Helper()
	cmd := fmt.Sprintf("echo '%s' | sudo tee /var/lib/hibernaut/host-keys/%s >/dev/null", key, id)
	if _, err := vm.ssh(ctx, cmd); err != nil {
		t.Fatalf("writing the host's pin for %s: %v", id, err)
	}
}

func wrongHostKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrapping the key: %v", err)
	}
	return hibapi.FormatHostKey(key)
}
