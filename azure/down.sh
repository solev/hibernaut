#!/usr/bin/env bash
# Deallocates the VM so compute billing stops. Disk contents are kept; E2B's services stop.
set -euo pipefail
source "$(dirname "$0")/common.sh"

az vm deallocate -g "$RESOURCE_GROUP" -n "$VM_NAME" -o none
echo "$VM_NAME: $(vm_power_state)"
