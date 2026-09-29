#!/usr/bin/env bash
# Host configuration: key-only SSH and the idle auto-deallocation timer. Safe to re-run.
set -euo pipefail
[[ $EUID -eq 0 ]] || exec sudo "$0" "$@"

here="$(cd "$(dirname "$0")" && pwd)"

echo "==> Hardening SSH (keys only, no root login)"
install -m 644 "$here/sshd/00-hardening.conf" /etc/ssh/sshd_config.d/00-hardening.conf
sshd -t
systemctl reload ssh

echo "==> Installing idle auto-deallocation"
install -m 755 "$here/idle-deallocate/idle-deallocate" /usr/local/bin/idle-deallocate
# Keep local tweaks to the thresholds.
[[ -f /etc/default/idle-deallocate ]] || install -m 644 "$here/idle-deallocate/idle-deallocate.env" /etc/default/idle-deallocate
install -m 644 "$here/idle-deallocate/idle-deallocate.service" "$here/idle-deallocate/idle-deallocate.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now idle-deallocate.timer
systemctl is-active idle-deallocate.timer
