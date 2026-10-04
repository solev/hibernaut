package machines_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/envblock"
	"github.com/solev/hibernaut/internal/machines"
)

const (
	testName  = "The Octocat"
	testEmail = "583231+octocat@users.noreply.github.com"
)

func testBlock(t *testing.T) []byte {
	t.Helper()
	block, err := envblock.Render([]envblock.Var{{Name: "GH_TOKEN", Value: "gho_hibplaceholder1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	return block
}

// shell runs the git identity script for real, with only bin on PATH. A git in bin plays git:
// it writes its arguments, each ending in a NUL byte, to the file log, and exits with code.
type shell struct {
	bin, log string
}

func newShell(t *testing.T, withGit bool, code int) shell {
	t.Helper()
	dir := t.TempDir()
	s := shell{bin: filepath.Join(dir, "bin"), log: filepath.Join(dir, "git.log")}
	if err := os.Mkdir(s.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if withGit {
		script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done >> \"$GIT_LOG\"\nprintf '\\n' >> \"$GIT_LOG\"\nexit " +
			strconv.Itoa(code) + "\n"
		if err := os.WriteFile(filepath.Join(s.bin, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s shell) run(ctx context.Context, req engine.ExecRequest) (engine.ExecResult, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", req.Args...)
	cmd.Env = []string{"PATH=" + s.bin, "GIT_LOG=" + s.log}
	cmd.Dir = filepath.Dir(s.bin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if _, ok := errors.AsType[*exec.ExitError](err); err != nil && !ok {
		return engine.ExecResult{}, err
	}
	return engine.ExecResult{ExitCode: cmd.ProcessState.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

// calls returns the argument lists git was called with.
func (s shell) calls(t *testing.T) [][]string {
	t.Helper()
	return readCalls(t, s.log)
}

func runningMachine(guest *envFileGuest) *fake.Engine {
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now()})
	return e
}

func TestSetupWriteWritesTheBlockThenGitsIdentity(t *testing.T) {
	sh := newShell(t, true, 0)
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n"), git: sh.run}
	e := runningMachine(guest)
	block := testBlock(t)
	s := machines.Setup{EnvBlock: block, Git: &machines.GitIdentity{Name: testName, Email: testEmail}}
	if err := s.Write(t.Context(), e, "app1"); err != nil {
		t.Fatal(err)
	}
	if want := "PATH=/usr/bin\n" + string(block); string(guest.content) != want {
		t.Errorf("/etc/environment = %q, want %q", guest.content, want)
	}
	want := [][]string{
		{"config", "--system", "--replace-all", "user.name", testName},
		{"config", "--system", "--replace-all", "user.email", testEmail},
	}
	if got := sh.calls(t); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("git calls = %q, want %q", got, want)
	}
	last := guest.requests[len(guest.requests)-1]
	if last.Cmd != "sh" || last.User != "root" || len(last.Args) != 5 || last.Args[0] != "-c" ||
		!slices.Equal(last.Args[2:], []string{"hibd-git", testName, testEmail}) {
		t.Errorf("last exec = %+v, want sh -c SCRIPT hibd-git NAME EMAIL as root", last)
	}
	if strings.Contains(last.Args[1], "Octocat") || strings.Contains(last.Args[1], "noreply") {
		t.Errorf("the script holds a value, not only positional arguments: %q", last.Args[1])
	}
}

func TestSetupWriteDoesntLetANameInjectAnything(t *testing.T) {
	sh := newShell(t, true, 0)
	guest := &envFileGuest{git: sh.run}
	e := runningMachine(guest)
	pwned := filepath.Join(filepath.Dir(sh.bin), "pwned")
	name := `$(touch ` + pwned + `) "; touch ` + pwned + `; " ` + "`touch " + pwned + "`" + ` 'x' \ -n`
	s := machines.Setup{Git: &machines.GitIdentity{Name: name, Email: "-e@x"}}
	if err := s.Write(t.Context(), e, "app1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("the name ran a command")
	}
	want := [][]string{
		{"config", "--system", "--replace-all", "user.name", name},
		{"config", "--system", "--replace-all", "user.email", "-e@x"},
	}
	if got := sh.calls(t); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("git calls = %q, want %q", got, want)
	}
}

func TestSetupWriteWithoutGitIsFine(t *testing.T) {
	sh := newShell(t, false, 0)
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n"), git: sh.run}
	e := runningMachine(guest)
	s := machines.Setup{EnvBlock: testBlock(t), Git: &machines.GitIdentity{Name: testName, Email: testEmail}}
	if err := s.Write(t.Context(), e, "app1"); err != nil {
		t.Fatalf("Write in a machine without git = %v, want nil", err)
	}
	if !bytes.Contains(guest.content, []byte("GH_TOKEN=")) {
		t.Errorf("/etc/environment = %q, want the block", guest.content)
	}
}

func TestSetupWriteReportsGitFailing(t *testing.T) {
	sh := newShell(t, true, 3)
	guest := &envFileGuest{git: sh.run}
	e := runningMachine(guest)
	s := machines.Setup{Git: &machines.GitIdentity{Name: testName, Email: testEmail}}
	err := s.Write(t.Context(), e, "app1")
	if err == nil || !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("Write = %v, want an error saying the script exited 3", err)
	}
	if n := len(sh.calls(t)); n != 1 {
		t.Errorf("git ran %d times, want once: the script stops at the first failure", n)
	}
}

func TestEmptySetupWritesNothing(t *testing.T) {
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n")}
	e := runningMachine(guest)
	if err := (machines.Setup{}).Write(t.Context(), e, "app1"); err != nil {
		t.Fatal(err)
	}
	for _, req := range guest.requests {
		if req.Cmd != "cat" {
			t.Errorf("an empty Setup ran %+v", req)
		}
	}
	if string(guest.content) != "PATH=/usr/bin\n" {
		t.Errorf("/etc/environment = %q", guest.content)
	}
}

func TestEmptySetupRemovesAStaleBlock(t *testing.T) {
	guest := &envFileGuest{content: append([]byte("PATH=/usr/bin\n"), testBlock(t)...)}
	e := runningMachine(guest)
	if err := (machines.Setup{}).Write(t.Context(), e, "app1"); err != nil {
		t.Fatal(err)
	}
	if string(guest.content) != "PATH=/usr/bin\n" {
		t.Errorf("/etc/environment = %q, want the block gone, as after removing the last secret", guest.content)
	}
}

func TestSetupHash(t *testing.T) {
	block := testBlock(t)
	setups := map[string]machines.Setup{
		"empty":               {},
		"block":               {EnvBlock: block},
		"empty identity":      {Git: &machines.GitIdentity{}},
		"identity":            {Git: &machines.GitIdentity{Name: "ab", Email: "c"}},
		"identity shifted":    {Git: &machines.GitIdentity{Name: "a", Email: "bc"}},
		"other name":          {Git: &machines.GitIdentity{Name: "ab ", Email: "c"}},
		"block and identity":  {EnvBlock: block, Git: &machines.GitIdentity{Name: "ab", Email: "c"}},
		"name in the block":   {EnvBlock: []byte("ab"), Git: &machines.GitIdentity{Email: "c"}},
		"block and empty git": {EnvBlock: []byte("ab"), Git: &machines.GitIdentity{}},
		"codex":               {CodexAuth: []byte("ab")},
		"other codex":         {CodexAuth: []byte("ab ")},
		"codex and git":       {CodexAuth: []byte("ab"), Git: &machines.GitIdentity{}},
		"block and codex":     {EnvBlock: block, CodexAuth: []byte("ab")},
		"codex in the email":  {Git: &machines.GitIdentity{Email: "ab"}},
	}
	seen := map[[32]byte]string{}
	for name, s := range setups {
		h := s.Hash()
		if other, ok := seen[h]; ok {
			t.Errorf("%s and %s hash the same", name, other)
		}
		seen[h] = name
	}
	again := machines.Setup{EnvBlock: bytes.Clone(block), Git: &machines.GitIdentity{Name: "ab", Email: "c"}}
	if again.Hash() != setups["block and identity"].Hash() {
		t.Error("equal setups hash differently")
	}
	if (machines.Setup{EnvBlock: []byte{}}).Hash() != (machines.Setup{}).Hash() {
		t.Error("an empty block and no block hash differently, but both write the same")
	}
	if (machines.Setup{CodexAuth: []byte{}}).Hash() != (machines.Setup{}).Hash() {
		t.Error("an empty login file and none hash differently, but both leave the file alone")
	}
}

func TestCreateWritesTheSetup(t *testing.T) {
	v := newEnv(t)
	sh := newShell(t, true, 0)
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n"), git: sh.run}
	v.e = fake.New(fake.WithTemplates("hib-dev"), fake.WithExec(func(ctx context.Context, m engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
		if req.Cmd == images.MachineInit {
			return v.g.exec(ctx, m, home, req)
		}
		return guest.exec(ctx, m, home, req)
	}))
	block := testBlock(t)
	m := machines.New(machines.Config{Engine: v.e, StateDir: v.dir, AuthorizedKeys: v.keys, ReadRoom: roomy,
		Setup: machines.Setup{EnvBlock: block, Git: &machines.GitIdentity{Name: testName, Email: testEmail}}})
	if _, err := m.Create(t.Context(), "app", "dev", machines.DefaultIdle); err != nil {
		t.Fatal(err)
	}
	if want := "PATH=/usr/bin\n" + string(block); string(guest.content) != want {
		t.Errorf("/etc/environment = %q, want %q", guest.content, want)
	}
	if n := len(sh.calls(t)); n != 2 {
		t.Errorf("git ran %d times, want 2", n)
	}
}

func TestCreateKillsAMachineWhoseSetupFails(t *testing.T) {
	v := newEnv(t)
	sh := newShell(t, true, 1)
	guest := &envFileGuest{git: sh.run}
	v.e = fake.New(fake.WithTemplates("hib-dev"), fake.WithExec(func(ctx context.Context, m engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
		if req.Cmd == images.MachineInit {
			return v.g.exec(ctx, m, home, req)
		}
		return guest.exec(ctx, m, home, req)
	}))
	m := machines.New(machines.Config{Engine: v.e, StateDir: v.dir, AuthorizedKeys: v.keys, ReadRoom: roomy,
		Setup: machines.Setup{Git: &machines.GitIdentity{Name: testName, Email: testEmail}}})
	_, err := m.Create(t.Context(), "app", "dev", machines.DefaultIdle)
	if err == nil || !strings.Contains(err.Error(), "exited 1") {
		t.Fatalf("Create = %v, want the setup's error", err)
	}
	if ms := v.machines(t); len(ms) != 0 {
		t.Errorf("machines = %+v, want the failed one killed", ms)
	}
}

// codexHome runs the script that writes Codex's login file for real, in a temporary directory
// that plays /home/user. A chown in bin logs its arguments, each ending in a NUL byte, to the
// file log, and fails for paths matching failOn.
type codexHome struct {
	root, bin, log string
}

func newCodexHome(t *testing.T, failOn string) codexHome {
	t.Helper()
	dir := t.TempDir()
	h := codexHome{root: filepath.Join(dir, "home", "user"), bin: filepath.Join(dir, "bin"), log: filepath.Join(dir, "chown.log")}
	for _, d := range []string{h.root, h.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done >> \"$CHOWN_LOG\"\nprintf '\\n' >> \"$CHOWN_LOG\"\n" +
		"case \"$2\" in " + failOn + ") exit 1;; esac\n"
	if failOn == "" {
		script = strings.Replace(script, "case \"$2\" in ) exit 1;; esac\n", "", 1)
	}
	if err := os.WriteFile(filepath.Join(h.bin, "chown"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h codexHome) dir() string { return filepath.Join(h.root, ".codex") }

func (h codexHome) run(ctx context.Context, req engine.ExecRequest) (engine.ExecResult, error) {
	if req.Args[3] != "/home/user/.codex" {
		return engine.ExecResult{ExitCode: 64, Stderr: []byte("unexpected directory " + req.Args[3])}, nil
	}
	args := slices.Clone(req.Args)
	args[3] = h.dir()
	cmd := exec.CommandContext(ctx, "/bin/sh", args...)
	cmd.Env = []string{"PATH=" + h.bin + ":/usr/bin:/bin", "CHOWN_LOG=" + h.log}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if _, ok := errors.AsType[*exec.ExitError](err); err != nil && !ok {
		return engine.ExecResult{}, err
	}
	return engine.ExecResult{ExitCode: cmd.ProcessState.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

func (h codexHome) chowns(t *testing.T) [][]string {
	t.Helper()
	return readCalls(t, h.log)
}

func readCalls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
	}
	return out
}

func codexRequests(guest *envFileGuest) []engine.ExecRequest {
	var out []engine.ExecRequest
	for _, req := range guest.requests {
		if len(req.Args) > 2 && req.Args[2] == "hibd-codex" {
			out = append(out, req)
		}
	}
	return out
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %v, want %v", filepath.Base(path), got, want)
	}
}

func TestSetupWriteWritesCodexLoginFile(t *testing.T) {
	home := newCodexHome(t, "")
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n"), codex: home.run}
	e := runningMachine(guest)
	auth := []byte("{\n  \"tokens\": {\"access_token\": \"$(touch pwned) 'x'\"}\n}")
	s := machines.Setup{EnvBlock: testBlock(t), CodexAuth: auth}
	if err := s.Write(t.Context(), e, "app1"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(home.dir(), "auth.json")); err != nil || !bytes.Equal(got, auth) {
		t.Fatalf("auth.json = %q, %v; want %q", got, err, auth)
	}
	assertMode(t, home.dir(), 0o700)
	assertMode(t, filepath.Join(home.dir(), "auth.json"), 0o600)
	if entries, _ := os.ReadDir(home.dir()); len(entries) != 1 {
		t.Errorf("~/.codex holds %v, want only auth.json", entries)
	}
	chowns := home.chowns(t)
	if len(chowns) != 2 || !slices.Equal(chowns[0], []string{"user:user", home.dir()}) || chowns[1][0] != "user:user" ||
		filepath.Dir(chowns[1][1]) != home.dir() || filepath.Base(chowns[1][1]) == "auth.json" {
		t.Errorf("chown calls = %q, want the directory, then a temporary file beside auth.json", chowns)
	}
	reqs := codexRequests(guest)
	if len(reqs) != 1 || reqs[0].Cmd != "sh" || reqs[0].User != "root" || len(reqs[0].Args) != 5 || reqs[0].Args[0] != "-c" {
		t.Fatalf("codex execs = %+v, want one sh -c SCRIPT hibd-codex DIR DATA as root", reqs)
	}
	if strings.Contains(reqs[0].Args[1], "tokens") {
		t.Errorf("the script holds the file, not only positional arguments: %q", reqs[0].Args[1])
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(home.bin), "pwned")); err == nil {
		t.Error("the file's content ran a command")
	}

	next := []byte(`{"tokens": {}}`)
	if err := (machines.Setup{CodexAuth: next}).Write(t.Context(), e, "app1"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(home.dir(), "auth.json")); err != nil || !bytes.Equal(got, next) {
		t.Errorf("auth.json after a second write = %q, %v; want %q", got, err, next)
	}
}

func TestSetupWriteWithoutCodexLeavesTheFileAlone(t *testing.T) {
	home := newCodexHome(t, "")
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n"), codex: home.run}
	e := runningMachine(guest)
	for _, s := range []machines.Setup{{EnvBlock: testBlock(t)}, {}, {CodexAuth: []byte{}}} {
		if err := s.Write(t.Context(), e, "app1"); err != nil {
			t.Fatal(err)
		}
	}
	if reqs := codexRequests(guest); len(reqs) != 0 {
		t.Errorf("codex execs = %+v, want none", reqs)
	}
}

func TestSetupWriteReportsCodexFailingAndLeavesNoTemporaryFile(t *testing.T) {
	home := newCodexHome(t, "*/.auth.json.*")
	guest := &envFileGuest{codex: home.run}
	e := runningMachine(guest)
	err := (machines.Setup{CodexAuth: []byte(`{"tokens": {}}`)}).Write(t.Context(), e, "app1")
	if err == nil || !strings.Contains(err.Error(), "exited 1") || !strings.Contains(err.Error(), "Codex") {
		t.Fatalf("Write = %v, want an error saying writing Codex's login file exited 1", err)
	}
	if entries, _ := os.ReadDir(home.dir()); len(entries) != 0 {
		t.Errorf("~/.codex holds %v after a failure, want nothing", entries)
	}
}

func TestCreateWritesCodexLoginFile(t *testing.T) {
	v := newEnv(t)
	home := newCodexHome(t, "")
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n"), codex: home.run}
	v.e = fake.New(fake.WithTemplates("hib-dev"), fake.WithExec(func(ctx context.Context, m engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
		if req.Cmd == images.MachineInit {
			return v.g.exec(ctx, m, home, req)
		}
		return guest.exec(ctx, m, home, req)
	}))
	auth := []byte(`{"tokens": {}}`)
	m := machines.New(machines.Config{Engine: v.e, StateDir: v.dir, AuthorizedKeys: v.keys, ReadRoom: roomy,
		Setup: machines.Setup{CodexAuth: auth}})
	if _, err := m.Create(t.Context(), "app", "dev", machines.DefaultIdle); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(home.dir(), "auth.json")); err != nil || !bytes.Equal(got, auth) {
		t.Errorf("auth.json = %q, %v; want %q", got, err, auth)
	}
}
