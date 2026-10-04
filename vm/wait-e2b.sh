#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/e2b.sh"

if [[ "${1:-}" == --start ]]; then
  echo "==> Starting E2B"
  mapfile -t units < <(e2b_units)
  sudo systemctl start "${units[@]}"
fi
echo "==> Waiting for E2B"
wait_for_e2b "${WAIT_SECONDS:-600}"
base_template_ready || { echo "The base template is missing; run hibernaut/vm/install-e2b.sh" >&2; exit 1; }
echo "E2B is healthy. Logs: journalctl -u 'e2b-*'"
