//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/hibapi"
)

const codexSmoke = `codex exec --skip-git-repo-check "Reply with exactly OK."`
const copilotSmoke = `copilot -p "Reply with exactly OK." -s --allow-all-tools --no-ask-user`

func TestCodexRefresh(t *testing.T) {
	ctx := testContext(t, 25*time.Minute)
	status := toolStatus(t, ctx)
	requireCodexConnected(t, status)
	existing := newHibMachine(t, ctx)
	requireCodexPackage(t, ctx, existing)
	requireToolOK(t, ctx, existing, codexSmoke)

	out, err := vm.ssh(ctx, "sudo -n hibd codex refresh -force -json")
	if err != nil {
		t.Fatal("the forced host Codex refresh failed. Check hib status and reconnect with hib connect codex")
	}
	var reply hibapi.Response
	if err := json.Unmarshal([]byte(out), &reply); err != nil || reply.V != hibapi.Version ||
		reply.Error != "" || reply.Codex == nil {
		t.Fatal("the forced refresh didn't return a Codex profile")
	}
	if !reply.Codex.LastRefresh.After(status.Codex.LastRefresh) {
		t.Fatal("the forced refresh didn't advance the profile's lastRefresh")
	}
	if reply.Codex.Health != hibapi.CodexOK || reply.Codex.AccountID != status.Codex.AccountID {
		t.Fatal("the forced refresh didn't preserve a healthy login for the same account")
	}
	requireToolOK(t, ctx, existing, codexSmoke)
	fresh := newHibMachine(t, ctx)
	requireToolOK(t, ctx, fresh, codexSmoke)
	for _, name := range []string{existing, fresh} {
		scan := requireNoLeaks(t, ctx, name, toolSecrets(status)...)
		if scan.SecretCount <= len(scan.Secrets) {
			t.Error("the leak scan after refreshing must cover retired token values as well")
		}
	}
}

func TestCopilot(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	status := toolStatus(t, ctx)
	if status.GitHub == nil {
		t.Skip("GitHub isn't connected. Run hib connect github --from-gh, then run the end-to-end tests again")
	}
	name := newHibMachine(t, ctx)
	requireToolOK(t, ctx, name, copilotSmoke)
	requireNoLeaks(t, ctx, name, toolSecrets(status)...)
}

func toolStatus(t *testing.T, ctx context.Context) hibapi.Status {
	t.Helper()
	out, err := vm.ssh(ctx, "sudo -n hibd status -json")
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		t.Fatal("couldn't read host status for the AI tool tests")
	}
	var reply hibapi.Response
	if err := json.Unmarshal([]byte(out), &reply); err != nil || reply.V != hibapi.Version ||
		reply.Error != "" || reply.Status == nil {
		t.Fatal("host status didn't return a valid status reply")
	}
	return *reply.Status
}

func requireCodexConnected(t *testing.T, status hibapi.Status) {
	t.Helper()
	if status.Codex == nil {
		t.Skip("Codex isn't connected. Run hib connect codex, then run the end-to-end tests again")
	}
	if status.Codex.Health != hibapi.CodexOK {
		t.Fatal("the Codex login isn't healthy. Run hib connect codex, then run the end-to-end tests again")
	}
}

func requireCodexPackage(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	_, _, err := sshResult(ctx, nil, name, `set -eu
entry=$(readlink /usr/local/bin/codex)
case "$entry" in /usr/local/lib/codex-*/bin/codex) ;; *) exit 1 ;; esac
test -x "$entry"
package=$(dirname "$(dirname "$entry")")
test -x "$package/bin/codex-code-mode-host"
test -f "$package/codex-package.json"
unowned=$(find "$package" \( ! -user root -o ! -group root \) -print -quit)
test -z "$unowned"
test "$(stat -c '%U:%G' /usr/local/bin/codex)" = root:root`)
	if err != nil {
		t.Fatal("the machine must have a root-owned Codex package with code mode. Rebuild the dev image")
	}
}

func requireToolOK(t *testing.T, ctx context.Context, name, command string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	stdout, _, err := sshResult(ctx, nil, name, command)
	if err != nil {
		t.Fatal("the AI tool failed in the machine. Check hib status and the connected account's plan")
	}
	if stdout != "OK" {
		// Do not print arbitrary model output. A broken proxy could have let a token into it.
		t.Fatal("the AI tool didn't reply with exactly OK without a login prompt")
	}
}

func toolSecrets(status hibapi.Status) []string {
	var names []string
	if status.GitHub != nil {
		names = append(names, "github")
	}
	if status.Codex != nil {
		names = append(names, codex.AccessName, codex.RefreshName, codex.IDName)
	}
	return names
}
