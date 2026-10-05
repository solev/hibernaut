//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

const githubFixture = "solev/hibernaut-e2e"

func TestGitHubFullFlow(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	token, login, email := connectGitHub(t, ctx)
	checkGitHubFullFlow(t, ctx, login, email, token)
}

func TestDeviceFlow(t *testing.T) {
	if os.Getenv("HIB_E2E_DEVICE_FLOW") != "1" {
		t.Skip("approve a GitHub code by hand: HIB_E2E_DEVICE_FLOW=1 go test -tags e2e -run '^TestDeviceFlow$' -v -timeout 40m ./e2e")
	}
	connectCtx := testContext(t, 17*time.Minute)
	stdout, stderr, code := runHibStderr(connectCtx, os.Stderr, "connect", "github")
	if code != 0 {
		t.Fatalf("hib connect github exited %d: %s%s", code, stdout, stderr)
	}
	prompt := false
	for line := range strings.SplitSeq(stderr, "\n") {
		if hibapi.IsGitHubDevicePrompt(line) {
			prompt = true
		}
	}
	if !prompt {
		t.Fatal("hib connect github didn't show the device prompt")
	}
	login, email := connectedGitHubAccount(t, stdout)
	flowCtx := testContext(t, 20*time.Minute)
	checkGitHubFullFlow(t, flowCtx, login, email, "")
}

func checkGitHubFullFlow(t *testing.T, ctx context.Context, login, email, token string) {
	t.Helper()
	stdout, stderr, code := runHib(ctx, "status")
	if token != "" && strings.Contains(stdout+stderr, token) {
		t.Fatal("hib status exposed the real token")
	}
	if code != 0 {
		t.Fatalf("hib status exited %d: %s%s", code, stdout, stderr)
	}
	foundLogin, secretOK := false, false
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "GitHub" && strings.TrimSuffix(fields[1], ",") == login {
			foundLogin = true
		}
		if len(fields) > 2 && fields[0] == "github" && fields[2] == "ok" {
			secretOK = true
		}
	}
	if !foundLogin || !secretOK {
		t.Fatalf("hib status doesn't show the GitHub login %q and secret github ok:\n%s", login, stdout)
	}
	name := newHibMachine(t, ctx)
	placeholder := pollMachineEnv(t, ctx, name, "GH_TOKEN")
	if token != "" && placeholder == token {
		t.Fatal("the machine has the real token in GH_TOKEN")
	}
	if got := githubScript(t, ctx, name, token, `git config user.email`); got != email {
		t.Fatalf("git user.email = %q, want %q", got, email)
	}
	if got := githubScript(t, ctx, name, token, `git config user.name`); got == "" {
		t.Fatal("git user.name isn't set")
	}
	credentials := githubScript(t, ctx, name, token,
		`printf 'protocol=https\nhost=github.com\n\n' | GIT_TERMINAL_PROMPT=0 git credential fill`)
	if !strings.Contains(credentials, "username=x-access-token\n") ||
		!strings.Contains(credentials+"\n", "password="+placeholder+"\n") {
		t.Fatal("git's credential helper didn't return x-access-token and the placeholder")
	}
	githubScript(t, ctx, name, token, `
git clone https://github.com/solev/hibernaut-e2e github-https
git clone git@github.com:solev/hibernaut-e2e.git github-ssh
git clone ssh://git@github.com/solev/hibernaut-e2e.git github-ssh-url`)
	branch := newGitHubBranch(t)
	if got := githubScript(t, ctx, name, token, `
cd github-https
git switch -c "$1"
printf 'GitHub end-to-end branch %s.\n' "$1" > e2e.txt
git add e2e.txt
git commit -m 'e2e: Check the GitHub flow' >/dev/null
git show -s --format=%ae HEAD`, branch); got != email {
		t.Fatalf("commit author email = %q, want %q", got, email)
	}
	githubScript(t, ctx, name, token, `git -C github-https push --set-upstream origin "$1"`, branch)
	githubScript(t, ctx, name, token, `
cd github-https
gh pr create --repo solev/hibernaut-e2e --head "$1" --base main \
	--title 'e2e: Check the GitHub flow' --body 'Created by the Hibernaut end-to-end test.'`, branch)
	number := githubScript(t, ctx, name, token,
		`gh pr view "$1" --repo solev/hibernaut-e2e --json number --jq .number`, branch)
	if n, err := strconv.Atoi(number); err != nil || n <= 0 {
		t.Fatalf("gh pr view didn't give a PR number: %q", number)
	}
	githubScript(t, ctx, name, token, `
cd github-https
gh pr close "$1" --repo solev/hibernaut-e2e --delete-branch`, number)
	requireNoLeaks(t, ctx, name)
}

