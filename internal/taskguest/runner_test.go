package taskguest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

func TestPublicationErrorBound(t *testing.T) {
	value := strings.Repeat("\x00", 8<<20)
	for _, prefix := range []string{"", "unknown", "changes=", "commit=", "start="} {
		t.Run(prefix, func(t *testing.T) {
			_, err := ParsePublication([]byte(prefix + value))
			if err == nil {
				t.Fatal("accepted an invalid publication")
			}
			if n := len(err.Error()); n > 1250 {
				t.Fatalf("publication error has %d bytes; want at most 200 quoted input bytes", n)
			}
		})
	}
}

type repoRunner struct {
	t                             *testing.T
	root, remote, seed, work, run string
	script, system, bin           string
	env                           []string
}

func newRepoRunner(t *testing.T, empty bool) *repoRunner {
	t.Helper()
	root := t.TempDir()
	f := &repoRunner{t: t, root: root, remote: filepath.Join(root, "remote.git"),
		seed: filepath.Join(root, "seed"), work: filepath.Join(root, "work"),
		run: filepath.Join(root, "r1"), script: filepath.Join(root, "runner"),
		system: filepath.Join(root, "system"), bin: filepath.Join(root, "bin")}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "HIB_") &&
			!strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "GH_TOKEN=") {
			f.env = append(f.env, entry)
		}
	}
	f.env = append(f.env, "HOME="+root, "GIT_CONFIG_GLOBAL="+filepath.Join(root, "global"),
		"GIT_CONFIG_SYSTEM="+f.system, "GIT_ALLOW_PROTOCOL=file", "GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false", "HIB_REPO=o/r", "HIB_BASE=", "HIB_BRANCH=hib/t-test",
		"HIB_WORKDIR="+f.work, "HIB_PUBLISH=draft-pr", "WAITS="+filepath.Join(root, "waits"),
		"PATH="+f.bin+":"+os.Getenv("PATH"))
	for _, dir := range []string{f.seed, f.run, f.bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, f.system, "")
	writeTestFile(t, filepath.Join(root, "global"), "")
	writeTestFile(t, f.script, string(Runner))
	writeTestFile(t, filepath.Join(f.bin, "sleep"), "#!/bin/sh\nprintf '%s\\n' \"$1\" >>\"$WAITS\"\n")
	f.git(root, "config", "--file", f.system, "user.name", "Task User")
	f.git(root, "config", "--file", f.system, "user.email", "task@users.noreply.github.com")
	f.git(root, "config", "--file", f.system, "url."+f.remote+".insteadOf", "https://github.com/o/r")
	f.git(root, "init", "--bare", "-q", f.remote)
	f.git(f.remote, "symbolic-ref", "HEAD", "refs/heads/trunk")
	if !empty {
		f.git(f.seed, "init", "-q")
		f.git(f.seed, "symbolic-ref", "HEAD", "refs/heads/trunk")
		writeTestFile(t, filepath.Join(f.seed, "tracked"), "before\n")
		f.git(f.seed, "add", ".")
		f.git(f.seed, "commit", "-qm", "Initial")
		f.git(f.seed, "push", "-q", f.remote, "HEAD:refs/heads/trunk")
	}
	return f
}

func (f *repoRunner) exec(input string, extra []string, argv ...string) (string, int) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdin = f.root, append(f.env[:len(f.env):len(f.env)], extra...), strings.NewReader(input)
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		f.t.Fatalf("%s exceeded its deadline", argv[0])
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode()
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return out.String(), 0
}

func (f *repoRunner) git(dir string, args ...string) string {
	f.t.Helper()
	out, code := f.exec("", nil, append([]string{"git", "-C", dir}, args...)...)
	if code != 0 {
		f.t.Fatalf("git %v exited %d: %s", args, code, out)
	}
	return strings.TrimSpace(out)
}

func (f *repoRunner) stage(stage string, command ...string) (string, int) {
	f.t.Helper()
	return f.exec("", nil, append([]string{"sh", f.script, "start", f.run, stage, "--"}, command...)...)
}

func (f *repoRunner) okStage(stage string, command ...string) string {
	f.t.Helper()
	out, code := f.stage(stage, command...)
	if code != 0 {
		f.t.Fatalf("%s exited %d: %s", stage, code, out)
	}
	return out
}

