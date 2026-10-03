package images

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/ca"
)

func TestSpecDev(t *testing.T) {
	caPEM := testCAPEM(t)
	spec, err := Spec(Default, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "hib-dev" || spec.Name != Template(Default) {
		t.Errorf("name %q", spec.Name)
	}
	if spec.FromImage != "ubuntu:24.04" || spec.CPUs != 2 || spec.MemoryMB != 2048 || spec.MinFreeDiskMB != 10240 {
		t.Errorf("spec %+v", spec)
	}
	if spec.StartCmd != "sudo -n /usr/lib/hibernaut/start" || spec.ReadyCmd != "/usr/lib/hibernaut/ready" {
		t.Errorf("start %q, ready %q", spec.StartCmd, spec.ReadyCmd)
	}
	if spec.Force {
		t.Error("Force set")
	}
	if len(spec.Steps) != 3 {
		t.Fatalf("%d steps", len(spec.Steps))
	}
	for i, s := range spec.Steps {
		if s.User != "root" {
			t.Errorf("step %d runs as %q", i, s.User)
		}
		// Linux limits each argument of a command to 128 KiB, and the step is one.
		if len(s.Cmd) > 64<<10 {
			t.Errorf("step %d is %d bytes", i, len(s.Cmd))
		}
		if strings.Contains(s.Cmd, "\n") {
			t.Errorf("step %d spans lines", i)
		}
	}
	if !strings.HasSuffix(spec.Steps[0].Cmd, "; /usr/lib/hibernaut/packages") ||
		!strings.HasSuffix(spec.Steps[1].Cmd, "; /usr/lib/hibernaut/configure") {
		t.Errorf("steps don't end by running their script")
	}
	if !strings.Contains(spec.Steps[1].Cmd, "put '/etc/gitconfig' 644 '") {
		t.Error("configure step doesn't install /etc/gitconfig with mode 0644")
	}
	caStep := spec.Steps[2].Cmd
	if !strings.Contains(caStep, "/usr/local/share/ca-certificates/hibernaut.crt") ||
		!strings.HasSuffix(caStep, "; /usr/lib/hibernaut/trust-ca") {
		t.Errorf("CA step = %q", caStep)
	}
	if strings.Contains(caStep, "HIBERNAUT_IMAGE_ROOT") || strings.Contains(caStep, "runtime_root") {
		t.Errorf("CA step has a test-only runtime root seam: %q", caStep)
	}
	if strings.Contains(caStep, string(caPEM)) {
		t.Error("CA step embeds raw PEM instead of base64")
	}
	if !strings.Contains(caStep, base64.StdEncoding.EncodeToString(caPEM)) {
		t.Error("CA step doesn't carry the CA as base64")
	}
}

func TestGitHubGitConfig(t *testing.T) {
	root := t.TempDir()
	config, err := files.ReadFile("dev/gitconfig")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(configPath, config, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"github helper", []string{"config", "--get-all", "credential.https://github.com.helper"}, "\n!/usr/bin/gh auth git-credential\n"},
		{"gist helper", []string{"config", "--get-all", "credential.https://gist.github.com.helper"}, "\n!/usr/bin/gh auth git-credential\n"},
		{"scp URL", []string{"ls-remote", "--get-url", "git@github.com:solev/hibernaut-e2e.git"}, "https://github.com/solev/hibernaut-e2e.git\n"},
		{"SSH URL", []string{"ls-remote", "--get-url", "ssh://git@github.com/solev/hibernaut-e2e.git"}, "https://github.com/solev/hibernaut-e2e.git\n"},
		{"other host", []string{"ls-remote", "--get-url", "git@example.com:owner/repo.git"}, "git@example.com:owner/repo.git\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("git", tc.args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GIT_CONFIG_SYSTEM="+configPath, "GIT_CONFIG_GLOBAL="+os.DevNull,
				"GIT_CONFIG_NOSYSTEM=0", "GIT_CEILING_DIRECTORIES="+root)
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != tc.want {
				t.Fatalf("git %v = %q, %v, want %q", tc.args, out, err, tc.want)
			}
		})
	}
}

func TestSpecUnknown(t *testing.T) {
	_, err := Spec("nope", testCAPEM(t))
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "dev") {
		t.Fatalf("err %v", err)
	}
	if got := Names(); len(got) != 1 || got[0] != "dev" {
		t.Errorf("Names() = %v", got)
	}
}

