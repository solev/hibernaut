package images

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecDev(t *testing.T) {
	spec, err := Spec(Default)
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
	if len(spec.Steps) != 2 {
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
}

func TestSpecUnknown(t *testing.T) {
	_, err := Spec("nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "dev") {
		t.Fatalf("err %v", err)
	}
	if got := Names(); len(got) != 1 || got[0] != "dev" {
		t.Errorf("Names() = %v", got)
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
	if out, err := exec.Command("bash", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "x", "ready")); err != nil {
		t.Fatal(err)
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
