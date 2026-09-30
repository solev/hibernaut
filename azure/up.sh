#!/usr/bin/env bash
# Makes the VM ready to use: starts it if needed, deploys this checkout (azure/deploy.sh),
# installs or updates E2B when this version isn't installed yet, starts E2B if it's stopped, and
# waits until it's healthy.
# Safe to run repeatedly.
set -euo pipefail
source "$(dirname "$0")/common.sh"

"$PROJECT_ROOT/azure/deploy.sh"
ssh "$VM_USER@$(vm_ip)" 'hibernaut/vm/install-e2b.sh --if-needed && hibernaut/vm/wait-e2b.sh --start'

echo
echo "Shell:         ./azure/ssh.sh"
echo "E2B status:    ./azure/ssh.sh systemctl status 'e2b-*'"
echo "Try a sandbox: ./azure/ssh.sh hibernaut/.venv/bin/python hibernaut/examples/try.py"
