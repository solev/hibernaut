#!/usr/bin/env bash
# Wakes the VM if it is deallocated, syncs this project to it, installs E2B on first run,
# and starts E2B's services. Safe to run repeatedly.
set -euo pipefail
source "$(dirname "$0")/common.sh"

state=$(vm_power_state)
if [[ "$state" != "VM running" ]]; then
  echo "==> Starting $VM_NAME ($state)"
  az vm start -g "$RESOURCE_GROUP" -n "$VM_NAME" -o none
fi

ip=$(vm_ip)
wait_for_ssh "$ip"
echo "==> Syncing project to $VM_USER@$ip:~/hibernaut"
sync_to_vm "$ip"

ssh "$VM_USER@$ip" '[[ -f ~/.hibernaut-installed ]] || hibernaut/vm/install-e2b.sh'
ssh "$VM_USER@$ip" 'hibernaut/vm/start-e2b.sh'

echo
echo "Shell:         ./azure/ssh.sh"
echo "Watch E2B:     ./azure/ssh.sh -t tmux attach -t e2b"
echo "Try a sandbox: ./azure/ssh.sh hibernaut/.venv/bin/python hibernaut/examples/try.py"
