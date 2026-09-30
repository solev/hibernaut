#!/usr/bin/env bash
# Prints the VM's SSH target (user@ip), for tools such as the e2e tests.
set -euo pipefail
source "$(dirname "$0")/common.sh"

echo "$VM_USER@$(vm_ip)"
