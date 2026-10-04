package images

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/envblock"
)

func TestToolsInstall(t *testing.T) {
	run, root, calls := toolInstallFixture(t)
	block, err := envblock.Render([]envblock.Var{{Name: "GH_TOKEN", Value: "gho_placeholder"}})
	if err != nil {
		t.Fatal(err)
	}
	before := "UNRELATED=kept\nCODEX_CA_CERTIFICATE=/etc/ssl/certs/ca-certificates.crt\nCOPILOT_AUTO_UPDATE=true\n" + string(block)
	envPath := filepath.Join(root, "etc", "environment")
	writeToolFile(t, envPath, []byte(before), 0o644)
	if out, err := run(""); err != nil {
		t.Fatalf("install tools: %v: %s", err, out)
	}
	for name, want := range map[string]string{
		"usr/local/bin/codex":        "fixture codex\n",
		"usr/local/bin/copilot":      "fixture copilot\n",
		"usr/lib/hibernaut/websocat": "fixture websocat\n",
	} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s wasn't installed correctly: %v", name, err)
		}
		requireToolMode(t, filepath.Join(root, name), 0o755)
	}
	config, err := os.ReadFile(filepath.Join(root, "home/user/.codex/config.toml"))
	const wantCodex = `approval_policy = "never"
sandbox_mode = "danger-full-access"
check_for_update_on_startup = false
cli_auth_credentials_store = "file"

[analytics]
enabled = false
`
	if err != nil || string(config) != wantCodex {
		t.Fatalf("Codex config doesn't match the image contract: %v", err)
	}
	settings, err := os.ReadFile(filepath.Join(root, "home/user/.copilot/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var copilot struct {
		AutoUpdate bool   `json:"autoUpdate"`
		Banner     string `json:"banner"`
	}
	if err := json.Unmarshal(settings, &copilot); err != nil || copilot.AutoUpdate || copilot.Banner != "never" {
		t.Fatalf("Copilot settings must disable updates and the banner: %v", err)
	}
	for _, dir := range []string{"home/user/.codex", "home/user/.copilot"} {
		requireToolMode(t, filepath.Join(root, dir), 0o700)
	}
	for _, file := range []string{"home/user/.codex/config.toml", "home/user/.copilot/settings.json"} {
		requireToolMode(t, filepath.Join(root, file), 0o600)
	}
	owners, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{
		"home/user/.codex/config.toml", "home/user/.copilot/settings.json",
	} {
		found := false
		for line := range strings.SplitSeq(string(owners), "\n") {
			if strings.Contains(line, "-o user -g user") && strings.HasSuffix(line, filepath.Join(root, dest)) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s must be installed as user:user", dest)
		}
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	wantEnv := "UNRELATED=kept\nCODEX_CA_CERTIFICATE=/etc/ssl/certs/ca-certificates.crt\n" +
		string(block) + "COPILOT_AUTO_UPDATE=false\n"
	if string(env) != wantEnv {
		t.Errorf("tool installation changed unrelated environment or the managed block")
	}
	requireToolMode(t, envPath, 0o644)
	if out, err := run(""); err != nil {
		t.Fatalf("second tools install: %v: %s", err, out)
	}
	env, err = os.ReadFile(envPath)
	if err != nil || string(env) != wantEnv {
		t.Errorf("the second tools install didn't preserve the environment: %v", err)
	}
}

func TestToolsRejectCorruptDownloads(t *testing.T) {
	for _, download := range []string{"codex.tar.gz", "copilot.tar.gz", "websocat"} {
		t.Run(download, func(t *testing.T) {
			run, root, _ := toolInstallFixture(t)
			dest := filepath.Join(root, "usr/local/bin/codex")
			writeToolFile(t, dest, []byte("existing binary"), 0o755)
			if out, err := run(download); err == nil {
				t.Fatalf("a corrupt download was accepted: %s", out)
			}
			data, err := os.ReadFile(dest)
			if err != nil || string(data) != "existing binary" {
				t.Fatalf("checksum failure changed an existing binary: %v", err)
			}
			for _, name := range []string{"usr/local/bin/copilot", "home/user/.codex/config.toml"} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Errorf("checksum failure installed %s", name)
				}
			}
			entries, err := os.ReadDir(filepath.Join(root, "usr/lib/hibernaut"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".tools-") {
					t.Error("checksum failure left download staging behind")
				}
			}
		})
	}
}

