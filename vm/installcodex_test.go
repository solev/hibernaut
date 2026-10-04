package vm

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallCodexMissingPins(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("bash", "install-codex.sh", filepath.Join(dir, "missing-versions"), filepath.Join(dir, "bin"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "deploy images/dev/versions") {
		t.Fatal("missing release pins must explain how to fix the deployment")
	}
}

func TestInstallCodex(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "codex.tar.gz")
	const binary = `#!/bin/sh
set -eu
[ "$#" -eq 1 ] && [ "$1" = --version ]
[ "$HOME" = "$CODEX_HOME" ]
[ -z "${HIB_TEST_SECRET:-}" ]
printf 'codex-cli 0.160.0\n'
`
	sum := writeCodexArchive(t, archive, binary)
	versions := filepath.Join(dir, "versions")
	if err := os.WriteFile(versions, fmt.Appendf(nil, "CODEX_VERSION=0.160.0\nCODEX_SHA256=%x\n", sum), 0o600); err != nil {
		t.Fatal(err)
	}
	fakes := filepath.Join(dir, "fakes")
	if err := os.Mkdir(fakes, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		shim := filepath.Join(fakes, "sha256sum")
		if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec shasum -a 256 \"$@\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS == "darwin" {
		shim := filepath.Join(fakes, "mv")
		if err := os.WriteFile(shim, []byte(`#!/bin/sh
set -eu
if [ "$1" = -T ]; then
	shift
	exec /bin/mv -h "$@"
fi
exec /bin/mv "$@"
`), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	curl := `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$CALLS"
dest=
while [ "$#" -gt 0 ]; do
	case "$1" in
	-o) dest=$2; shift 2 ;;
	*) shift ;;
	esac
done
[ -n "$dest" ]
cp "$ARCHIVE" "$dest"
`
	if err := os.WriteFile(filepath.Join(fakes, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	dest := filepath.Join(dir, "usr/local/bin")
	oldDir := filepath.Join(dir, "usr/local/lib/codex-0.159.0")
	if err := os.MkdirAll(filepath.Join(oldDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "bin/codex"), []byte("old codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func() ([]byte, error) {
		cmd := exec.Command("bash", "install-codex.sh", versions, dest)
		cmd.Env = append(os.Environ(), "PATH="+fakes+":"+os.Getenv("PATH"), "ARCHIVE="+archive,
			"CALLS="+calls, "HIB_TEST_SECRET=must-not-reach-codex")
		return cmd.CombinedOutput()
	}
	out, err := run()
	if err != nil {
		t.Fatalf("install Codex: %v: %s", err, out)
	}
	installed := filepath.Join(dest, "codex")
	data, err := os.ReadFile(installed)
	if err != nil || string(data) != binary {
		t.Fatalf("installed binary differs: %v", err)
	}
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary must have mode 0755: %v", err)
	}
	packageDir := filepath.Join(dir, "usr/local/lib/codex-0.160.0")
	target, err := os.Readlink(installed)
	if err != nil || target != filepath.Join(packageDir, "bin/codex") {
		t.Fatalf("Codex must link to the canonical package entrypoint: %v", err)
	}
	for _, name := range []string{
		"bin/codex-code-mode-host", "codex-package.json", "codex-path/rg",
		"codex-resources/bwrap", "codex-resources/zsh/bin/zsh",
	} {
		if _, err := os.Stat(filepath.Join(packageDir, name)); err != nil {
			t.Errorf("the Codex package is missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("the old Codex package directory wasn't removed")
	}
	first, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(first),
		"https://github.com/openai/codex/releases/download/rust-v0.160.0/codex-package-x86_64-unknown-linux-musl.tar.gz") {
		t.Fatalf("Codex wasn't downloaded from the pinned release: %v", err)
	}
	out, err = run()
	if err != nil {
		t.Fatalf("second install: %v: %s", err, out)
	}
	second, err := os.ReadFile(calls)
	if err != nil || string(second) != string(first) {
		t.Fatalf("the matching version was downloaded again: %v", err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nprintf 'codex-cli 0.159.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = run()
	if err != nil {
		t.Fatalf("replace old version: %v: %s", err, out)
	}
	data, err = os.ReadFile(installed)
	if err != nil || string(data) != binary {
		t.Fatalf("the old version wasn't replaced: %v", err)
	}
	if err := os.Remove(installed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nprintf 'codex-cli 0.160.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err = run(); err != nil {
		t.Fatalf("migrate a matching bare binary: %v: %s", err, out)
	}
	if target, err := os.Readlink(installed); err != nil || target != filepath.Join(packageDir, "bin/codex") {
		t.Fatalf("a matching bare binary must still migrate to the package layout: %v", err)
	}
	for _, missing := range []string{"bin/codex", "bin/codex-code-mode-host", "codex-package.json"} {
		t.Run("missing "+missing, func(t *testing.T) {
			sum := writeCodexArchive(t, archive, binary, missing)
			if err := os.WriteFile(versions, fmt.Appendf(nil, "CODEX_VERSION=0.160.0\nCODEX_SHA256=%x\n", sum), 0o600); err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(oldDir, "bin/codex")
			if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(old, []byte("old codex"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(installed); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(old, installed); err != nil {
				t.Fatal(err)
			}
			if out, err := run(); err == nil {
				t.Fatalf("an incomplete Codex package was accepted: %s", out)
			}
			if target, err := os.Readlink(installed); err != nil || target != old {
				t.Fatalf("the incomplete package changed the current symlink: %v", err)
			}
			if data, err := os.ReadFile(old); err != nil || string(data) != "old codex" {
				t.Fatalf("the incomplete package removed the older version: %v", err)
			}
		})
	}
	if err := os.WriteFile(archive, []byte("corrupt download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err = run(); err == nil {
		t.Fatalf("a corrupt archive was accepted: %s", out)
	}
	if target, err := os.Readlink(installed); err != nil || target != filepath.Join(oldDir, "bin/codex") {
		t.Fatal("a checksum failure must not replace the current symlink")
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the installer left temporary symlinks behind: %v", err)
	}
	entries, err = os.ReadDir(filepath.Dir(packageDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".codex-install-") {
			t.Error("the installer left staging files behind")
		}
	}
}

func writeCodexArchive(t *testing.T, name, binary string, missing ...string) [32]byte {
	t.Helper()
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	zip := gzip.NewWriter(file)
	archive := tar.NewWriter(zip)
	entries := map[string]string{
		"bin/codex":                   binary,
		"bin/codex-code-mode-host":    "fixture code mode host\n",
		"codex-package.json":          "{\"version\":\"0.160.0\"}\n",
		"codex-path/rg":               "fixture rg\n",
		"codex-resources/bwrap":       "fixture bwrap\n",
		"codex-resources/zsh/bin/zsh": "fixture zsh\n",
	}
	for _, entry := range missing {
		delete(entries, entry)
	}
	for entry, contents := range entries {
		mode := int64(0o755)
		if strings.HasSuffix(entry, ".json") {
			mode = 0o644
		}
		if err := archive.WriteHeader(&tar.Header{Name: entry, Mode: mode, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []io.Closer{archive, zip, file} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}
