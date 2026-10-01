//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHibInit checks what hib init left on the laptop: files with private modes, an idempotent
// Include in the user's ssh config, and a version that matches the host's hibd. setUp already
// ran init once; this re-runs it to prove a second run is a no-op.
func TestHibInit(t *testing.T) {
	ctx := testContext(t, 2*time.Minute)

	// hib keeps its settings and pinned host keys to itself.
	for _, f := range []struct {
		path string
		mode os.FileMode
	}{
		{hibDir, 0o700},
		{filepath.Join(hibDir, "config.json"), 0o600},
		{filepath.Join(hibDir, "ssh_config"), 0o600},
		{filepath.Join(hibDir, "known_hosts"), 0o700},
	} {
		fi, err := os.Stat(f.path)
		if err != nil {
			t.Fatalf("stat %s: %v", f.path, err)
		}
		if fi.Mode().Perm() != f.mode {
			t.Errorf("%s has mode %v, want %v", f.path, fi.Mode().Perm(), f.mode)
		}
	}

	// The ssh config for *.hib goes through hib, pins host keys under hibDir, and never lends
	// the machine the laptop's agent or X11.
	block := readFile(t, filepath.Join(hibDir, "ssh_config"))
	for _, want := range []string{"Host *.hib", "ProxyCommand", "bridge", "known_hosts", "ForwardAgent no", "ForwardX11 no"} {
		if !strings.Contains(block, want) {
			t.Errorf("ssh_config is missing %q:\n%s", want, block)
		}
	}

	// init added one Include line to the user's config. A second init doesn't add another or
	// warn about the version, because the host runs this exact build.
	includer := filepath.Join(hibDir, "included.conf")
	includeLine := `Include "` + filepath.Join(hibDir, "ssh_config") + `"`
	if got := strings.Count(readFile(t, includer), includeLine); got != 1 {
		t.Errorf("the user config has %d Include lines, want 1", got)
	}
	stdout, stderr, code := runHib(ctx, "init", "-host", vm.target,
		"-ssh-config", includer, "-azure-group", "rg-e2b-test", "-azure-vm", "e2b-test")
	if code != 0 {
		t.Fatalf("second hib init exited %d: %s%s", code, stdout, stderr)
	}
	if got := strings.Count(readFile(t, includer), includeLine); got != 1 {
		t.Errorf("after a second init the user config has %d Include lines, want 1", got)
	}
	// init warns with a "note:" exactly when hib and the host's hibd are different builds, which
	// happens whenever the working tree is dirty. Check the note tracks the real version match.
	hibVer := strings.TrimPrefix(strings.TrimSpace(hib(t, ctx, "version")), "hib ")
	hibdVer := hibdVersion(stdout)
	if hibVer == hibdVer {
		if strings.Contains(stdout, "note:") {
			t.Errorf("hib and hibd are both %q but init warned of a mismatch:\n%s", hibVer, stdout)
		}
	} else if !strings.Contains(stdout, "note:") {
		t.Errorf("hib is %q and hibd is %q but init didn't warn of the mismatch:\n%s", hibVer, hibdVer, stdout)
	}
	if strings.Contains(stdout, "added hib's settings") {
		t.Errorf("init said it added its settings again:\n%s", stdout)
	}
	if !strings.Contains(stdout, "runs hibd") {
		t.Errorf("init didn't report the host's hibd version:\n%s", stdout)
	}

	if out := hib(t, ctx, "version"); !strings.HasPrefix(out, "hib ") {
		t.Errorf("hib version = %q, want it to start with %q", strings.TrimSpace(out), "hib ")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// hibdVersion pulls the version out of init's "<host> runs hibd <v>" line.
func hibdVersion(initOutput string) string {
	for _, line := range strings.Split(initOutput, "\n") {
		if _, v, ok := strings.Cut(line, "runs hibd "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
