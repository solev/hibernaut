//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/vault"
)

func TestSecretSwap(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	token, login := laptopGitHub(t, ctx)
	name := newHibMachine(t, ctx)
	gh := addSecret(t, ctx, "api.github.com,github.com", token)

	if got := pollMachineEnv(t, ctx, name, gh.env); got != gh.placeholder {
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
		{"git", `GIT_TERMINAL_PROMPT=0 git -c credential.helper= ls-remote "https://x-access-token:$` + gh.env +
			`@github.com/solev/hibernaut" HEAD | cut -f2`, "HEAD"},
	} {
		out, stderr, err := sshResult(ctx, nil, name, c.script)
		if err != nil || out != c.want {
			t.Errorf("%s with the placeholder = %q, %v, want %q\n%s", c.client, redact(out, token), err, c.want, redact(stderr, token))
		}
	}

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
	value := fmt.Sprintf("sk-e2e-%016x%016x", rand.Uint64(), rand.Uint64())
	echo := addSecret(t, ctx, "postman-echo.com", value)
	pollMachineEnv(t, ctx, name, echo.env)
	requireEchoAuthorization(t, ctx, name, echo, value)
	requireNoLeaks(t, ctx, name)
}

func requireEchoAuthorization(t *testing.T, ctx context.Context, machine string, s secret, value string) {
	t.Helper()
	waitFor(t, ctx, "postman-echo.com interception", time.Minute, func() bool {
		return strings.Contains(certIssuer(t, ctx, machine, "postman-echo.com"), "Hibernaut")
	})
	for _, c := range []struct{ script, want string }{
		{
			`curl -sf -m 30 -H "Authorization: ` + `Bearer $` + s.env + `" https://postman-echo.com/headers | jq -r .headers.authorization`,
			"Bearer " + s.placeholder,
		},
		{
			`curl -sf -m 30 -u "e2e:$` + s.env + `" https://postman-echo.com/headers | jq -r .headers.authorization`,
			"Basic " + base64.StdEncoding.EncodeToString([]byte("e2e:"+s.placeholder)),
		},
	} {
		if out := mustSSHMachine(t, ctx, nil, machine, c.script); out != c.want {
			t.Errorf("Authorization echo = %q, want %q", redact(out, value), c.want)
		}
	}
}

func TestAuditLog(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx)
	value := fmt.Sprintf("sk-e2e-%016x%016x", rand.Uint64(), rand.Uint64())
	echo := addSecret(t, ctx, "postman-echo.com", value)
	pollMachineEnv(t, ctx, name, echo.env)
	requireEchoAuthorization(t, ctx, name, echo, value)
	for _, script := range []string{
		`curl -sf -m 30 -H "X-E2e: $` + echo.env + `" https://postman-echo.com/headers | jq -r '.headers["x-e2e"]'`,
		`curl -sf -m 30 --data-urlencode "token=$` + echo.env + `" https://postman-echo.com/post | jq -r .form.token`,
	} {
		if out := mustSSHMachine(t, ctx, nil, name, script); out != echo.placeholder {
			t.Errorf("content echo = %q, want %q", redact(out, value), echo.placeholder)
		}
	}

	log := hib(t, ctx, "log", "-n", "500")
	if strings.Contains(log, value) {
		t.Errorf("hib log shows the real value")
	}
	found := false
	for line := range strings.SplitSeq(log, "\n") {
		f := strings.Fields(line)
		if len(f) == 6 && f[1] == name && f[2] == "postman-echo.com" && f[3] == "GET" && f[4] == "/headers" &&
			strings.Contains(","+f[5]+",", ","+echo.name+",") {
			found = true
		}
	}
	if !found {
		t.Errorf("hib log has no swap of %s by %s on postman-echo.com:\n%s", echo.name, name, log)
	}

	auditData, err := vm.ssh(ctx, "sudo sh -c 'cat /var/log/hibernaut/audit.jsonl*'")
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	if strings.Contains(auditData, value) {
		t.Errorf("the audit log holds the real value")
	}
	var entries []audit.Entry
	for line := range strings.SplitSeq(auditData, "\n") {
		var entry audit.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("audit entry: %v", err)
		}
		if entry.Machine == name && entry.Site == "postman-echo.com" {
			entries = append(entries, entry)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("Authorization swaps produced %d audit entries, want 2. Content must not be audited.", len(entries))
	}
	for _, entry := range entries {
		if entry.Method != "GET" || entry.Path != "/headers" || len(entry.Secrets) != 1 || entry.Secrets[0] != echo.name {
			t.Errorf("audit entry = %+v", entry)
		}
	}
	requireNoLeaks(t, ctx, name)
}

func TestLeakScan(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	ensureImage(t)
	canary := leakScanAllowingHits(t, ctx, "-canary")
	if !canary.MemoryFound || !canary.DiskFound {
		t.Errorf("the canary scan missed the canary: memory %t, disk %t", canary.MemoryFound, canary.DiskFound)
	}

	addSecret(t, ctx, "postman-echo.com", fmt.Sprintf("sk-e2e-%016x%016x", rand.Uint64(), rand.Uint64()))
	image := leakScanAllowingHits(t, ctx, "-image", "dev")
	if image.SecretCount == 0 {
		t.Error("the image scan had no secrets to look for")
	}
	for _, h := range image.Hits {
		t.Errorf("the dev image holds secret %s (%s) in %s at %d", h.Secret, h.Encoding, h.Where, h.Offset)
	}
}

func requireNoLeaks(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	r := leakScanAllowingHits(t, ctx, name)
	if r.SecretCount == 0 {
		t.Errorf("the leak scan of %s had no secrets to look for", name)
	}
	for _, h := range r.Hits {
		t.Errorf("%s holds secret %s (%s) in %s at %d", name, h.Secret, h.Encoding, h.Where, h.Offset)
	}
}

func leakScanAllowingHits(t *testing.T, ctx context.Context, args ...string) hibapi.LeakScan {
	t.Helper()
	out, err := vm.ssh(ctx, "sudo hibd leakscan -json "+strings.Join(args, " "))
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		t.Fatalf("hibd leakscan %s: %v", strings.Join(args, " "), err)
	}
	var r hibapi.Response
	if jsonErr := json.Unmarshal([]byte(out), &r); jsonErr != nil || r.LeakScan == nil {
		t.Fatalf("hibd leakscan %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return *r.LeakScan
}

type secret struct{ name, env, placeholder string }

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
		if f := strings.Fields(line); len(f) == 4 && f[0] == s.name {
			s.placeholder = f[3]
		}
	}
	if s.placeholder == "" {
		t.Fatalf("hib secret ls doesn't list %s", s.name)
	}
	return s
}

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

func pollMachineEnv(t *testing.T, ctx context.Context, name, env string) string {
	t.Helper()
	var value string
	waitFor(t, ctx, env+" in "+name, time.Minute, func() bool {
		out, _, err := sshResult(ctx, nil, name, "printenv "+env)
		value = out
		return err == nil && out != ""
	})
	return value
}

func certIssuer(t *testing.T, ctx context.Context, name, host string) string {
	t.Helper()
	return mustSSHMachine(t, ctx, nil, name, fmt.Sprintf(
		`openssl s_client -connect %[1]s:443 -servername %[1]s </dev/null 2>/dev/null | openssl x509 -noout -issuer`, host))
}

func redact(s, value string) string {
	return strings.ReplaceAll(s, value, "<real value>")
}
