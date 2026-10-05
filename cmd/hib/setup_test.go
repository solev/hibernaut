package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/version"
)

func TestInit(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	if err := os.MkdirAll(filepath.Dir(c.sshConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	mine := "Host work\n  User me\n"
	if err := os.WriteFile(c.sshConfig, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(identity, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := c.hib("init", "-host", testHost, "-identity", identity, "-azure-group", testGroup, "-azure-vm", testVM); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if want := testHost + " runs hibd " + version.Version + "\n"; !strings.HasPrefix(c.out.String(), want) ||
		!strings.Contains(c.out.String(), "added hib's settings to "+c.sshConfig) {
		t.Errorf("printed %q", c.out)
	}

	calls := h.takeCalls()
	if len(calls) != 2 || !slices.Equal(calls[0], []string{"-V"}) || slices.Contains(calls[1], "BatchMode=yes") {
		t.Errorf("init ran ssh %q, want ssh -V and then hibd version without BatchMode", calls)
	}

	for path, want := range map[string]os.FileMode{
		c.dir:                               0o700 | os.ModeDir,
		filepath.Join(c.dir, "config.json"): 0o600,
		filepath.Join(c.dir, "ssh_config"):  0o600,
		filepath.Join(c.dir, "known_hosts"): 0o700 | os.ModeDir,
		c.sshConfig:                         0o644,
	} {
		if fi, err := os.Stat(path); err != nil || fi.Mode() != want {
			t.Errorf("%s: mode %v, %v; want %v", path, fi.Mode(), err, want)
		}
	}

	cfg, err := loadConfig(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != testHost || cfg.Identity != identity || cfg.Azure == nil || cfg.Azure.Group != testGroup ||
		cfg.Azure.VM != testVM || cfg.SSHConfig != "" || cfg.prompt {
		t.Errorf("saved %+v", cfg)
	}

	sshConfig, _ := os.ReadFile(filepath.Join(c.dir, "ssh_config"))
	for _, want := range []string{
		"Host *.hib\n",
		"  ProxyCommand '" + testHib + "' -config '" + c.dir + "' bridge '%h'\n",
		"  User user\n",
		"  UserKnownHostsFile \"" + c.dir + "/known_hosts/%h\"\n",
		"  GlobalKnownHostsFile /dev/null\n",
		"  StrictHostKeyChecking yes\n",
		"  UpdateHostKeys no\n",
		"  ConnectTimeout 600\n",
		"  ForwardAgent no\n",
		"  ForwardX11 no\n",
		"  IdentityFile \"" + identity + "\"\n",
		"  IdentitiesOnly yes\n",
	} {
		if !strings.Contains(string(sshConfig), want) {
			t.Errorf("ssh_config lacks %q:\n%s", want, sshConfig)
		}
	}

	include := "Include \"" + filepath.Join(c.dir, "ssh_config") + "\"\n"
	data, _ := os.ReadFile(c.sshConfig)
	if !strings.HasPrefix(string(data), "# Added by hib init") || !strings.Contains(string(data), "\n"+include+"\n"+mine) {
		t.Errorf("~/.ssh/config =\n%s", data)
	}

	if code := c.hib("init", "-host", testHost, "-identity", identity); code != 0 {
		t.Fatalf("second init exit %d: %s", code, c.errOut)
	}
	again, _ := os.ReadFile(c.sshConfig)
	if !bytes.Equal(again, data) || strings.Contains(c.out.String(), "added") {
		t.Errorf("second init changed ~/.ssh/config:\n%s", again)
	}
	if cfg, err := loadConfig(c.dir); err != nil || cfg.Azure != nil {
		t.Errorf("second init should replace the settings: %+v, %v", cfg, err)
	}
}

func TestInitVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		host    string
		warning bool
	}{
		{"matching", version.Version, false},
		{"different", "older-host-build", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
				if !slices.Equal(words, []string{"version", "--json"}) {
					return 0, false
				}
				if err := json.NewEncoder(stdout).Encode(hibapi.Response{V: hibapi.Version, Version: tc.host}); err != nil {
					t.Fatal(err)
				}
				return 0, true
			}
			c := newClient(t, h)
			if code := c.hib("version"); code != 0 || c.out.String() != "hib "+version.Version+"\n" {
				t.Fatalf("version: exit %d, stdout %q", code, c.out)
			}
			if code := c.hib("init", "-host", testHost); code != 0 {
				t.Fatalf("init exit %d: %s", code, c.errOut)
			}
			if want := testHost + " runs hibd " + tc.host + "\n"; !strings.HasPrefix(c.out.String(), want) {
				t.Errorf("init printed %q, want the host's version %q", c.out, want)
			}
			if warned := strings.Contains(c.out.String(), "note:"); warned != tc.warning {
				t.Errorf("init warned %v, want %v: %s", warned, tc.warning, c.out)
			}
			if tc.warning && !strings.Contains(c.out.String(), "note: this is hib "+version.Version+". Deploy the same release to the host\n") {
				t.Errorf("init didn't say which release to deploy: %s", c.out)
			}
		})
	}
}

func TestInitRefusesOldOpenSSH(t *testing.T) {
	for version, ok := range map[string]bool{
		"OpenSSH_9.5p1, OpenSSL 3.0.2":    false,
		"OpenSSH_8.9p1 Ubuntu-3ubuntu0.1": false,
		"OpenSSH_9.6p1, LibreSSL 3.3.6":   true,
		"OpenSSH_10.0p2":                  true,
		"dropbear v2022.83":               false,
	} {
		h := newHost(t)
		h.sshVersion = version
		c := newClient(t, h)
		code := c.hib("init", "-host", testHost)
		if ok != (code == 0) {
			t.Errorf("%s: exit %d: %s", version, code, c.errOut)
		}
		_, err := os.Stat(c.dir)
		if !ok && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: init saved settings anyway (%v)", version, err)
		}
		if !ok && version[0] == 'O' && !strings.Contains(c.errOut.String(), "CVE-2023-51385") {
			t.Errorf("%s: stderr %q, want the reason", version, c.errOut)
		}
	}
}