func TestWorkflowPush(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	token, _, _ := connectGitHub(t, ctx)
	response, err := output(exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com", "-i", "user", "--jq", ".login"))
	if err != nil {
		t.Fatalf("checking the laptop's GitHub scopes: %v", err)
	}
	for line := range strings.SplitSeq(response, "\n") {
		header, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(header, "X-OAuth-Scopes") {
			continue
		}
		workflow := false
		for scope := range strings.SplitSeq(value, ",") {
			if strings.TrimSpace(scope) == "workflow" {
				workflow = true
			}
		}
		if !workflow {
			t.Skip("the connected token has no workflow scope. To fix it, run gh auth refresh -h github.com -s workflow, then hib connect github --from-gh")
		}
		break
	}
	name := newHibMachine(t, ctx)
	pollMachineEnv(t, ctx, name, "GH_TOKEN")
	branch := newGitHubBranch(t)
	githubScript(t, ctx, name, token, `
git clone https://github.com/solev/hibernaut-e2e github-workflow
cd github-workflow
git switch -c "$1"
printf '\n# Hibernaut end-to-end branch %s.\n' "$1" >> .github/workflows/e2e.yml
git add .github/workflows/e2e.yml
git commit -m 'e2e: Check workflow pushes'
git push --set-upstream origin "$1"
git push origin --delete "$1"`, branch)
	requireNoLeaks(t, ctx, name)
}

func connectGitHub(t *testing.T, ctx context.Context) (token, login, email string) {
	t.Helper()
	token, login = laptopGitHub(t, ctx)
	stdout, stderr, code := runHib(ctx, "connect", "github", "--from-gh")
	if strings.Contains(stdout+stderr, token) {
		t.Fatal("hib connect github exposed the real token")
	}
	if code != 0 {
		t.Fatalf("hib connect github --from-gh exited %d: %s%s", code, stdout, stderr)
	}
	connectedLogin, email := connectedGitHubAccount(t, stdout)
	if connectedLogin != login {
		t.Fatalf("hib connected %q, want the laptop's GitHub login %q", connectedLogin, login)
	}
	return token, login, email
}

func connectedGitHubAccount(t *testing.T, stdout string) (login, email string) {
	t.Helper()
	for line := range strings.SplitSeq(stdout, "\n") {
		account, ok := strings.CutPrefix(line, "connected GitHub as ")
		if !ok {
			continue
		}
		who, email, ok := strings.Cut(account, ". Machines commit as ")
		fields := strings.Fields(who)
		if ok && len(fields) > 0 && email != "" {
			return fields[0], email
		}
	}
	t.Fatal("hib connect github didn't print the connected login and commit email")
	return "", ""
}

func githubScript(t *testing.T, ctx context.Context, name, token, script string, args ...string) string {
	t.Helper()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	command := "sh -eu -c " + quote(script) + " sh"
	for _, arg := range args {
		command += " " + quote(arg)
	}
	stdout, stderr, err := sshResult(ctx, nil, name, command)
	if token != "" && strings.Contains(stdout+stderr, token) {
		t.Fatal("a GitHub command in the machine exposed the real token")
	}
	if err != nil {
		t.Fatalf("GitHub command in %s: %v\n%s", name, err, stderr)
	}
	return stdout
}

func newGitHubBranch(t *testing.T) string {
	t.Helper()
	branch := fmt.Sprintf("e2e-%016x", rand.Uint64())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := cleanGitHubBranch(ctx, branch); err != nil {
			t.Errorf("cleanup of GitHub branch %s: %v", branch, err)
		}
	})
	return branch
}

type githubPull struct {
	Number int `json:"number"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

func openGitHubPulls(ctx context.Context) ([]githubPull, error) {
	out, err := output(exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com",
		"repos/"+githubFixture+"/pulls?state=open&per_page=100", "--paginate", "--jq", ".[]"))
	if err != nil {
		return nil, fmt.Errorf("listing fixture PRs: %w", err)
	}
	var pulls []githubPull
	decoder := json.NewDecoder(strings.NewReader(out))
	for {
		var pull githubPull
		if err := decoder.Decode(&pull); err == io.EOF {
			return pulls, nil
		} else if err != nil {
			return nil, fmt.Errorf("reading fixture PRs: %w", err)
		}
		pulls = append(pulls, pull)
	}
}

func closeGitHubPull(ctx context.Context, number int) error {
	_, err := output(exec.CommandContext(ctx, "gh", "pr", "close", strconv.Itoa(number), "--repo", githubFixture))
	if err != nil {
		state, checkErr := output(exec.CommandContext(ctx, "gh", "pr", "view", strconv.Itoa(number),
			"--repo", githubFixture, "--json", "state", "--jq", ".state"))
		if checkErr == nil && (state == "CLOSED" || state == "MERGED") {
			return nil
		}
	}
	return err
}

func deleteGitHubBranch(ctx context.Context, branch string) error {
	_, err := output(exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com", "--method", "DELETE",
		"repos/"+githubFixture+"/git/refs/heads/"+branch))
	if err == nil {
		return nil
	}
	if branches, checkErr := gitHubBranches(ctx); checkErr == nil && !slices.Contains(branches, branch) {
		return nil
	}
	return fmt.Errorf("deleting fixture branch %s: %w", branch, err)
}

func gitHubBranches(ctx context.Context) ([]string, error) {
	out, err := output(exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com",
		"repos/"+githubFixture+"/branches?per_page=100", "--paginate", "--jq", ".[].name"))
	if err != nil {
		return nil, fmt.Errorf("listing fixture branches: %w", err)
	}
	return strings.Fields(out), nil
}

func cleanGitHubBranch(ctx context.Context, branch string) error {
	pulls, err := openGitHubPulls(ctx)
	if err != nil {
		return err
	}
	for _, pull := range pulls {
		if pull.Head.Ref == branch {
			if err := closeGitHubPull(ctx, pull.Number); err != nil {
				return fmt.Errorf("closing fixture PR %d: %w", pull.Number, err)
			}
		}
	}
	return deleteGitHubBranch(ctx, branch)
}

func deleteLeftoverGitHub(ctx context.Context) error {
	branches, err := gitHubBranches(ctx)
	if err != nil {
		return err
	}
	for _, branch := range branches {
		if !strings.HasPrefix(branch, "e2e-") {
			continue
		}
		fmt.Fprintf(os.Stderr, "e2e: deleting fixture branch %s and its PRs, left by an earlier run\n", branch)
		if err := cleanGitHubBranch(ctx, branch); err != nil {
			return err
		}
	}
	return nil
}
