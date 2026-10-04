#!/usr/bin/env bash
# Installs key-only SSH, hibd, pinned Codex, its CA and egress proxy, the busy daemon, the pause-all guard,
# and the nightly and idle deallocate timers.
# `make deploy` runs it after copying the repo and the hibd binary to the VM. Safe to re-run: it
# never stops or restarts running machines.
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
"$here/install-codex.sh" "$here/../images/dev/versions"
"$hibd" ca init
# hibd runs as the user who ran this script (over ssh, from hib), and as root to pause every
# machine. Its config holds E2B's API key; its state directory is that user's alone.
user="${SUDO_USER:-}"
[[ -n "$user" && "$user" != root ]] || { echo "Run this as the user hibd runs as; it uses sudo" >&2; exit 1; }
group="$(id -gn "$user")"
# Keep local changes to the config.
[[ -f /etc/hibernaut/hibd.env ]] || install -D -m 640 "$here/hibd/hibd.env" /etc/hibernaut/hibd.env
chown "root:$group" /etc/hibernaut/hibd.env
chmod 640 /etc/hibernaut/hibd.env
install -d -m 700 -o "$user" -g "$group" /var/lib/hibernaut
chown "$user:$group" /var/lib/hibernaut
chmod 700 /var/lib/hibernaut
install -D -m 755 "$here/lib/quiesce" "$here/lib/deallocate-self" -t /usr/local/lib/hibernaut/

echo "==> Busy daemon, egress proxy, pause-all guard, nightly deallocate and idle deallocate"
install -m 755 "$here/idle-deallocate/idle-deallocate" /usr/local/bin/idle-deallocate
[[ -f /etc/default/idle-deallocate ]] || install -m 644 "$here/idle-deallocate/idle-deallocate.env" /etc/default/idle-deallocate
install -m 644 "$here/systemd/hibd.service" "$here/systemd/hibd-proxy.service" "$here/systemd/hibd-pause-all.service" \
  "$here/systemd/hibernaut-nightly.service" \
  "$here/systemd/hibernaut-nightly.timer" "$here/idle-deallocate/idle-deallocate.service" \
  "$here/idle-deallocate/idle-deallocate.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable -q --now idle-deallocate.timer hibernaut-nightly.timer
systemctl enable -q hibd.service hibd-proxy.service hibd-pause-all.service
# Restarting the daemon picks up a new hibd; stopping it leaves machines alone.
systemctl restart hibd.service
systemctl restart hibd-proxy.service
# Starting the guard would start E2B through Requires=, so only start it next to a running
# orchestrator. Never restart it: stopping it pauses every machine.
if systemctl is-active -q e2b-orchestrator.service; then
  systemctl start hibd-pause-all.service
fi
for unit in idle-deallocate.timer hibernaut-nightly.timer hibd.service hibd-proxy.service; do
  systemctl is-active -q "$unit" || { echo "$unit isn't active" >&2; exit 1; }
done
echo "Host set up. Next: azure/up.sh installs or starts E2B."
