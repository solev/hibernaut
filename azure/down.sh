#!/usr/bin/env bash
# Pauses every machine and stops E2B (vm/lib/quiesce), then deallocates the VM so compute billing
# stops. Disks and paused machines are kept. If machines can't be paused it stops, because
# deallocating would lose running ones; FORCE=1 deallocates anyway.
set -euo pipefail
source "$(dirname "$0")/common.sh"

if [[ "$(vm_power_state)" == "VM running" ]]; then
  echo "==> Pausing every machine and stopping E2B"
  if ! ssh "$VM_USER@$(vm_ip)" 'sudo /usr/local/lib/hibernaut/quiesce'; then
    if [[ "${FORCE:-}" != 1 ]]; then
      echo "Pausing failed, so deallocating could lose running machines. FORCE=1 ./azure/down.sh deallocates anyway." >&2
      exit 1
    fi
    echo "FORCE=1: deallocating anyway" >&2
  fi
fi
echo "==> Deallocating $VM_NAME"
az vm deallocate -g "$RESOURCE_GROUP" -n "$VM_NAME" -o none
echo "$VM_NAME: $(vm_power_state)"