func TestSpecRejectsBadCA(t *testing.T) {
	caPEM := testCAPEM(t)
	for _, tc := range []struct {
		name string
		pem  []byte
		want string
	}{
		{"not PEM", []byte("not a pem certificate"), "one PEM CERTIFICATE block"},
		{"not a certificate", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")}), "one PEM CERTIFICATE block"},
		{"not a CA", testLeafPEM(t), "not a CA certificate"},
		{"two certificates", append(bytes.Clone(caPEM), caPEM...), "one PEM CERTIFICATE block"},
		{"trailing junk", append(bytes.Clone(caPEM), []byte("junk")...), "one PEM CERTIFICATE block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Spec(Default, tc.pem)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Spec = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHibernautCAInstallStep(t *testing.T) {
	caPEM := testCAPEM(t)
	spec, err := Spec(Default, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	envPath := filepath.Join(root, "etc", "environment")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatal(err)
	}
	before := strings.Join([]string{
		"UNRELATED=kept",
		"SSL_CERT_FILE=/old",
		"",
	}, "\n")
	if err := os.WriteFile(envPath, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	update := filepath.Join(bin, "update-ca-certificates")
	updateScript := `#!/bin/sh
set -eu
	[ "$#" -eq 0 ]
	root="${FAKE_ROOT:?}"
	mkdir -p "$root/etc/ssl/certs"
	cp "$root/usr/local/share/ca-certificates/hibernaut.crt" "$root/etc/ssl/certs/ca-certificates.crt"
	`
	if err := os.WriteFile(update, []byte(updateScript), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := strings.Cut(spec.Steps[len(spec.Steps)-1].Cmd, "; /usr/lib/hibernaut/trust-ca"); !ok {
		t.Fatalf("last step = %q, want it to run trust-ca", spec.Steps[len(spec.Steps)-1].Cmd)
	}
	catalogStep := catalog[Default].steps[len(catalog[Default].steps)-1]
	install, err := installStepScript(Default, root, catalogStep, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", install)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CA install: %v: %s", err, out)
	}
	trustCA := filepath.Join(root, "usr", "lib", "hibernaut", "trust-ca")
	cmd = exec.Command(trustCA, root)
	cmd.Env = append(os.Environ(), "FAKE_ROOT="+root, "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CA step: %v: %s", err, out)
	}
	certPath := filepath.Join(root, "usr", "local", "share", "ca-certificates", "hibernaut.crt")
	got, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, caPEM) {
		t.Errorf("installed CA differs")
	}
	info, err := os.Stat(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("CA mode = %v, want 0644", info.Mode().Perm())
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []string{
		"UNRELATED=kept",
		"NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/hibernaut.crt",
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt",
		"CODEX_CA_CERTIFICATE=/etc/ssl/certs/ca-certificates.crt",
	}
	for _, line := range wantLines {
		if !strings.Contains(string(env), line+"\n") {
			t.Errorf("/etc/environment missing %q:\n%s", line, env)
		}
	}
	if strings.Contains(string(env), "SSL_CERT_FILE=/old") {
		t.Errorf("/etc/environment kept stale SSL_CERT_FILE:\n%s", env)
	}
	afterFirst := bytes.Clone(env)
	cmd = exec.Command(trustCA, root)
	cmd.Env = append(os.Environ(), "FAKE_ROOT="+root, "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second CA step: %v: %s", err, out)
	}
	afterSecond, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterSecond, afterFirst) {
		t.Errorf("second run changed /etc/environment:\nfirst:\n%s\nsecond:\n%s", afterFirst, afterSecond)
	}
}

// Every file in an image's directory is installed by exactly one step, and every installed file
// exists.
func TestImageFilesInstalled(t *testing.T) {
	for name, img := range catalog {
		installed := map[string]int{}
		for _, s := range img.steps {
			for _, f := range s.files {
				installed[f.name]++
				if _, err := fs.Stat(files, path.Join(name, f.name)); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}
		}
		entries, err := fs.ReadDir(files, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if n := installed[e.Name()]; n != 1 {
				t.Errorf("%s/%s installed %d times", name, e.Name(), n)
			}
		}
	}
}

// The install part of each step writes its files with the right contents and modes, and
// writing them again replaces them.
func TestInstallScript(t *testing.T) {
	for name, img := range catalog {
		for i, s := range img.steps {
			root := t.TempDir()
			script, err := installScript(name, root, s.files)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if out, err := exec.Command("bash", "-c", script).CombinedOutput(); err != nil {
					t.Fatalf("%s step %d: %v: %s", name, i, err, out)
				}
			}
			for _, f := range s.files {
				dest := filepath.Join(root, f.dest)
				got, err := os.ReadFile(dest)
				if err != nil {
					t.Fatal(err)
				}
				want, _ := files.ReadFile(path.Join(name, f.name))
				if !bytes.Equal(got, want) {
					t.Errorf("%s differs from %s", dest, f.name)
				}
				info, _ := os.Stat(dest)
				if info.Mode().Perm() != f.mode {
					t.Errorf("%s has mode %v, want %v", dest, info.Mode().Perm(), f.mode)
				}
				if _, err := os.Stat(dest + ".tmp"); err == nil {
					t.Errorf("%s.tmp left behind", dest)
				}
			}
		}
	}
}

func TestInstallScriptQuotesPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), `it's $(here)`)
	script, err := installScript("dev", root, []file{{"ready", "/x/ready", 0o700}})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := `set -euo pipefail; umask 022; put() { install -d -m 755 "$(dirname "$1")"; printf %s "$3" | base64 -d >"$1.tmp"; chmod "$2" "$1.tmp"; mv -f "$1.tmp" "$1"; }; `
	if !strings.HasPrefix(script, wantPrefix) {
		t.Fatalf("installScript prefix changed:\n%s", script)
	}
	if strings.Contains(script, "HIBERNAUT_IMAGE_ROOT") || strings.Contains(script, "runtime_root") {
		t.Fatalf("installScript has a shipped runtime root seam:\n%s", script)
	}
	if out, err := exec.Command("bash", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "x", "ready")); err != nil {
		t.Fatal(err)
	}
}

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if _, err := ca.Init(certPath, keyPath, "Hibernaut Test CA"); err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	return caPEM
}

func testLeafPEM(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if _, err := ca.Init(certPath, keyPath, "Hibernaut Test CA"); err != nil {
		t.Fatal(err)
	}
	authority, err := ca.Load(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := authority.Leaf("localhost")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
}

// A login shell keeps a TERM the machine has terminfo for and falls back to xterm-256color for
// one it hasn't, so clear, nano and tmux work from any terminal. With no terminal, TERM stays
// unset.
func TestTermFallback(t *testing.T) {
	script, err := files.ReadFile("dev/hib-term.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ term, want string }{
		{"xterm-256color", "TERM=xterm-256color"},
		{"hib-no-such-terminal", "TERM=xterm-256color"},
		{"", ""},
	} {
		// env is a child process, so it shows TERM as programs started from the shell see it.
		cmd := exec.Command("sh", "-c", string(script)+"\nenv")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		if c.term != "" {
			cmd.Env = append(cmd.Env, "TERM="+c.term)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("TERM=%q: %v", c.term, err)
		}
		var got string
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "TERM=") {
				got = line
			}
		}
		if got != c.want {
			t.Errorf("TERM=%q: after the script, env has %q, want %q", c.term, got, c.want)
		}
	}
}

// Machines are reachable only by key, as user, without forwarding anything back to the laptop.
// E2B's own sshd settings allow root and empty passwords, so nothing may be included.
func TestSSHDConfigHardened(t *testing.T) {
	data, err := files.ReadFile("dev/sshd_config")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		key = strings.ToLower(key)
		if key == "include" || key == "match" {
			t.Errorf("sshd_config has %q", line)
		}
		if _, dup := got[key]; dup {
			t.Errorf("%s set twice", key)
		}
		got[key] = value
	}
	for key, want := range map[string]string{
		"allowusers":                   "user",
		"permitrootlogin":              "no",
		"authenticationmethods":        "publickey",
		"passwordauthentication":       "no",
		"kbdinteractiveauthentication": "no",
		"permitemptypasswords":         "no",
		"hostbasedauthentication":      "no",
		"authorizedkeysfile":           "/etc/hibernaut/ssh/authorized_keys",
		"hostkey":                      "/etc/hibernaut/ssh/ssh_host_ed25519_key",
		"allowagentforwarding":         "no",
		"allowtcpforwarding":           "local",
		"allowstreamlocalforwarding":   "no",
		"gatewayports":                 "no",
		"x11forwarding":                "no",
		"permittunnel":                 "no",
		"permituserenvironment":        "no",
		"strictmodes":                  "yes",
	} {
		if got[key] != want {
			t.Errorf("%s is %q, want %q", key, got[key], want)
		}
	}
}