func toolInstallFixture(t *testing.T) (func(string) ([]byte, error), string, string) {
	t.Helper()
	root := t.TempDir()
	spec, err := Spec(Default, testCAPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	step := spec.Steps[len(spec.Steps)-1].Cmd
	install, ok := strings.CutSuffix(step, "/usr/lib/hibernaut/tools")
	if !ok {
		t.Fatal("the last step must run tools")
	}
	// Run the public plan's install command under an isolated root.
	install = strings.ReplaceAll(install, "put '/", "put '"+root+"/")
	if out, err := exec.Command("bash", "-c", install).CombinedOutput(); err != nil {
		t.Fatalf("install the tools step's files: %v: %s", err, out)
	}
	archives := t.TempDir()
	codexSum := toolArchive(t, filepath.Join(archives, "codex.tar.gz"), "codex-x86_64-unknown-linux-musl", "fixture codex\n")
	copilotSum := toolArchive(t, filepath.Join(archives, "copilot.tar.gz"), "copilot", "fixture copilot\n")
	writeToolFile(t, filepath.Join(archives, "websocat"), []byte("fixture websocat\n"), 0o600)
	pins := fmt.Sprintf("CODEX_VERSION=0.160.0\nCODEX_SHA256=%x\nCOPILOT_VERSION=1.0.91\nCOPILOT_SHA256=%x\n",
		codexSum, copilotSum)
	writeToolFile(t, filepath.Join(root, "usr/lib/hibernaut/versions"), []byte(pins), 0o644)
	fakes := t.TempDir()
	realInstall, err := exec.LookPath("install")
	if err != nil {
		t.Fatal(err)
	}
	realSHA, err := exec.LookPath("sha256sum")
	if err != nil {
		realSHA = filepath.Join(fakes, "real-sha256sum")
		writeToolFile(t, realSHA, []byte("#!/bin/sh\nexec shasum -a 256 \"$@\"\n"), 0o755)
	}
	for name, script := range map[string]string{
		"curl": `#!/bin/sh
set -eu
dest=
while [ "$#" -gt 0 ]; do
	case "$1" in
	-o) dest=$2; shift 2 ;;
	*) shift ;;
	esac
done
[ -n "$dest" ]
name=$(basename "$dest")
if [ "$name" = "$CORRUPT" ]; then
	printf 'corrupt download' >"$dest"
else
	cp "$ARCHIVES/$name" "$dest"
fi
`,
		"sha256sum": `#!/bin/sh
set -eu
[ "$#" -eq 2 ] && [ "$1" = -c ] && [ "$2" = - ]
read -r sum file
case "$file" in
*/websocat)
	[ "$sum" = 66f8dd3a0394761556339117f8bb5123bddefd44e087af2a72ec22b0bd08d514 ]
	[ "$(cat "$file")" = "fixture websocat" ]
	;;
*) printf '%s  %s\n' "$sum" "$file" | "$REAL_SHA" -c - ;;
esac
`,
		"install": `#!/bin/bash
set -eu
printf '%s\n' "$*" >>"$CALLS"
args=()
while [[ "$#" -gt 0 ]]; do
	case "$1" in
	-o|-g) shift 2 ;;
	*) args+=("$1"); shift ;;
	esac
done
exec "$REAL_INSTALL" "${args[@]}"
`,
	} {
		writeToolFile(t, filepath.Join(fakes, name), []byte(script), 0o755)
	}
	calls := filepath.Join(root, "install-calls")
	run := func(corrupt string) ([]byte, error) {
		cmd := exec.Command("bash", filepath.Join(root, "usr/lib/hibernaut/tools"), root)
		cmd.Env = append(os.Environ(), "PATH="+fakes+":"+os.Getenv("PATH"), "HOME="+root,
			"ARCHIVES="+archives, "CORRUPT="+corrupt, "CALLS="+calls,
			"REAL_INSTALL="+realInstall, "REAL_SHA="+realSHA)
		return cmd.CombinedOutput()
	}
	return run, root, calls
}

func toolArchive(t *testing.T, name, entry, contents string) [32]byte {
	t.Helper()
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	zip := gzip.NewWriter(file)
	archive := tar.NewWriter(zip)
	if err := archive.WriteHeader(&tar.Header{Name: entry, Mode: 0o755, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(contents)); err != nil {
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

func writeToolFile(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
}

func requireToolMode(t *testing.T, name string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != want {
		t.Errorf("%s must have mode %04o: %v", name, want, err)
	}
}
