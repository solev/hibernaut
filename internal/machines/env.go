package machines

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/envblock"
	"github.com/solev/hibernaut/internal/vault"
)

const environmentPath = "/etc/environment"

// BlockFromSecrets renders the managed /etc/environment block for secrets.
func BlockFromSecrets(secrets []vault.Secret) ([]byte, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	vars := make([]envblock.Var, len(secrets))
	for i, s := range secrets {
		vars[i] = envblock.Var{Name: s.Env, Value: s.Placeholder}
	}
	slices.SortFunc(vars, func(a, b envblock.Var) int { return strings.Compare(a.Name, b.Name) })
	return envblock.Render(vars)
}

// WriteBlock writes block into id's /etc/environment through envd as root.
func WriteBlock(ctx context.Context, e engine.Engine, id string, block []byte) error {
	var last error
	for range 4 {
		file, err := readEnvironment(ctx, e, id)
		if err != nil {
			return err
		}
		next, err := envblock.Merge(file, block)
		if err != nil {
			return err
		}
		if bytes.Equal(file, next) {
			return nil
		}
		err = compareAndSwapEnvironment(ctx, e, id, file, next)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errCASMismatch) {
			return err
		}
		last = err
	}
	return fmt.Errorf("writing %s: %w", environmentPath, last)
}

func readEnvironment(ctx context.Context, e engine.Engine, id string) ([]byte, error) {
	res, err := e.Exec(ctx, id, engine.ExecRequest{Cmd: "cat", Args: []string{environmentPath}, User: "root"})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("cat %s exited %d: %q", environmentPath, res.ExitCode, tail(res.Stderr, 1024))
	}
	return res.Stdout, nil
}

func compareAndSwapEnvironment(ctx context.Context, e engine.Engine, id string, old, next []byte) error {
	sum := sha256.Sum256(old)
	res, err := e.Exec(ctx, id, engine.ExecRequest{
		Cmd:  "sh",
		Args: []string{"-c", casScript, "hibd-env", fmt.Sprintf("%x", sum), base64.StdEncoding.EncodeToString(next)},
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
	return fmt.Errorf("writing %s exited %d: %q", environmentPath, res.ExitCode, tail(res.Stderr, 1024))
}

var errCASMismatch = errors.New("current /etc/environment changed")

const casScript = `set -eu
file=/etc/environment
want=$1
data=$2
current=$(sha256sum "$file" | awk '{print $1}')
if [ "$current" != "$want" ]; then
	exit 75
fi
tmp=$(mktemp /etc/.environment.hibernaut.XXXXXX)
trap 'rm -f "$tmp"' EXIT
printf '%s' "$data" | base64 -d > "$tmp"
chmod 0644 "$tmp"
mv "$tmp" "$file"
trap - EXIT`
