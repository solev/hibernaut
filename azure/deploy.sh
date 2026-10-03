#!/usr/bin/env bash
# Builds hibd for Linux, copies it and the host scripts to the VM, and installs them (hibd, the
# CA, egress proxy, pause-all guard, and nightly and idle deallocate timers). Starts the VM if
# needed. Never restarts E2B or touches running machines.
set -euo pipefail
source "$(dirname "$0")/common.sh"

make -s -C "$PROJECT_ROOT" build-linux
ensure_running
echo "==> Syncing to $VM_USER@$VM_IP:~/hibernaut"
sync_to_vm "$VM_IP"
ssh "$VM_USER@$VM_IP" 'hibernaut/vm/setup-host.sh'
