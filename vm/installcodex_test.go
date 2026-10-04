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
	"strings"
	"testing"
)

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
	dest := filepath.Join(dir, "bin")
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
	first, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(first),
		"https://github.com/openai/codex/releases/download/rust-v0.160.0/codex-x86_64-unknown-linux-musl.tar.gz") {
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
	if err := os.WriteFile(archive, []byte("corrupt download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(installed); err != nil {
		t.Fatal(err)
	}
	if out, err = run(); err == nil {
		t.Fatalf("a corrupt archive was accepted: %s", out)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatal("a checksum failure must not install a binary")
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the installer left staging files behind: %v", err)
	}
}

func writeCodexArchive(t *testing.T, name, binary string) [32]byte {
	t.Helper()
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	zip := gzip.NewWriter(file)
	archive := tar.NewWriter(zip)
	if err := archive.WriteHeader(&tar.Header{
		Name: "codex-x86_64-unknown-linux-musl", Mode: 0o755, Size: int64(len(binary)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(binary)); err != nil {
		t.Fatal(err)
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
