# Shared settings for the azure/ scripts. Override any of them with environment variables.
# shellcheck shell=bash

RESOURCE_GROUP="${RESOURCE_GROUP:-rg-e2b-test}"
VM_NAME="${VM_NAME:-e2b-test}"
LOCATION="${LOCATION:-swedencentral}"
VM_SIZE="${VM_SIZE:-Standard_D4s_v5}"
VM_USER="${VM_USER:-azureuser}"
SSH_PUBLIC_KEY="${SSH_PUBLIC_KEY:-$HOME/.ssh/id_ed25519.pub}"
SHUTDOWN_UTC="${SHUTDOWN_UTC:-2000}"

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

vm_power_state() {
  az vm get-instance-view -g "$RESOURCE_GROUP" -n "$VM_NAME" \
    --query "instanceView.statuses[?starts_with(code,'PowerState')].displayStatus | [0]" -o tsv
}

vm_ip() {
  az vm show -d -g "$RESOURCE_GROUP" -n "$VM_NAME" --query publicIps -o tsv
}

wait_for_ssh() {
  local ip=$1
  for _ in $(seq 1 30); do
    ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new "$VM_USER@$ip" true 2>/dev/null && return 0
    sleep 5
  done
  echo "SSH to $ip did not come up" >&2
  return 1
}

sync_to_vm() {
  local ip=$1
  rsync -az --delete "$PROJECT_ROOT/vm" "$PROJECT_ROOT/examples" "$VM_USER@$ip:hibernaut/"
}
