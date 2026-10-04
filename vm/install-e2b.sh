#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
source "$here/lib/e2b.sh"
E2B_INFRA_DIR="${E2B_INFRA_DIR:-$HOME/infra}"
E2B_INFRA_REF="${E2B_INFRA_REF:-5cfcf3282ddb95e90a8214ce71329641bdfe6cb6}"
GO_VERSION="${GO_VERSION:-1.26.8}"
VENV_DIR="${VENV_DIR:-$HOME/hibernaut/.venv}"
MARKER="$HOME/.hibernaut-installed"
LOG_DIR="${LOG_DIR:-$HOME/e2b-logs}"
export DEBIAN_FRONTEND=noninteractive
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin:/snap/bin"

install_id="$E2B_INFRA_REF $(cat "$here/install-e2b.sh" "$here"/lib/e2b* "$here"/systemd/e2b-*.service \
  "$here"/e2b/* | sha256sum | cut -c1-16)"
if [[ "${1:-}" == --if-needed && "$(cat "$MARKER" 2>/dev/null)" == "$install_id" ]]; then
  echo "E2B is installed ($install_id)"
  exit 0
fi

log() { printf '==> %s\n' "$*"; }
mkdir -p "$LOG_DIR"
: >"$LOG_DIR/install.log"
run_logged() {
  "$@" >>"$LOG_DIR/install.log" 2>&1 || { echo "Failed: $*" >&2; tail -20 "$LOG_DIR/install.log" >&2; return 1; }
}

[[ -e /dev/kvm ]] || { echo "/dev/kvm is missing: this VM size has no nested virtualization" >&2; exit 1; }

log "Base packages"
echo 'DPkg::Lock::Timeout "600";' | sudo tee /etc/apt/apt.conf.d/90hibernaut-lock-timeout >/dev/null
echo "\$nrconf{restart} = 'a';" | sudo tee /etc/needrestart/conf.d/90-auto.conf >/dev/null
sudo apt-get update -qq
sudo apt-get install -y -qq git make build-essential gettext-base jq unzip rsync python3-venv >/dev/null

log "Go $GO_VERSION"
if ! go version 2>/dev/null | grep -q "go$GO_VERSION "; then
  curl -fsSL "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf /tmp/go.tgz
  rm /tmp/go.tgz
  # shellcheck disable=SC2016 # expanded at login, not now
  echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' | sudo tee /etc/profile.d/go.sh >/dev/null
fi

log "Docker"
command -v docker >/dev/null || curl -fsSL https://get.docker.com | sudo sh >/dev/null
sudo usermod -aG docker "$USER"

log "Node.js 22"
if ! node --version 2>/dev/null | grep -q '^v22\.'; then
  curl -fsSL https://deb.nodesource.com/setup_22.x | sudo bash - >/dev/null
  sudo apt-get install -y -qq nodejs >/dev/null
fi

log "Google Cloud CLI (gsutil, for E2B's public artifact bucket)"
command -v gsutil >/dev/null || sudo snap install google-cloud-cli --classic >/dev/null

log "Kernel prep: nbd module and 3200 huge pages, persisted across reboots"
echo nbd | sudo tee /etc/modules-load.d/nbd.conf >/dev/null
echo "options nbd nbds_max=64" | sudo tee /etc/modprobe.d/nbd.conf >/dev/null
echo "vm.nr_hugepages=3200" | sudo tee /etc/sysctl.d/99-hugepages.conf >/dev/null
sudo modprobe nbd nbds_max=64
sudo sysctl -q -w vm.nr_hugepages=3200
hugepages=$(cat /proc/sys/vm/nr_hugepages)
((hugepages >= 3200)) || echo "    only $hugepages huge pages until the next boot: memory is too fragmented now" >&2

log "e2b-dev/infra at $E2B_INFRA_REF"
[[ -d "$E2B_INFRA_DIR/.git" ]] || git clone -q https://github.com/e2b-dev/infra.git "$E2B_INFRA_DIR"
git -C "$E2B_INFRA_DIR" fetch -q origin
git -C "$E2B_INFRA_DIR" -c advice.detachedHead=false checkout -q "$E2B_INFRA_REF"
cd "$E2B_INFRA_DIR"

