package machines

import (
	"context"
	"crypto/sha256"
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
	// leaves the file alone.
	CodexAuth []byte
}

// GitIdentity is the name and email git signs commits with.
type GitIdentity struct {
	Name, Email string
}

// empty reports whether s writes nothing into a new machine.
func (s Setup) empty() bool {
	return len(s.EnvBlock) == 0 && s.Git == nil
}

// Write writes s into machine id through envd as root: first the block, then git's identity. A
// machine without git gets the block only.
func (s Setup) Write(ctx context.Context, e engine.Engine, id string) error {
	if err := WriteBlock(ctx, e, id, s.EnvBlock); err != nil {
		return err
	}
	if s.Git == nil {
		return nil
	}
	return writeGitIdentity(ctx, e, id, *s.Git)
}

// Hash identifies what Write writes, so a machine that has s needn't get it again.
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

// gitScript sets git's identity for every user in the machine. The name and email come as
// arguments, never as shell code. Images without git are fine.
const gitScript = `set -eu
command -v git >/dev/null 2>&1 || exit 0
git config --system --replace-all user.name "$1"
git config --system --replace-all user.email "$2"`
