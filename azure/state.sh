#!/usr/bin/env bash
# Prints the VM's power state, such as "VM running" or "VM deallocated".
set -euo pipefail
source "$(dirname "$0")/common.sh"

vm_power_state
