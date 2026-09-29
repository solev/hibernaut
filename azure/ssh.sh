#!/usr/bin/env bash
# Opens SSH to the VM, looking up its current IP. Extra arguments are passed to ssh.
set -euo pipefail
source "$(dirname "$0")/common.sh"

exec ssh "$VM_USER@$(vm_ip)" "$@"
