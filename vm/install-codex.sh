#!/bin/bash
# Installs the host's pinned Codex. CI uses the same installer.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
versions="${1:-$here/../images/dev/versions}"
bin_dir="${2:-/usr/local/bin}"
# shellcheck source=/dev/null
source "$versions"

install -d -m 755 "$bin_dir"
stage="$bin_dir/.codex-install-$$-${RANDOM}-${RANDOM}"
mkdir -m 700 "$stage"
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -m 700 "$stage/home"
dest="$bin_dir/codex"
if [[ -x "$dest" ]] &&
	version="$(env -i HOME="$stage/home" CODEX_HOME="$stage/home" PATH=/usr/bin:/bin LANG=C.UTF-8 \
		"$dest" --version 2>/dev/null)" &&
	[[ "$version" == "codex-cli $CODEX_VERSION" ]]; then
	exit 0
fi

echo "==> Installing Codex $CODEX_VERSION"
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --retry 3 \
	-o "$stage/codex.tar.gz" \
	"https://github.com/openai/codex/releases/download/rust-v$CODEX_VERSION/codex-x86_64-unknown-linux-musl.tar.gz"
printf '%s  %s\n' "$CODEX_SHA256" "$stage/codex.tar.gz" | sha256sum -c -
tar -xzf "$stage/codex.tar.gz" -C "$stage" --no-same-owner codex-x86_64-unknown-linux-musl
install -m 755 "$stage/codex-x86_64-unknown-linux-musl" "$stage/codex"
mv -f "$stage/codex" "$dest"