func TestRepoSetupBase(t *testing.T) {
	for _, base := range []string{"", "topic"} {
		t.Run(base, func(t *testing.T) {
			f := newRepoRunner(t, false)
			ref := "trunk"
			if base != "" {
				ref = base
				f.git(f.seed, "checkout", "-qb", base)
				f.git(f.seed, "commit", "--allow-empty", "-qm", "Topic")
				f.git(f.seed, "push", "-q", f.remote, "HEAD:refs/heads/"+base)
			}
			f.env = append(f.env, "HIB_BASE="+base)
			want := f.git(f.remote, "rev-parse", ref)
			out := f.okStage("setup")
			if got := f.git(f.work, "symbolic-ref", "--short", "HEAD"); got != "hib/t-test" {
				t.Fatalf("checked out %q, want hib/t-test", got)
			}
			if got := f.git(f.work, "rev-parse", "HEAD"); got != want || !strings.Contains(out, want) {
				t.Fatalf("clone HEAD %s, want remote %s; log %q", got, want, out)
			}
		})
	}
}

func TestRepoSetupExistingBranch(t *testing.T) {
	f := newRepoRunner(t, false)
	f.git(f.remote, "update-ref", "refs/heads/hib/t-test", f.git(f.remote, "rev-parse", "trunk"))
	out, code := f.stage("setup")
	if code == 0 || !strings.Contains(out, "hib/t-test") {
		t.Fatalf("existing branch setup = %d %q; want refusal naming hib/t-test", code, out)
	}
	if _, err := os.Stat(f.work); !os.IsNotExist(err) {
		t.Fatal("refused setup created a workdir")
	}
	if _, code := f.stage("setup"); code != ExitTryAgain {
		t.Fatalf("second setup exit %d, want %d", code, ExitTryAgain)
	}
}

func TestRepoSetupInvalidBase(t *testing.T) {
	for _, base := range []string{"missing", "-option", "bad..ref"} {
		t.Run(base, func(t *testing.T) {
			f := newRepoRunner(t, false)
			f.env = append(f.env, "HIB_BASE="+base)
			out, code := f.stage("setup")
			if code == 0 || !strings.Contains(out, base) {
				t.Fatalf("invalid base setup = %d %q; want refusal naming %q", code, out, base)
			}
			if base == "missing" {
				if _, err := os.Stat(filepath.Join(f.work, ".git")); err != nil {
					t.Fatal("failed setup removed its clone")
				}
				if _, code := f.stage("setup"); code != ExitTryAgain {
					t.Fatal("failed setup ran twice")
				}
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		f := newRepoRunner(t, true)
		out, code := f.stage("setup")
		if code == 0 || !strings.Contains(out, "empty") {
			t.Fatalf("empty repository setup = %d %q; want an empty repository error", code, out)
		}
	})
	t.Run("lookup failure", func(t *testing.T) {
		f := newRepoRunner(t, false)
		f.git(f.root, "config", "--file", f.system, "url."+f.remote+".insteadOf", "https://github.com/other")
		out, code := f.stage("setup")
		if code == 0 || !strings.Contains(out, "check remote branch") {
			t.Fatalf("failed remote lookup = %d %q", code, out)
		}
	})
}

func TestRepoCredentials(t *testing.T) {
	f := newRepoRunner(t, false)
	const token = "gho_fake_task_token_0123456789"
	f.env = append(f.env, "GH_TOKEN="+token)
	f.git(f.root, "config", "--file", f.system, "credential.helper",
		"!printf '%s\\n' username=earlier password=earlier")
	log := f.okStage("setup")
	for _, url := range []string{"https://github.com/o/r", "https://example.com/o/r",
		"http://github.com/o/r", "https://github.com.evil.com/o/r", "https://api.github.com/x"} {
		out, code := f.exec("url="+url+"\n\n", nil, "git", "-C", f.work, "credential", "fill")
		want := strings.HasPrefix(url, "https://github.com/")
		if strings.Contains(out, token) != want || (want && (code != 0 || !strings.Contains(out, "username=x-access-token"))) {
			t.Fatalf("credential for %s: exit %d, token present %t, want %t", url, code, strings.Contains(out, token), want)
		}
	}
	out, code := f.exec("url=https://github.com/o/r\n\n", []string{"GH_TOKEN="},
		"git", "-C", f.work, "credential", "fill")
	if code == 0 || strings.Contains(out, token) {
		t.Fatal("empty token supplied a credential")
	}
	log += f.okStage("command", "true")
	log += f.okStage("finish")
	for _, path := range []string{filepath.Join(f.work, ".git", "config"), filepath.Join(f.run, "publish")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		log += string(data)
	}
	if strings.Contains(log, token) || bytes.Contains(Runner, []byte(token)) {
		t.Fatal("token escaped into stage output, config, publication or runner")
	}
}

func TestRepoCommandStart(t *testing.T) {
	f := newRepoRunner(t, false)
	f.okStage("setup")
	want := f.git(f.remote, "rev-parse", "trunk")
	out, code := f.exec("prompt input\n", nil, "sh", f.script, "start", f.run, "command", "--",
		"sh", "-c", `cat; git -C "$HIB_WORKDIR" commit --allow-empty -qm agent; exit 7`)
	if code != 7 || out != "prompt input\n" {
		t.Fatalf("command changed stdin, output or exit: %d %q", code, out)
	}
	data, err := os.ReadFile(filepath.Join(f.run, "start"))
	if err != nil || strings.TrimSpace(string(data)) != want {
		t.Fatalf("command start = %q, %v; want pre-command HEAD %s", data, err, want)
	}
	f = newRepoRunner(t, false)
	f.env = append(f.env, "HIB_REPO=")
	if out, code := f.stage("command", "sh", "-c", "echo plain; exit 9"); code != 9 || out != "plain\n" {
		t.Fatalf("no-repo command = %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(f.run, "start")); !os.IsNotExist(err) {
		t.Fatal("no-repo command wrote a start file")
	}
}

func TestRepoGitRequirement(t *testing.T) {
	for _, old := range []bool{false, true} {
		f := newRepoRunner(t, false)
		for _, name := range []string{"mkdir", "mv"} {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path, filepath.Join(f.bin, name)); err != nil {
				t.Fatal(err)
			}
		}
		if old {
			writeTestFile(t, filepath.Join(f.bin, "git"), "#!/bin/sh\necho 'git version 2.22.0'\n")
		}
		for _, stage := range []string{"setup", "finish"} {
			out, code := f.exec("", []string{"PATH=" + f.bin}, "sh", f.script, "start", f.run, stage)
			if code == 0 || !strings.Contains(out, "Git 2.23") {
				t.Fatalf("missing/old git %s = %d %q; want required version", stage, code, out)
			}
		}
	}
}

func (f *repoRunner) publication() task.Publication {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.run, "publish"))
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := ParsePublication(data)
	if err != nil {
		f.t.Fatalf("invalid publication: %v", err)
	}
	return p
}

