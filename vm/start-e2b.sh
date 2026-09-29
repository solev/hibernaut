#!/usr/bin/env bash
# Starts E2B's services in a tmux session named "e2b" and makes sure the base template exists.
# Run after every VM start (azure/up.sh does). Services that are already healthy are left alone.
set -euo pipefail

E2B_INFRA_DIR="${E2B_INFRA_DIR:-$HOME/infra}"
LOG_DIR="${LOG_DIR:-$HOME/e2b-logs}"
# E2B's fixed local-development key from DEV-LOCAL.md. The Azure firewall only exposes SSH.
E2B_API_KEY="e2b_53ae1fed82754c17ad8077fbc8bcdd90"
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin:/snap/bin"

# Right after install-e2b.sh, the docker group isn't active in this login yet.
if [[ -z "${HIBERNAUT_SG:-}" ]] && ! docker info >/dev/null 2>&1; then
  export HIBERNAUT_SG=1
  exec sg docker -c "$(printf '%q ' "$0" "$@")"
fi

cd "$E2B_INFRA_DIR"
mkdir -p "$LOG_DIR"

infra_up() {
  local services
  services=$(docker compose -f packages/local-dev/docker-compose.yaml ps --status running --services 2>/dev/null)
  grep -qx postgres <<<"$services" && grep -qx clickhouse <<<"$services" && grep -qx redis <<<"$services"
}
# The API returns 503 until an orchestrator registers, so at first any HTTP answer counts.
api_answers() { [[ "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:3000/health)" != 000 ]]; }
api_healthy() { curl -sf -o /dev/null http://localhost:3000/health; }
orchestrator_healthy() { curl -sf -o /dev/null http://localhost:5008/health; }
proxy_healthy() { curl -sf -o /dev/null http://localhost:3003/health; }
base_template_ready() {
  curl -sf -H "X-API-Key: $E2B_API_KEY" http://localhost:3000/templates \
    | jq -e '.[] | select((.aliases | index("base")) and .buildStatus == "ready")' >/dev/null
}

wait_until() { # name, check function, timeout seconds
  local name=$1 check=$2 timeout=$3 waited=0
  until "$check"; do
    if (( waited >= timeout )); then
      echo "$name not ready after ${timeout}s. Last lines of $LOG_DIR/$name.log:" >&2
      tail -20 "$LOG_DIR/$name.log" >&2 || true
      exit 1
    fi
    sleep 5
    waited=$(( waited + 5 ))
  done
  echo "    $name ready"
}

start_in_tmux() { # name, check function, timeout seconds, command
  local name=$1 check=$2 timeout=$3 command=$4
  echo "==> $name"
  if "$check"; then
    echo "    already running"
    return
  fi
  tmux kill-window -t "e2b:$name" 2>/dev/null || true
  tmux new-window -d -t e2b -n "$name" -c "$E2B_INFRA_DIR"
  tmux send-keys -t "e2b:$name" "export PATH=\"$PATH\"; { $command; } 2>&1 | tee $LOG_DIR/$name.log" Enter
  wait_until "$name" "$check" "$timeout"
}

run_logged() {
  "$@" >>"$LOG_DIR/setup.log" 2>&1 || { echo "Failed: $*" >&2; tail -20 "$LOG_DIR/setup.log" >&2; return 1; }
}

tmux has-session -t e2b 2>/dev/null || tmux new-session -d -s e2b -n shell -c "$E2B_INFRA_DIR"

start_in_tmux infra infra_up 300 "make local-infra"

echo "==> Database migrations and local seed data (idempotent)"
: >"$LOG_DIR/setup.log"
run_logged make -C packages/db migrate-local
# ClickHouse accepts connections a little before it can run migrations.
for attempt in 1 2 3 4 5; do
  run_logged make -C packages/clickhouse migrate-local && break
  (( attempt == 5 )) && exit 1
  sleep 10
done
run_logged make -C packages/local-dev seed-database

start_in_tmux api api_answers 600 "make -C packages/api run-local"
start_in_tmux orchestrator orchestrator_healthy 900 \
  "make -C packages/orchestrator build-debug && sudo env PATH=\"\$PATH\" make -C packages/orchestrator run-local"
start_in_tmux proxy proxy_healthy 600 "make -C packages/client-proxy run-local"
wait_until api api_healthy 120

echo "==> base template"
if base_template_ready; then
  echo "    already built"
else
  # npm_config_yes stops `npx tsx` from waiting for a y/n answer.
  npm_config_yes=true make -C packages/shared/scripts local-build-base-template 2>&1 | tee "$LOG_DIR/template.log" || true
  base_template_ready || { echo "Base template build failed; see $LOG_DIR/template.log" >&2; exit 1; }
  echo "    built"
fi

echo
echo "E2B is running. Logs: $LOG_DIR. Watch: tmux attach -t e2b"
