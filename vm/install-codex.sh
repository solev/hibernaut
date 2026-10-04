#!/bin/bash
# Installs the host's pinned Codex. CI uses the same installer.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
versions="${1:-$here/../images/dev/versions}"
bin_dir="${2:-/usr/local/bin}"
[[ -r "$versions" ]] || {
	echo "missing codex release pins: deploy images/dev/versions with vm, then run make deploy again" >&2
	exit 1
}
# shellcheck source=/dev/null
source "$versions"

install -d -m 755 "$bin_dir"
bin_dir="$(cd "$bin_dir" && pwd)"
lib_dir="$(dirname "$bin_dir")/lib"
install -d -m 755 "$lib_dir"
codex_dir="$lib_dir/codex-$CODEX_VERSION"
stage="$lib_dir/.codex-install-$$-${RANDOM}-${RANDOM}"
codex_link="$bin_dir/.codex-link-$$-${RANDOM}-${RANDOM}"
mkdir -m 700 "$stage"
trap 'rm -rf "$stage"; rm -f "$codex_link"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -m 700 "$stage/home"
dest="$bin_dir/codex"
if [[ -L "$dest" && "$(readlink "$dest")" == "$codex_dir/bin/codex" &&
	-x "$codex_dir/bin/codex" && -x "$codex_dir/bin/codex-code-mode-host" &&
	-f "$codex_dir/codex-package.json" ]] &&
	version="$(env -i HOME="$stage/home" CODEX_HOME="$stage/home" PATH=/usr/bin:/bin LANG=C.UTF-8 \
		"$dest" --version 2>/dev/null)" &&
	[[ "$version" == "codex-cli $CODEX_VERSION" ]]; then
	exit 0
fi

echo "==> Installing Codex $CODEX_VERSION"
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL --retry 3 \
	-o "$stage/codex.tar.gz" \
	"https://github.com/openai/codex/releases/download/rust-v$CODEX_VERSION/codex-package-x86_64-unknown-linux-musl.tar.gz"
printf '%s  %s\n' "$CODEX_SHA256" "$stage/codex.tar.gz" | sha256sum -c -
mkdir -m 755 "$stage/codex"
tar -xzf "$stage/codex.tar.gz" -C "$stage/codex" --no-same-owner
[[ -x "$stage/codex/bin/codex" && -x "$stage/codex/bin/codex-code-mode-host" &&
	-f "$stage/codex/codex-package.json" ]] || {
	echo "incomplete codex package: download the pinned canonical package" >&2
	exit 1
}
if [[ -e "$codex_dir" || -L "$codex_dir" ]]; then
	mv -T "$codex_dir" "$stage/previous-codex"
fi
mv -T "$stage/codex" "$codex_dir"
ln -s "$codex_dir/bin/codex" "$codex_link"
mv -T "$codex_link" "$dest"
for old in "$lib_dir"/codex-*; do
	if [[ "$old" != "$codex_dir" && -d "$old" ]]; then
		rm -rf -- "$old"
	fi
done
