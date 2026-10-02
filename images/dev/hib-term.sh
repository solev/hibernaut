# shellcheck shell=sh
# Sourced by login shells. ssh passes on the laptop's TERM, and with no terminfo entry for it
# here, clear, nano, tmux and the like fail. The image has entries for the common terminals; for
# any other, fall back to xterm-256color, which nearly every terminal understands.
if [ -n "${TERM-}" ] && ! infocmp "$TERM" >/dev/null 2>&1; then
	TERM=xterm-256color
	export TERM
fi