func (f *repoRunner) remoteRef() string {
	f.t.Helper()
	out, code := f.exec("", nil, "git", "-C", f.remote, "show-ref", "--verify", "--hash", "refs/heads/hib/t-test")
	if code == 1 || code == 128 {
		return ""
	}
	if code != 0 {
		f.t.Fatalf("remote ref lookup exited %d: %s", code, out)
	}
	return strings.TrimSpace(out)
}

func TestRepoPublicationEnds(t *testing.T) {
	for _, end := range []string{"no changes", "dirty", "agent commit", "refused push", "none", "no identity"} {
		t.Run(end, func(t *testing.T) {
			f := newRepoRunner(t, false)
			f.okStage("setup")
			f.okStage("command", "true")
			start := f.git(f.remote, "rev-parse", "trunk")
			changed, pushed, failed := end != "no changes", false, false
			switch end {
			case "no changes":
			case "agent commit":
				f.git(f.work, "commit", "--allow-empty", "-qm", "Agent work")
				pushed = true
			default:
				writeTestFile(t, filepath.Join(f.work, "tracked"), "after\n")
				writeTestFile(t, filepath.Join(f.work, ".gitignore"), "ignored\n")
				writeTestFile(t, filepath.Join(f.work, "ignored"), "leave this out\n")
				switch end {
				case "dirty":
					pushed = true
					hooks := filepath.Join(f.root, "agent-hooks")
					writeTestFile(t, filepath.Join(hooks, "prepare-commit-msg"), "#!/bin/sh\necho noisy-hook; exit 1\n")
					f.git(f.work, "config", "core.hooksPath", hooks)
					f.git(f.work, "config", "commit.gpgsign", "true")
				case "refused push":
					failed = true
					writeTestFile(t, filepath.Join(f.remote, "hooks", "pre-receive"),
						"#!/bin/sh\necho try >>'"+filepath.Join(f.root, "attempts")+"'\necho noisy-refusal\nexit 1\n")
				case "none":
					f.env = append(f.env, "HIB_PUBLISH=none")
				case "no identity":
					failed = true
					f.git(f.root, "config", "--file", f.system, "--unset", "user.name")
					f.git(f.root, "config", "--file", f.system, "--unset", "user.email")
				}
			}
			out, code := f.stage("finish")
			if (code != 0) != failed || code == ExitTryAgain || code == ExitFrozen {
				t.Fatalf("finish %q exited %d: %s", end, code, out)
			}
			if strings.Contains(out, "noisy-") || len(out) > 2048 {
				t.Fatalf("unbounded Git or hook output: %q", out)
			}
			p := f.publication()
			wantCommit := ""
			if changed && end != "no identity" {
				wantCommit = f.git(f.work, "rev-parse", "HEAD")
			}
			want := task.Publication{Start: start, Changes: changed, Commit: wantCommit,
				Pushed: pushed, Author: "Task User <task@users.noreply.github.com>"}
			if p != want {
				t.Fatalf("publication = %+v, want %+v", p, want)
			}
			ref := f.remoteRef()
			if (ref != "") != pushed || (pushed && ref != p.Commit) {
				t.Fatalf("remote ref %q disagrees with publication %+v", ref, p)
			}
			verified, err := p.AtRef(ref)
			if wantOK := !changed || pushed; (err == nil) != wantOK || (wantOK && verified != want) {
				t.Fatalf("AtRef(%q) = %+v, %v; publication %+v", ref, verified, err, p)
			}
			if end == "dirty" {
				if got := f.git(f.remote, "show", ref+":tracked"); got != "after" {
					t.Fatalf("pushed file = %q", got)
				}
				if out, code := f.exec("", nil, "git", "-C", f.remote, "show", ref+":ignored"); code == 0 {
					t.Fatalf("committed ignored content: %s", out)
				}
				if got := f.git(f.remote, "show", "-s", "--format=%an <%ae>", ref); got != want.Author {
					t.Fatalf("commit author = %q, want %q", got, want.Author)
				}
			}
			if end == "refused push" {
				for file, want := range map[string]string{"attempts": "try\ntry\ntry\n", "waits": "5\n15\n"} {
					data, err := os.ReadFile(filepath.Join(f.root, file))
					if err != nil || string(data) != want {
						t.Errorf("%s = %q, %v; want %q", file, data, err, want)
					}
				}
			}
			if end == "no identity" && !strings.Contains(out, "identity") {
				t.Fatalf("missing identity error = %q", out)
			}
		})
	}
}