func TestInitRejects(t *testing.T) {
	h := newHost(t)
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"no host", []string{"init"}, 2},
		{"host with a space", []string{"init", "-host", "a b"}, 2},
		{"host that's an option", []string{"init", "-host", "-oProxyCommand=sh"}, 2},
		{"host with a semicolon", []string{"init", "-host", "u@h;id"}, 2},
		{"host with a quote", []string{"init", "-host", "u@h'"}, 2},
		{"group without VM", []string{"init", "-host", testHost, "-azure-group", "g"}, 2},
		{"VM without group", []string{"init", "-host", testHost, "-azure-vm", "v"}, 2},
		{"bad group", []string{"init", "-host", testHost, "-azure-group", "g;id", "-azure-vm", "v"}, 2},
		{"missing identity", []string{"init", "-host", testHost, "-identity", "/nonexistent/id"}, 2},
		{"ssh config with a quote", []string{"init", "-host", testHost, "-ssh-config", "/tmp/it's"}, 2},
		{"ssh config with a percent", []string{"init", "-host", testHost, "-ssh-config", "/tmp/%d"}, 2},
		{"extra argument", []string{"init", "-host", testHost, "extra"}, 2},
	} {
		c := newClient(t, h)
		if code := c.hib(tc.args...); code != tc.code {
			t.Errorf("%s: exit %d, want %d: %s", tc.name, code, tc.code, c.errOut)
		}
		if _, err := os.Stat(c.dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: init saved settings anyway", tc.name)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("rejected inits ran ssh: %q", calls)
	}

	c := newClient(t, h)
	c.executable = func() (string, error) { return "/Users/x/bin/hib'$(id)'", nil }
	if code := c.hib("init", "-host", testHost); code != 1 || !strings.Contains(c.errOut.String(), "can't use") {
		t.Errorf("odd executable path: exit %d, stderr %q", code, c.errOut)
	}
	c = newClient(t, h)
	c.dir = filepath.Join(t.TempDir(), "a\"b")
	if code := c.hib("init", "-host", testHost); code != 1 || !strings.Contains(c.errOut.String(), "can't use") {
		t.Errorf("odd -config: exit %d, stderr %q", code, c.errOut)
	}
}

