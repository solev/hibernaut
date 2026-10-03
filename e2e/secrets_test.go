//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/vault"
)

// TestSecretSwap is M3's demo with the laptop's GitHub login. The machine gets a placeholder,
// real clients in the machine reach GitHub with it, and a site the secret isn't bound to gets
// the placeholder, unchanged, over its own certificate.
func TestSecretSwap(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	token, login := laptopGitHub(t, ctx)
	name := newHibMachine(t, ctx)
	gh := addSecret(t, ctx, "api.github.com,github.com", token)

	// hibd writes the secret into running machines at once.
	if got := machineEnv(t, ctx, name, gh.env); got != gh.placeholder {
		t.Fatalf("%s in the machine = %q, want the placeholder %q", gh.env, redact(got, token), gh.placeholder)
	}
	if gh.placeholder == token || !vault.Compatible(gh.placeholder, token) {
		t.Fatalf("placeholder %q isn't shaped like the token", redact(gh.placeholder, token))
	}

	for _, c := range []struct{ client, script, want string }{
		{"curl", `curl -sf -m 30 -H "Authorization: Bearer $` + gh.env + `" https://api.github.com/user | jq -r .login`, login},
		{"gh", `GH_TOKEN=$` + gh.env + ` gh api user --jq .login`, login},
		{"node", `node -e 'fetch("https://api.github.com/user", {headers: {authorization: "Bearer " + process.env.` + gh.env +
			`}}).then(r => r.json()).then(j => console.log(j.login))'`, login},
		// git sends the placeholder as Basic auth to a private repository.
		{"git", `GIT_TERMINAL_PROMPT=0 git -c credential.helper= ls-remote "https://x-access-token:$` + gh.env +
			`@github.com/solev/hibernaut" HEAD | cut -f2`, "HEAD"},
	} {
		out, stderr, err := sshResult(ctx, nil, name, c.script)
		if err != nil || out != c.want {
			t.Errorf("%s with the placeholder = %q, %v, want %q\n%s", c.client, redact(out, token), err, c.want, redact(stderr, token))
		}
	}

	// postman-echo.com isn't bound, so it gets the placeholder over its own certificate.
	echoed := mustSSHMachine(t, ctx, nil, name,
		`curl -sf -m 30 -H "Authorization: Bearer $`+gh.env+`" https://postman-echo.com/headers | jq -r .headers.authorization`)
	if echoed != "Bearer "+gh.placeholder {
		t.Errorf("postman-echo.com saw %q, want the placeholder", redact(echoed, token))
	}
	if issuer := certIssuer(t, ctx, name, "postman-echo.com"); strings.Contains(issuer, "Hibernaut") {
		t.Errorf("postman-echo.com's certificate came from the proxy: %s", issuer)
	}
	if issuer := certIssuer(t, ctx, name, "api.github.com"); !strings.Contains(issuer, "Hibernaut") {
		t.Errorf("api.github.com's certificate didn't come from the proxy: %s", issuer)
	}
	requireNoLeaks(t, ctx, name)
}

// TestAuditLog checks that hib log shows a swap with its machine, site and secret name, and
// that neither hib log nor the audit file holds the real value. The secret is a made-up value
// bound to postman-echo.com, which echoes the swapped header back, so the test also sees the
// proxy scrub the real value out of the response.
func TestAuditLog(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx)
	value := fmt.Sprintf("sk-e2e-%016x%016x", rand.Uint64(), rand.Uint64())
	echo := addSecret(t, ctx, "postman-echo.com", value)
	machineEnv(t, ctx, name, echo.env)

	out := mustSSHMachine(t, ctx, nil, name, `curl -sf -m 30 -H "X-E2e: $`+echo.env+`" https://postman-echo.com/headers`)
	if strings.Contains(out, value) {
		t.Fatalf("the real value reached the machine in postman-echo.com's response:\n%s", out)
	}
	if !strings.Contains(out, echo.placeholder) {
		t.Errorf("postman-echo.com's response doesn't hold the placeholder, so nothing was swapped and scrubbed:\n%s", out)
	}

	log := hib(t, ctx, "log", "-n", "500")
	if strings.Contains(log, value) {
		t.Errorf("hib log shows the real value")
	}
	found := false
	for line := range strings.SplitSeq(log, "\n") {
		// TIME MACHINE SITE METHOD PATH SECRETS
		f := strings.Fields(line)
		if len(f) == 6 && f[1] == name && f[2] == "postman-echo.com" && f[3] == "GET" && f[4] == "/headers" &&
			strings.Contains(","+f[5]+",", ","+echo.name+",") {
			found = true
		}
	}
	if !found {
		t.Errorf("hib log has no swap of %s by %s on postman-echo.com:\n%s", echo.name, name, log)
	}

	audit, err := vm.ssh(ctx, "sudo sh -c 'cat /var/log/hibernaut/audit.jsonl*'")
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	if strings.Contains(audit, value) {
		t.Errorf("the audit log holds the real value")
	}
	requireNoLeaks(t, ctx, name)
}

