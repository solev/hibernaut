package machines

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/envblock"
	"github.com/solev/hibernaut/internal/vault"
)

const environmentPath = "/etc/environment"

// BlockFromSecrets renders the managed /etc/environment block for secrets. It skips secrets
// without a variable.
func BlockFromSecrets(secrets []vault.Secret) ([]byte, error) {
	var vars []envblock.Var
	for _, s := range secrets {
		if s.Env != "" {
			vars = append(vars, envblock.Var{Name: s.Env, Value: s.Placeholder})
		}
	}
	if len(vars) == 0 {
		return nil, nil
	}
	slices.SortFunc(vars, func(a, b envblock.Var) int { return strings.Compare(a.Name, b.Name) })
	return envblock.Render(vars)
}

// WriteBlock writes block into id's /etc/environment through envd as root.
func WriteBlock(ctx context.Context, e engine.Engine, id string, block []byte) error {
	return environmentFile.write(ctx, e, id, block)
}

// managedFile is a root-owned file in the machine that hibd keeps one managed block in. write
// reads it, merges the block, and writes the result back only if nothing changed it meanwhile,
// trying four times.
type managedFile struct {
	path string
	// tag is the write script's $0, which names it in ps and in tests.
	tag    string
	script string
	merge  func(file, block []byte) ([]byte, error)
}

var environmentFile = managedFile{path: environmentPath, tag: "hibd-env", script: casScript, merge: envblock.Merge}

func (f managedFile) write(ctx context.Context, e engine.Engine, id string, block []byte) error {
	var last error
	for range 4 {
		file, err := f.read(ctx, e, id)
		if err != nil {
			return err
		}
		next, err := f.merge(file, block)
		if err != nil {
			return err
		}
		if bytes.Equal(file, next) {
			return nil
		}
		err = f.compareAndSwap(ctx, e, id, file, next)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errCASMismatch) {
			return err
		}
		last = err
	}
	return fmt.Errorf("writing %s: %w", f.path, last)
}

func (f managedFile) read(ctx context.Context, e engine.Engine, id string) ([]byte, error) {
	res, err := e.Exec(ctx, id, engine.ExecRequest{Cmd: "cat", Args: []string{f.path}, User: "root"})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("cat %s exited %d: %q", f.path, res.ExitCode, tail(res.Stderr, 1024))
	}
	return res.Stdout, nil
}

func (f managedFile) compareAndSwap(ctx context.Context, e engine.Engine, id string, old, next []byte) error {
	sum := sha256.Sum256(old)
	res, err := e.Exec(ctx, id, engine.ExecRequest{
		Cmd:  "sh",
		Args: []string{"-c", f.script, f.tag, fmt.Sprintf("%x", sum), base64.StdEncoding.EncodeToString(next)},
		User: "root",
	})
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return nil
	}
	if res.ExitCode == 75 {
		return errCASMismatch
	}
	return fmt.Errorf("writing %s exited %d: %q", f.path, res.ExitCode, tail(res.Stderr, 1024))
}

var errCASMismatch = errors.New("the file changed as hibd wrote it")

var casScript = casScriptFor(environmentPath)

// casScriptFor returns the script that replaces file with $2, base64-encoded, if file's sha256
// is still $1, and exits 75 if it isn't. file must be a plain absolute path, since the script
// doesn't quote it.
func casScriptFor(file string) string {
	dir, base := path.Split(file)
	return `set -eu
` + setupLockScript + `
file=` + file + `
want=$1
data=$2
current=$(sha256sum "$file" | awk '{print $1}')
if [ "$current" != "$want" ]; then
	exit 75
fi
tmp=$(mktemp ` + dir + "." + base + `.hibernaut.XXXXXX)
trap 'rm -f "$tmp"' EXIT
printf '%s' "$data" | base64 -d > "$tmp"
chmod 0644 "$tmp"
mv "$tmp" "$file"
trap - EXIT`
}
