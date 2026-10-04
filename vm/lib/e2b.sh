# shellcheck shell=bash

# E2B's fixed local-development key from DEV-LOCAL.md.
E2B_API_KEY="e2b_53ae1fed82754c17ad8077fbc8bcdd90"
E2B_UNITS=(e2b-infra e2b-api e2b-orchestrator e2b-client-proxy)

e2b_units() {
  printf '%s\n' "${E2B_UNITS[@]}"
  [[ ! -f /etc/systemd/system/hibd-pause-all.service ]] || echo hibd-pause-all
}

e2b_healthy() {
  local unit
  for unit in $(e2b_units); do
    systemctl is-active -q "$unit" || return 1
  done
  curl -sf -o /dev/null http://127.0.0.1:3000/health &&
    curl -sf -o /dev/null http://127.0.0.1:5008/health &&
    curl -sf -o /dev/null http://127.0.0.1:3003/health
}

base_template_ready() {
  curl -sf -H "X-API-Key: $E2B_API_KEY" http://127.0.0.1:3000/templates |
    jq -e '.[] | select((.aliases | index("base")) and .buildStatus == "ready")' >/dev/null
}

wait_for_e2b() {
  local deadline=$((SECONDS + $1)) unit
  until e2b_healthy; do
    if ((SECONDS >= deadline)); then
      echo "E2B isn't healthy after $1 s:" >&2
      for unit in $(e2b_units); do
        printf '  %-18s %s\n' "$unit" "$(systemctl is-active "$unit")" >&2
      done
      echo "Logs: journalctl -u 'e2b-*' -b" >&2
      return 1
    fi
    sleep 3
  done
}