log "Prebuilt Firecracker binaries, guest kernels and busybox"
[[ -n "$(ls -A packages/fc-kernels 2>/dev/null)" ]] || make -s download-public-kernels
[[ -n "$(ls -A packages/fc-versions/builds 2>/dev/null)" ]] || make -s download-public-firecrackers
# Template builds need busybox, but DEV-LOCAL.md never downloads it.
if [[ ! -x packages/orchestrator/.busybox/1.36.1/amd64/busybox ]]; then
  mkdir -p packages/orchestrator/.busybox
  gsutil -q -m cp -r "gs://e2b-artifact-binaries/busybox/*" packages/orchestrator/.busybox/
  find packages/orchestrator/.busybox -type f -exec chmod 755 {} +
fi

if systemctl is-active -q e2b-orchestrator; then
  log "Pausing every machine, then stopping E2B"
  sudo "$here/lib/quiesce"
fi

log "Building envd, and optimized api, orchestrator and client-proxy"
run_logged make -C packages/envd build
for pkg in api orchestrator client-proxy; do
  run_logged make -C "packages/$pkg" build-debug RACE_FLAG= DEBUG_GCFLAGS=
done
mkdir -p packages/orchestrator/.data/test-volume

log "Storage in /var/lib/e2b"
if [[ ! -e /var/lib/e2b ]]; then
  if sudo test -d packages/orchestrator/tmp; then
    sudo mv packages/orchestrator/tmp /var/lib/e2b
  else
    sudo mkdir -m 700 /var/lib/e2b
  fi
fi

log "systemd units"
for unit in "${E2B_UNITS[@]}"; do
  sed -e "s|@E2B_DIR@|$E2B_INFRA_DIR|g" -e "s|@E2B_USER@|$USER|g" "$here/systemd/$unit.service" |
    sudo tee "/etc/systemd/system/$unit.service" >/dev/null
done
sudo install -D -m 644 "$here/e2b/e2b-orchestrator.env" /etc/hibernaut/e2b-orchestrator.env
sudo install -D -m 644 "$here/e2b/sandbox-firewall.nft" /etc/hibernaut/sandbox-firewall.nft
sudo install -D -m 755 "$here/lib/e2b-infra-ready" /usr/local/lib/hibernaut/e2b-infra-ready
sudo systemctl daemon-reload
sudo systemctl enable -q "${E2B_UNITS[@]}"

log "Containers, database migrations and seed data"
run_logged make -C packages/local-dev clickhouse-config-generated.xml
sudo systemctl restart e2b-infra
run_logged make -C packages/db migrate-local
for attempt in 1 2 3 4 5; do
  run_logged make -C packages/clickhouse migrate-local && break
  ((attempt == 5)) && exit 1
  sleep 10
done
run_logged make -C packages/local-dev seed-database

log "Bigger machines than the base tier's defaults: 30-day runs and 10 GiB of disk"
# E2B pauses a sandbox max_length_hours after it starts or resumes, even while hibd renews its
# timeout for a connected ssh session.
read -r hours disk < <(sudo docker compose -f packages/local-dev/docker-compose.yaml exec -T postgres \
  psql -qAt -F' ' -v ON_ERROR_STOP=1 -U postgres -d postgres \
  -c "UPDATE tiers SET max_length_hours = 720, disk_mb = 10240 WHERE id = 'base_v1' RETURNING max_length_hours, disk_mb")
[[ "$hours" == 720 && "$disk" == 10240 ]] || { echo "Couldn't raise the base tier's limits (got hours='$hours' disk='$disk')" >&2; exit 1; }

log "Starting E2B"
sudo systemctl start e2b-api e2b-orchestrator e2b-client-proxy
[[ ! -f /etc/systemd/system/hibd-pause-all.service ]] || sudo systemctl start hibd-pause-all
wait_for_e2b 900

log "Base template"
if base_template_ready; then
  echo "    already built"
else
  # npm_config_yes stops `npx tsx` from waiting for a y/n answer.
  npm_config_yes=true make -C packages/shared/scripts local-build-base-template >"$LOG_DIR/template.log" 2>&1 || true
  base_template_ready || { echo "Base template build failed; see $LOG_DIR/template.log" >&2; exit 1; }
  echo "    built"
fi

log "Python SDK for the examples ($VENV_DIR)"
[[ -d "$VENV_DIR" ]] || python3 -m venv "$VENV_DIR"
"$VENV_DIR/bin/pip" install -q --upgrade e2b

echo "$install_id" >"$MARKER"
log "Installed. E2B runs under systemd: systemctl status 'e2b-*'"