// TestInitWithoutHost checks that init without --host names the missing flag, and on a laptop
// that's set up says for which host, instead of rejecting an empty host.
func TestInitWithoutHost(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	if code := c.hib("init"); code != 2 || !strings.Contains(c.errOut.String(), "missing --host USER@HOST") {
		t.Errorf("init without --host: exit %d, stderr %q; want 2 and the missing flag", code, c.errOut)
	}

	if code := c.hib("init", "-host", testHost); code != 0 {
		t.Fatalf("init exit %d: %s", code, c.errOut)
	}
	h.takeCalls()
	code := c.hib("init")
	if stderr := c.errOut.String(); code != 2 || !strings.Contains(stderr, "already set up for "+testHost) ||
		!strings.Contains(stderr, "\nnext: hib new NAME. Then ssh NAME.hib\n") {
		t.Errorf("init without -host once set up: exit %d, stderr %q; want 2, the saved host and what's next", code, stderr)
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("init without -host ran ssh: %q", calls)
	}
	if cfg, err := loadConfig(c.dir); err != nil || cfg.Host != testHost {
		t.Errorf("init without -host changed the settings: %+v, %v", cfg, err)
	}
}

func TestInitFailsWhenHibdDoesnt(t *testing.T) {
	h := newHost(t)
	h.override = func([]string, io.Writer, io.Writer) (int, bool) { return 127, true }
	c := newClient(t, h)
	if code := c.hib("init", "-host", testHost); code != 1 || !strings.Contains(c.errOut.String(), "make deploy") {
		t.Errorf("exit %d, stderr %q", code, c.errOut)
	}
	if _, err := os.Stat(c.dir); !errors.Is(err, os.ErrNotExist) {
		t.Error("init saved settings for a host without hibd")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hib")
	if _, err := loadConfig(dir); err == nil || !strings.Contains(err.Error(), "run hib init --host USER@HOST first") {
		t.Errorf("no settings: %v", err)
	}
	good := &config{Host: testHost, dir: dir}
	if err := good.save(testHib); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(dir); err != nil {
		t.Fatalf("good settings: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	for name, body := range map[string]string{
		"unknown field": `{"host":"` + testHost + `","proxyCommand":"sh"}`,
		"bad host":      `{"host":"a b"}`,
		"bad identity":  `{"host":"h","identity":"id_ed25519"}`,
		"bad Azure":     `{"host":"h","azure":{"group":"","vm":"v"}}`,
		"not JSON":      `host = h`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(dir); err == nil {
			t.Errorf("%s: loaded %s", name, body)
		}
	}

	if err := good.save(testHib); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, path} {
		if err := os.Chmod(p, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(dir); err == nil || !strings.Contains(err.Error(), "others can change") {
			t.Errorf("%s writable by others: %v", p, err)
		}
		if err := os.Chmod(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAddInclude(t *testing.T) {
	dir := t.TempDir()
	file := "/x/hib/ssh_config"
	line := `Include "` + file + `"`

	fresh := filepath.Join(dir, "new", ".ssh", "config")
	if added, err := addInclude(fresh, file); !added || err != nil {
		t.Fatalf("new file: %v, %v", added, err)
	}
	data, _ := os.ReadFile(fresh)
	if !strings.HasSuffix(string(data), "\n"+line+"\n") {
		t.Errorf("new file =\n%s", data)
	}
	for p, want := range map[string]os.FileMode{fresh: 0o600, filepath.Dir(fresh): 0o700 | os.ModeDir} {
		if fi, err := os.Stat(p); err != nil || fi.Mode() != want {
			t.Errorf("%s: %v, %v; want %v", p, fi.Mode(), err, want)
		}
	}

	existing := filepath.Join(dir, "existing")
	body := "Host a\n  User b\n   " + line + "  \n"
	if err := os.WriteFile(existing, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	if added, err := addInclude(existing, file); added || err != nil {
		t.Errorf("already included: %v, %v", added, err)
	}

	target := filepath.Join(dir, "dotfiles", "ssh_config")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("Host a\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if added, err := addInclude(link, file); !added || err != nil {
		t.Fatalf("symlink: %v, %v", added, err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("replaced the symlink: %v, %v", fi.Mode(), err)
	}
	data, _ = os.ReadFile(target)
	if !strings.HasSuffix(string(data), line+"\n\nHost a\n") {
		t.Errorf("target =\n%s", data)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o640 {
		t.Errorf("target mode %v, want 0640 kept", fi.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Errorf("left temporary files: %v", entries)
	}
}

func TestPins(t *testing.T) {
	cfg := &config{Host: testHost, dir: filepath.Join(t.TempDir(), "hib")}
	if err := cfg.save(testHib); err != nil {
		t.Fatal(err)
	}
	key := newKey(t)
	if err := cfg.pin("app", key); err != nil {
		t.Fatal(err)
	}
	if err := cfg.unpin("app"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.unpin("app"); err != nil {
		t.Errorf("unpin twice: %v", err)
	}
	known := filepath.Join(cfg.dir, "known_hosts")
	if err := os.Chmod(known, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := cfg.pin("app", key); err == nil || !strings.Contains(err.Error(), "others can change") {
		t.Errorf("pin into a directory others can change: %v", err)
	}
}

func TestSSHArgs(t *testing.T) {
	cfg := &config{Host: testHost}
	want := []string{"-T",
		"-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no", "-o", "RemoteCommand=none", "-o", "StdinNull=no", "-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=60", "-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes", "--", testHost, "hibd", "ls", "--json"}
	if got := cfg.sshArgs("ls", "--json"); !slices.Equal(got, want) {
		t.Errorf("sshArgs =\n%q\nwant\n%q", got, want)
	}
	cfg.Identity, cfg.prompt = "/k/id", true
	got := cfg.sshArgs("ls")
	if slices.Contains(got, "BatchMode=yes") || !slices.Contains(got, "/k/id") || !slices.Contains(got, "IdentitiesOnly=yes") {
		t.Errorf("with identity and prompt: %q", got)
	}
	if i := slices.Index(got, "--"); i < 0 || got[i+1] != testHost {
		t.Errorf("the host must follow --: %q", got)
	}
}

func TestRunCommand(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var out, errOut bytes.Buffer
	code, err := runCommand(script("echo", `cat; echo "$@"; echo oops >&2; exit 3`))(t.Context(), []string{"a", "b c"},
		strings.NewReader("in "), &out, &errOut)
	if code != 3 || err != nil || out.String() != "in a b c\n" || errOut.String() != "oops\n" {
		t.Errorf("got %d, %v, %q, %q", code, err, out.String(), errOut.String())
	}
	if code, err := runCommand(script("ok", "true"))(t.Context(), nil, nil, io.Discard, io.Discard); code != 0 || err != nil {
		t.Errorf("true: %d, %v", code, err)
	}
	if code, err := runCommand(filepath.Join(dir, "missing"))(t.Context(), nil, nil, io.Discard, io.Discard); code != -1 || err == nil {
		t.Errorf("missing program: %d, %v", code, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	code, err = runCommand(script("slow", "exec sleep 30"))(ctx, nil, nil, io.Discard, io.Discard)
	if code != -1 || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Errorf("timed out: %d, %v after %v", code, err, time.Since(start))
	}
}

func TestConfigDir(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		flag string
		want string
	}{
		{map[string]string{"HOME": "/h"}, "", "/h/.config/hibernaut"},
		{map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, "", "/x/hibernaut"},
		{map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "rel"}, "", "/h/.config/hibernaut"},
		{map[string]string{"HOME": "/h"}, "/flag", "/flag"},
		{map[string]string{}, "", ""},
	} {
		a := &app{getenv: func(k string) string { return tc.env[k] }}
		got, err := a.configDir(tc.flag)
		if got != tc.want || (tc.want == "") != (err != nil) {
			t.Errorf("env %v, -config %q: %q, %v; want %q", tc.env, tc.flag, got, err, tc.want)
		}
	}
}

func TestUsage(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	if code := c.hib("version"); code != 0 || c.out.String() != "hib "+version.Version+"\n" {
		t.Errorf("version: %d, %q", code, c.out)
	}
	if code := c.hib("help"); code != 0 || !strings.Contains(c.out.String(), "usage: hib") {
		t.Errorf("help: %d, %q", code, c.out)
	}
	if code := c.hib(); code != 2 {
		t.Errorf("no command: %d", code)
	}
	if code := c.hib("launch"); code != 2 || !strings.Contains(c.errOut.String(), "unknown command launch") {
		t.Errorf("unknown command: %d, %q", code, c.errOut)
	}
	if code := c.hib("ls"); code != 1 || !strings.Contains(c.errOut.String(), "run hib init --host USER@HOST first") {
		t.Errorf("before init: %d, %q", code, c.errOut)
	}
	var cfg config
	if err := json.Unmarshal([]byte(`{"host":"h","dir":"/x"}`), &cfg); err != nil || cfg.dir != "" {
		t.Errorf("config.json mustn't set unexported fields: %+v, %v", cfg, err)
	}
}