// TestLeakScan checks that the leak scan finds a value planted in a machine's memory and disk, and
// that the dev image holds no real secret. requireNoLeaks scans the other tests' machines.
func TestLeakScan(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	ensureImage(t)
	canary := leakScan(t, ctx, "-canary")
	if !canary.MemoryFound || !canary.DiskFound {
		t.Errorf("the canary scan missed the canary: memory %t, disk %t", canary.MemoryFound, canary.DiskFound)
	}

	addSecret(t, ctx, "postman-echo.com", fmt.Sprintf("sk-e2e-%016x%016x", rand.Uint64(), rand.Uint64()))
	image := leakScan(t, ctx, "-image", "dev")
	if image.SecretCount == 0 {
		t.Error("the image scan had no secrets to look for")
	}
	for _, h := range image.Hits {
		t.Errorf("the dev image holds secret %s (%s) in %s at %d", h.Secret, h.Encoding, h.Where, h.Offset)
	}
}

// requireNoLeaks pauses machine name, and checks that its memory and disk hold no real secret.
func requireNoLeaks(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	r := leakScan(t, ctx, name)
	if r.SecretCount == 0 {
		t.Errorf("the leak scan of %s had no secrets to look for", name)
	}
	for _, h := range r.Hits {
		t.Errorf("%s holds secret %s (%s) in %s at %d", name, h.Secret, h.Encoding, h.Where, h.Offset)
	}
}

// leakScan runs sudo hibd leakscan -json on the host. It exits 1 when it finds something, so
// only a reply without a result fails the test here.
func leakScan(t *testing.T, ctx context.Context, args ...string) hibapi.LeakScan {
	t.Helper()
	out, err := vm.ssh(ctx, "sudo hibd leakscan -json "+strings.Join(args, " "))
	var r hibapi.Response
	if jsonErr := json.Unmarshal([]byte(out), &r); jsonErr != nil || r.LeakScan == nil {
		t.Fatalf("hibd leakscan %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return *r.LeakScan
}

// secret is a secret a test added to the host's vault.
type secret struct{ name, env, placeholder string }

// addSecret stores value in the host's vault through hib, bound to sites, under a fresh e2e-
// name, and removes it when the test ends. The placeholder is the one hib secret ls shows.
func addSecret(t *testing.T, ctx context.Context, sites, value string) secret {
	t.Helper()
	suffix := fmt.Sprintf("%08x", rand.Uint32())
	s := secret{name: "e2e-" + suffix, env: "E2E_" + strings.ToUpper(suffix)}
	cmd := exec.CommandContext(ctx, hibBin, "-config", hibDir, "secret", "add", s.name, "-env", s.env, "-bind", sites)
	cmd.Stdin = strings.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hib secret add %s: %v\n%s", s.name, err, redact(string(out), value))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, stderr, code := runHib(ctx, "secret", "rm", s.name); code != 0 {
			t.Errorf("cleanup: hib secret rm %s exited %d: %s", s.name, code, stderr)
		}
	})
	for line := range strings.SplitSeq(hib(t, ctx, "secret", "ls"), "\n") {
		// NAME ENV SITES PLACEHOLDER
		if f := strings.Fields(line); len(f) == 4 && f[0] == s.name {
			s.placeholder = f[3]
		}
	}
	if s.placeholder == "" {
		t.Fatalf("hib secret ls doesn't list %s", s.name)
	}
	return s
}

// deleteLeftoverSecrets removes the e2e- secrets a crashed run left in the host's vault.
func deleteLeftoverSecrets(ctx context.Context) error {
	stdout, stderr, code := runHib(ctx, "secret", "ls")
	if code != 0 {
		return fmt.Errorf("hib secret ls exited %d: %s", code, stderr)
	}
	for line := range strings.SplitSeq(stdout, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "e2e-") {
			continue
		}
		if _, stderr, code := runHib(ctx, "secret", "rm", f[0]); code != 0 {
			return fmt.Errorf("hib secret rm %s exited %d: %s", f[0], code, stderr)
		}
	}
	return nil
}

// laptopGitHub returns the laptop's GitHub token and login, from gh.
func laptopGitHub(t *testing.T, ctx context.Context) (token, login string) {
	t.Helper()
	token, err := output(exec.CommandContext(ctx, "gh", "auth", "token"))
	if err != nil || token == "" {
		t.Fatalf("gh auth token: %v (log in on the laptop with gh auth login)", err)
	}
	if login, err = output(exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login")); err != nil || login == "" {
		t.Fatalf("gh api user: %v", err)
	}
	return token, login
}

// machineEnv returns variable env as a login session in machine name sees it. It waits up to
// a minute, because hibd's daemon also delivers secrets on its next look.
func machineEnv(t *testing.T, ctx context.Context, name, env string) string {
	t.Helper()
	var value string
	waitFor(t, ctx, env+" in "+name, time.Minute, func() bool {
		out, _, err := sshResult(ctx, nil, name, "printenv "+env)
		value = out
		return err == nil && out != ""
	})
	return value
}

// certIssuer returns the issuer of the certificate machine name sees for host.
func certIssuer(t *testing.T, ctx context.Context, name, host string) string {
	t.Helper()
	return mustSSHMachine(t, ctx, nil, name, fmt.Sprintf(
		`openssl s_client -connect %[1]s:443 -servername %[1]s </dev/null 2>/dev/null | openssl x509 -noout -issuer`, host))
}

// redact hides value in s, for failure messages.
func redact(s, value string) string {
	return strings.ReplaceAll(s, value, "<real value>")
}