func TestRepoMovedBranch(t *testing.T) {
	f := newRepoRunner(t, false)
	f.okStage("setup")
	f.okStage("command", "true")
	writeTestFile(t, filepath.Join(f.work, "tracked"), "agent work\n")
	f.git(f.seed, "commit", "--allow-empty", "-qm", "Remote work")
	f.git(f.seed, "push", "-q", f.remote, "HEAD:refs/heads/hib/t-test")
	want := f.remoteRef()
	out, code := f.stage("finish")
	if code == 0 || f.remoteRef() != want {
		t.Fatalf("finish overwrote a moved remote ref, exit %d: %s", code, out)
	}
	if p := f.publication(); !p.Changes || p.Pushed || p.Commit == "" {
		t.Fatalf("refused push publication = %+v", p)
	}
}

func TestRepoFollowUp(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "commit"}[change], func(t *testing.T) {
			f := newRepoRunner(t, false)
			f.okStage("setup")
			f.okStage("command", "true")
			writeTestFile(t, filepath.Join(f.work, "tracked"), "first\n")
			f.okStage("finish")
			start := f.remoteRef()
			f.run = filepath.Join(f.root, "r2")
			if err := os.Mkdir(f.run, 0o755); err != nil {
				t.Fatal(err)
			}
			f.okStage("command", "true")
			if change {
				f.git(f.work, "commit", "--allow-empty", "-qm", "Follow-up")
			}
			f.okStage("finish")
			p := f.publication()
			if p.Start != start || p.Changes != change || p.Pushed != change {
				t.Fatalf("follow-up publication = %+v, first push %s", p, start)
			}
			if _, err := p.AtRef(f.remoteRef()); err != nil {
				t.Fatal(err)
			}
			if got := f.git(f.remote, "merge-base", start, f.remoteRef()); got != start {
				t.Fatal("follow-up did not fast-forward the task branch")
			}
		})
	}
}

func TestRepoFinishGuards(t *testing.T) {
	for _, kind := range []string{"missing start", "empty policy", "unknown policy", "detached", "other branch", "clone identity"} {
		t.Run(kind, func(t *testing.T) {
			f := newRepoRunner(t, false)
			f.okStage("setup")
			if kind != "missing start" {
				f.okStage("command", "true")
			}
			failed := true
			switch kind {
			case "empty policy":
				f.env = append(f.env, "HIB_PUBLISH=")
			case "unknown policy":
				f.env = append(f.env, "HIB_PUBLISH=surprise")
			case "detached":
				f.git(f.work, "checkout", "--detach", "-q")
				failed = false
			case "other branch":
				f.git(f.work, "checkout", "-qb", "agent")
				failed = false
			case "clone identity":
				f.git(f.work, "config", "user.name", "Agent Choice")
				failed = false
			}
			writeTestFile(t, filepath.Join(f.work, "tracked"), "after\n")
			out, code := f.stage("finish")
			if (code != 0) != failed {
				t.Fatalf("finish %s exited %d: %s", kind, code, out)
			}
			p := f.publication()
			if kind == "missing start" && p.Start != "" {
				t.Fatal("invented a starting commit")
			}
			if !failed && (!p.Pushed || p.Commit != f.remoteRef()) {
				t.Fatalf("did not push HEAD: %+v", p)
			}
			if kind == "clone identity" && !strings.HasPrefix(p.Author, "Agent Choice <") {
				t.Fatalf("clone identity lost: %q", p.Author)
			}
		})
	}
}
