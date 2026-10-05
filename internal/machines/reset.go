package machines

import (
	"context"
	"fmt"

	"github.com/solev/hibernaut/internal/engine"
)

const hostKeyDir = "/etc/hibernaut/ssh"

func (m *Manager) resetHostKey(ctx context.Context, id string) error {
	if _, err := m.pinPath(id); err != nil {
		return err
	}
	res, err := m.e.Exec(ctx, id, engine.ExecRequest{
		Cmd: "sh", User: "root",
		Args: []string{"-c", resetScript, "hibd-reset", hostKeyDir, id},
	})
	if err != nil {
		return fmt.Errorf("resetting the copied host key: %w", safeError(err))
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("resetting the copied host key exited %d: %q", res.ExitCode, tail(res.Stderr, 1024))
	}
	return nil
}

const resetScript = `set -eu
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
umask 022
dir=$1
install -d -m 755 -o root -g root "$dir"
exec 9<"$dir"
flock 9
[ "$(cat "$dir/key-reset-for" 2>/dev/null || true)" = "$2" ] && exit 0
rm -f "$dir/ssh_host_ed25519_key" "$dir/ssh_host_ed25519_key.pub"
printf '%s\n' "$2" >"$dir/key-reset-for"`
