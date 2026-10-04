#!/usr/bin/env bash
# Starts the VM if it isn't running and waits for SSH. Unlike up.sh it deploys and starts nothing.
set -euo pipefail
source "$(dirname "$0")/common.sh"

ensure_running
