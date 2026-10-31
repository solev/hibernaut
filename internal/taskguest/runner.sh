#!/bin/sh
# The hib task runner. hibd uploads one copy per run to RUN/runner and runs every subcommand as
# "sh RUN/runner SUBCOMMAND ...". Its protocol with hibd is files under RUN:
#
#   STAGE.started/      the start marker, made by mkdir, so a stage starts at most once
#   STAGE.exit          the exit record, written once by ExecStopPost from systemd's variables
#   STAGE.log           the stage's output, appended by systemd
#   publish             what finish published
#   in/                 the uploaded inputs and their manifest
set -eu

EXIT_FROZEN=76

die() {
	printf 'runner: %s\n' "$*" >&2
	exit 2
}

# start RUN STAGE [-- CMD ARG...]: the unit's ExecStart. Exits 75 without doing anything when the
# stage's marker exists, so a second launch of a stage never acts.
start() {
	run=$1 stage=$2
	shift 2
	[ "${1:-}" = "--" ] && shift
	mkdir "$run/$stage.started" 2>/dev/null || exit 75
	printf '%s\n' "${INVOCATION_ID:-}" >"$run/$stage.started/invocation"
	case $stage in
	setup) setup "$run" ;;
	command) exec "$@" ;;
	finish) finish "$run" ;;
	*) die "unknown stage $stage" ;;
	esac
}

# exit RUN STAGE: the unit's ExecStopPost, run as root. It records how the stage ended once, and
# only for the invocation that made the marker, so a refused second launch can't overwrite it.
record_exit() {
	run=$1 stage=$2
	[ -d "$run/$stage.started" ] || exit 0
	inv=$(cat "$run/$stage.started/invocation" 2>/dev/null || true)
	[ "$inv" = "${INVOCATION_ID:-}" ] || exit 0
	tmp="$run/$stage.exit.$$"
	printf 'result=%s\ncode=%s\nstatus=%s\n' "${SERVICE_RESULT:-}" "${EXIT_CODE:-}" \
		"${EXIT_STATUS:-}" >"$tmp"
	ln "$tmp" "$run/$stage.exit" 2>/dev/null || true
	rm -f "$tmp"
}

# look RUN STAGE UNIT: everything hibd judges a stage by, as key=value lines.
look() {
	run=$1 stage=$2 unit=$3
	if [ -d "$run/$stage.started" ]; then echo marker=yes; else echo marker=no; fi
	if [ -f "$run/$stage.exit" ]; then sed 's/^/exit./' "$run/$stage.exit"; fi
	properties=$(systemctl show "$unit" -p LoadState -p ActiveState -p SubState -p ControlGroup)
	printf '%s\n' "$properties"
	group=$(printf '%s\n' "$properties" | sed -n 's/^ControlGroup=//p')
	if [ -n "$group" ] && [ -d "/sys/fs/cgroup$group" ]; then
		sed -n 's/^frozen /frozen=/p' "/sys/fs/cgroup$group/cgroup.events"
	fi
	if [ -r "$run/$stage.log" ]; then echo "log=$(wc -c <"$run/$stage.log" | tr -d ' ')"; fi
}

slice_cgroup() {
	group=$(systemctl show hib-task.slice -p ControlGroup --value)
	if [ -n "$group" ] && [ -d "/sys/fs/cgroup$group" ]; then
		printf '/sys/fs/cgroup%s\n' "$group"
	fi
}

freezer() {
	want=$1
	cgroup=$(slice_cgroup)
	if [ -z "$cgroup" ]; then
		[ "$want" != 1 ] || echo freeze=empty
		return 0
	fi
	printf '%s\n' "$want" >"$cgroup/cgroup.freeze"
	polls=0
	until grep -qx "frozen $want" "$cgroup/cgroup.events"; do
		[ "$polls" -lt 50 ] || die "hib-task.slice did not reach frozen $want"
		sleep 0.1
		polls=$((polls + 1))
	done
	[ "$want" != 1 ] || echo freeze=frozen
}

kill_stage() {
	systemctl kill --kill-whom=all --signal=SIGKILL "$1"
	freezer 0
}

launch() {
	cgroup=$(slice_cgroup) || die "cannot read hib-task.slice cgroup"
	if [ -n "$cgroup" ]; then
		if [ "$(cat "$cgroup/cgroup.freeze")" != 0 ] ||
			! grep -qx 'frozen 0' "$cgroup/cgroup.events"; then
			printf '%s\n' "runner: hib-task.slice is frozen; only the executor may thaw it" >&2
			exit "$EXIT_FROZEN"
		fi
	fi
	if "$@"; then
		return 0
	else
		status=$?
	fi
	[ "$status" -ne "$EXIT_FROZEN" ] || die "launch command exited reserved code $EXIT_FROZEN"
	return "$status"
}

# log RUN STAGE OFFSET MAX: at most MAX bytes of the stage's output from OFFSET, base64 on one
# line, after a line with the log's size. MAX keeps every reply far below Exec's 8 MiB.
log() {
	run=$1 stage=$2 offset=$3 max=$4
	f="$run/$stage.log"
	if [ ! -f "$f" ]; then
		echo size=0
		echo
		return
	fi
	echo "size=$(wc -c <"$f" | tr -d ' ')"
	tail -c "+$((offset + 1))" "$f" | head -c "$max" | base64 | tr -d '\n'
	echo
}

# verify RUN: checks every uploaded file against the manifest hibd wrote, then hands the run
# folder to the machine's user.
verify() {
	run=$1
	(cd "$run/in" && sha256sum -c --status .manifest) || die "an upload doesn't match its hash"
	if [ "$(id -u)" = 0 ]; then chown -R user:user "$run"; fi
}

# setup RUN: clones HIB_REPO into HIB_WORKDIR and makes HIB_BRANCH from HIB_BASE.
setup() {
	# TODO:
	#   git clone "https://github.com/$HIB_REPO" "$HIB_WORKDIR" with the token /etc/environment
	#   gives, then git switch -c "$HIB_BRANCH" "origin/${HIB_BASE:-HEAD}"
	die "setup is not implemented"
}

# finish RUN: commits what the command left, and with HIB_PUBLISH=draft-pr pushes HIB_BRANCH and
# opens a draft pull request. It writes RUN/publish whatever happens.
finish() {
	# TODO:
	#   git add -A, and commit if anything is staged, as the commit author hibd set
	#   changes is whether HIB_BRANCH differs from its base, the agent's own commits included
	#   with draft-pr and changes: git push, then look for an open PR on the branch before
	#   gh pr create --draft, so a lost reply never opens two
	#   write changes, commit, pushed and pr to RUN/publish as key=value lines
	die "finish is not implemented"
}

[ $# -ge 1 ] || die "usage: runner start|exit|look|log|verify|freeze|thaw|kill|launch ..."
cmd=$1
shift
case $cmd in
start) start "$@" ;;
exit) record_exit "$@" ;;
look) look "$@" ;;
log) log "$@" ;;
verify) verify "$@" ;;
freeze) freezer 1 ;;
thaw) freezer 0 ;;
kill) kill_stage "$@" ;;
launch) launch "$@" ;;
*) die "unknown subcommand $cmd" ;;
esac
