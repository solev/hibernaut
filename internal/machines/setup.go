package machines

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"github.com/solev/hibernaut/internal/engine"
)

// Setup is what hibd writes into each machine after machine-init, and again whenever it
// changes: the managed /etc/environment block, git's identity and Codex's login file.
type Setup struct {
	// EnvBlock is the managed /etc/environment block. It holds placeholders only. Empty removes
	// the block.
	EnvBlock []byte
	// Git is git's system-wide identity. Nil leaves git's configuration alone.
	Git *GitIdentity
	// CodexAuth is Codex's login file, /home/user/.codex/auth.json, with placeholders only. Nil
	// or empty leaves the file alone.
	CodexAuth []byte
}

// GitIdentity is the name and email git signs commits with.
type GitIdentity struct {
	Name, Email string
}

func (s Setup) empty() bool {
	return len(s.EnvBlock) == 0 && s.Git == nil && len(s.CodexAuth) == 0
}

// Write writes s into machine id through envd as root: first the block, then git's identity,
// then Codex's login file. A machine without git gets the rest.
func (s Setup) Write(ctx context.Context, e engine.Engine, id string) error {
	if err := WriteBlock(ctx, e, id, s.EnvBlock); err != nil {
		return err
	}
	if s.Git != nil {
		if err := writeGitIdentity(ctx, e, id, *s.Git); err != nil {
			return err
		}
	}
	if len(s.CodexAuth) == 0 {
		return nil
	}
	return writeCodexAuth(ctx, e, id, s.CodexAuth)
}

// Hash identifies what Write writes.
func (s Setup) Hash() [32]byte {
	h := sha256.New()
	field := func(b []byte) {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
		h.Write(b)
	}
	field([]byte("hibernaut machine setup v1"))
	field(s.EnvBlock)
	if s.Git != nil {
		h.Write([]byte{1})
		field([]byte(s.Git.Name))
		field([]byte(s.Git.Email))
	} else {
		h.Write([]byte{0})
	}
	if len(s.CodexAuth) != 0 {
		h.Write([]byte{1})
		field(s.CodexAuth)
	} else {
		h.Write([]byte{0})
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

func writeGitIdentity(ctx context.Context, e engine.Engine, id string, g GitIdentity) error {
	res, err := e.Exec(ctx, id, engine.ExecRequest{
		Cmd:  "sh",
		Args: []string{"-c", gitScript, "hibd-git", g.Name, g.Email},
		User: "root",
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("setting git's identity exited %d: %q", res.ExitCode, tail(res.Stderr, 1024))
	}
	return nil
}

const setupLockScript = `if command -v flock >/dev/null 2>&1; then
	exec 9>>"${3:-/run/hibernaut-setup.lock}"
	flock 9
fi`

const gitScript = `set -eu
` + setupLockScript + `
command -v git >/dev/null 2>&1 || exit 0
git config --system --replace-all user.name "$1"
git config --system --replace-all user.email "$2"`

const codexDir = "/home/user/.codex"

func writeCodexAuth(ctx context.Context, e engine.Engine, id string, auth []byte) error {
	res, err := e.Exec(ctx, id, engine.ExecRequest{
		Cmd:  "sh",
		Args: []string{"-c", codexScript, "hibd-codex", codexDir, base64.StdEncoding.EncodeToString(auth)},
		User: "root",
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("writing Codex's login file exited %d: %q", res.ExitCode, tail(res.Stderr, 1024))
	}
	return nil
}

const codexScript = `set -eu
dir=$1
mkdir -p "$dir"
chown user:user "$dir"
chmod 0700 "$dir"
tmp=$(mktemp "$dir/.auth.json.XXXXXX")
trap 'rm -f "$tmp"' EXIT
printf '%s' "$2" | base64 -d > "$tmp"
chown user:user "$tmp"
chmod 0600 "$tmp"
mv -f "$tmp" "$dir/auth.json"
trap - EXIT`
