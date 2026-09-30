#!/usr/bin/env bash
# Installs Hibernaut's host pieces: key-only SSH, hibd, the pause-all guard, the nightly
# deallocate timer and the idle deallocate timer. `make deploy` runs it after copying the repo
# and the hibd binary to the VM. Safe to re-run: it never stops or restarts running machines.
set -euo pipefail
[[ $EUID -eq 0 ]] || exec sudo "$0" "$@"

here="$(cd "$(dirname "$0")" && pwd)"
hibd="$here/../bin/hibd"
[[ -x "$hibd" ]] || { echo "Missing $hibd; run \`make deploy\` from your laptop" >&2; exit 1; }

echo "==> Hardening SSH (keys only, no root login)"
install -m 644 "$here/sshd/00-hardening.conf" /etc/ssh/sshd_config.d/00-hardening.conf
sshd -t
systemctl reload ssh

echo "==> $("$hibd" version)"
install -m 755 "$hibd" /usr/local/bin/hibd
# Keep local changes to the config.
[[ -f /etc/hibernaut/hibd.env ]] || install -D -m 644 "$here/hibd/hibd.env" /etc/hibernaut/hibd.env
install -D -m 755 "$here/lib/quiesce" "$here/lib/deallocate-self" -t /usr/local/lib/hibernaut/

echo "==> Pause-all guard, nightly deallocate and idle deallocate"
install -m 755 "$here/idle-deallocate/idle-deallocate" /usr/local/bin/idle-deallocate
[[ -f /etc/default/idle-deallocate ]] || install -m 644 "$here/idle-deallocate/idle-deallocate.env" /etc/default/idle-deallocate
install -m 644 "$here/systemd/hibd-pause-all.service" "$here/systemd/hibernaut-nightly.service" \
  "$here/systemd/hibernaut-nightly.timer" "$here/idle-deallocate/idle-deallocate.service" \
  "$here/idle-deallocate/idle-deallocate.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable -q --now idle-deallocate.timer hibernaut-nightly.timer
systemctl enable -q hibd-pause-all.service
# Starting the guard would start E2B through Requires=, so only start it next to a running
# orchestrator. Never restart it: stopping it pauses every machine.
if systemctl is-active -q e2b-orchestrator.service; then
  systemctl start hibd-pause-all.service
fi
for unit in idle-deallocate.timer hibernaut-nightly.timer; do
  systemctl is-active -q "$unit" || { echo "$unit isn't active" >&2; exit 1; }
done
echo "Host set up. Next: azure/up.sh installs or starts E2B."
