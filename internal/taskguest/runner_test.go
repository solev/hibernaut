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
	for _, path := range []string{filepath.Join(f.work, ".git", "config")} {
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
		out, code := f.exec("", []string{"PATH=" + f.bin}, "sh", f.script, "start", f.run, "setup")
		if code == 0 || !strings.Contains(out, "Git 2.23") {
			t.Fatalf("missing/old git setup = %d %q; want required version", code, out)
		}
	}
}
