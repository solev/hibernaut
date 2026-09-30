#!/usr/bin/env bash
# Starts the VM if it isn't running and waits for SSH. Unlike up.sh it deploys and starts nothing,
# so the e2e tests can check that E2B comes up on its own after a boot.
set -euo pipefail
source "$(dirname "$0")/common.sh"

ensure_running
