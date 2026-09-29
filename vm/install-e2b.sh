#!/usr/bin/env bash
# Installs E2B's open-source infrastructure (github.com/e2b-dev/infra) on this VM, following its
# DEV-LOCAL.md plus the fixes it needed in practice. Idempotent; start it with start-e2b.sh.
set -euo pipefail

E2B_INFRA_DIR="${E2B_INFRA_DIR:-$HOME/infra}"
# Pinned to the commit Hibernaut was verified against. Set E2B_INFRA_REF=main to follow upstream.
E2B_INFRA_REF="${E2B_INFRA_REF:-5cfcf3282ddb95e90a8214ce71329641bdfe6cb6}"
GO_VERSION="${GO_VERSION:-1.26.8}"
VENV_DIR="${VENV_DIR:-$HOME/hibernaut/.venv}"
export DEBIAN_FRONTEND=noninteractive
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin:/snap/bin"

log() { printf '==> %s\n' "$*"; }

[[ -e /dev/kvm ]] || { echo "/dev/kvm is missing: this VM size has no nested virtualization" >&2; exit 1; }

log "Base packages"
echo "\$nrconf{restart} = 'a';" | sudo tee /etc/needrestart/conf.d/90-auto.conf >/dev/null
sudo apt-get update -qq
sudo apt-get install -y -qq git make build-essential tmux jq unzip rsync python3-venv >/dev/null

log "Go $GO_VERSION"
if ! go version 2>/dev/null | grep -q "go$GO_VERSION "; then
  curl -fsSL "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf /tmp/go.tgz
  rm /tmp/go.tgz
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

log "Kernel prep: nbd module and 2048 huge pages, persisted across reboots"
echo nbd | sudo tee /etc/modules-load.d/nbd.conf >/dev/null
echo "options nbd nbds_max=64" | sudo tee /etc/modprobe.d/nbd.conf >/dev/null
echo "vm.nr_hugepages=2048" | sudo tee /etc/sysctl.d/99-hugepages.conf >/dev/null
sudo modprobe nbd nbds_max=64
sudo sysctl -q -w vm.nr_hugepages=2048

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

log "envd (the agent that runs inside every sandbox)"
make -s -C packages/envd build

log "Python SDK for the examples ($VENV_DIR)"
[[ -d "$VENV_DIR" ]] || python3 -m venv "$VENV_DIR"
"$VENV_DIR/bin/pip" install -q --upgrade e2b

touch "$HOME/.hibernaut-installed"
log "Installed. Next: hibernaut/vm/start-e2b.sh"
