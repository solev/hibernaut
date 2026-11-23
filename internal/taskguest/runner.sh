#!/bin/sh
# The hib task runner. hibd uploads one copy per run to RUN/runner and runs every subcommand as
# "sh RUN/runner SUBCOMMAND ...". Its protocol with hibd is files under RUN:
#
#   STAGE.started/      the start marker, made by mkdir, so a stage starts at most once
#   STAGE.exit          the exit record, written once by ExecStopPost from systemd's variables
#   STAGE.log           the stage's output, appended by systemd
#   start               HEAD before this run's repo command, written once
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
	command)
		if [ -n "${HIB_REPO:-}" ]; then record_start "$run"; fi
		exec "$@"
		;;
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
	if properties=$(systemctl show "$unit" -p LoadState -p ActiveState -p SubState -p ControlGroup); then
		printf '%s\n' "$properties"
	else
		echo show=failed
		properties=
	fi
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

require_git() {
	command -v git >/dev/null 2>&1 || die "Git 2.23 or newer is required"
	version=$(git --version 2>/dev/null) || die "Git 2.23 or newer is required"
	version=${version#git version }
	major=${version%%.*}
	minor=${version#*.}
	minor=${minor%%.*}
	case "$major:$minor" in
	*[!0-9:]*|:*|*:) die "Git 2.23 or newer is required" ;;
	esac
	if [ "$major" -lt 2 ] || { [ "$major" -eq 2 ] && [ "$minor" -lt 23 ]; }; then
		die "Git 2.23 or newer is required"
	fi
	credential_key=credential.https://github.com.helper
	IFS= read -r credential_helper <<'HELPER'
!f() { if [ "$1" = get ] && [ -n "${GH_TOKEN:-}" ]; then printf "%s\n" username=x-access-token "password=$GH_TOKEN"; fi; }; f
HELPER
	export GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false
	export GIT_TRACE=0 GIT_TRACE_CURL=0 GIT_CURL_VERBOSE=0
}

repo_git() {
	git -c credential.helper= -c "$credential_key=$credential_helper" \
		-c core.askPass=/bin/false -c core.hooksPath=/dev/null \
		-c commit.gpgsign=false -c user.useConfigOnly=true \
		-c http.lowSpeedLimit=1024 -c http.lowSpeedTime=30 "$@"
}

valid_commit() {
	[ "${#1}" -eq 40 ] || return 1
	case $1 in *[!0-9a-f]*) return 1 ;; esac
}

record_start() {
	require_git
	sha=$(repo_git -C "$HIB_WORKDIR" rev-parse --verify 'HEAD^{commit}' 2>/dev/null) ||
		die "cannot record the command starting commit"
	valid_commit "$sha" || die "command starting commit is not a SHA-1 commit"
	tmp="$1/start.$$"
	printf '%s\n' "$sha" >"$tmp"
	if ! ln "$tmp" "$1/start" 2>/dev/null; then
		rm -f "$tmp"
		die "cannot write the command starting commit once"
	fi
	rm -f "$tmp"
}

publication_exit() {
	status=$1
	trap - 0
	case $status in 75|76) status=2 ;; esac
	tmp="$run/publish.$$"
	if ! printf 'changes=%s\ncommit=%s\npushed=%s\nauthor=%s\nstart=%s\n' \
		"$changes" "$commit" "$pushed" "$author" "$start" >"$tmp" ||
		! mv -f "$tmp" "$run/publish"; then
		rm -f "$tmp"
		die "cannot write publication"
	fi
	exit "$status"
}

read_head() {
	head=$(repo_git -C "$HIB_WORKDIR" rev-parse --verify 'HEAD^{commit}' 2>/dev/null) ||
		die "cannot read task HEAD"
	valid_commit "$head" || die "task HEAD is not a SHA-1 commit"
	author=$(repo_git -C "$HIB_WORKDIR" show -s --format='%an <%ae>' HEAD 2>/dev/null) ||
		die "cannot read HEAD author"
	author=$(printf '%s' "$author" | tr -d '\r\n' | head -c 256)
	if [ -n "${GH_TOKEN:-}" ]; then
		case $author in
		*"$GH_TOKEN"*) author=; die "HEAD author contains a credential" ;;
		esac
	fi
}

setup() {
	require_git
	base=${HIB_BASE:-}
	case $base in -*) die "invalid base $base" ;; esac
	if [ -n "$base" ]; then
		if ! repo_git check-ref-format --branch "$base" >/dev/null 2>&1 ||
			! repo_git check-ref-format "refs/heads/$base" >/dev/null 2>&1; then
			die "invalid base $base"
		fi
	fi
	repo_git check-ref-format "refs/heads/$HIB_BRANCH" >/dev/null 2>&1 ||
		die "invalid task branch $HIB_BRANCH"
	url="https://github.com/$HIB_REPO"
	if repo_git ls-remote --exit-code --heads "$url" "refs/heads/$HIB_BRANCH" >/dev/null 2>&1; then
		die "remote branch $HIB_BRANCH already exists"
	else
		status=$?
		[ "$status" -eq 2 ] || die "cannot check remote branch $HIB_BRANCH"
	fi
	printf 'clone %s\n' "$HIB_REPO"
	repo_git clone --quiet --no-progress --filter=blob:none --no-checkout \
		-c credential.helper= -c "$credential_key=$credential_helper" \
		-c core.askPass=/bin/false "$url" "$HIB_WORKDIR" >/dev/null 2>&1 ||
		die "cannot clone $HIB_REPO"
	first=$(repo_git -C "$HIB_WORKDIR" rev-list --all --max-count=1 2>/dev/null) ||
		die "cannot read cloned repository"
	[ -n "$first" ] || die "repository $HIB_REPO is empty"
	ref=refs/remotes/origin/HEAD
	if [ -n "$base" ]; then ref="refs/remotes/origin/$base"; fi
	sha=$(repo_git -C "$HIB_WORKDIR" rev-parse --verify "$ref^{commit}" 2>/dev/null) ||
		die "base ${base:-origin/HEAD} does not exist"
	repo_git -C "$HIB_WORKDIR" switch --quiet -c "$HIB_BRANCH" "$sha" >/dev/null 2>&1 ||
		die "cannot create task branch $HIB_BRANCH"
	printf 'clone base %s commit %s branch %s\n' "${base:-origin/HEAD}" "$sha" "$HIB_BRANCH"
}

finish() {
	run=$1
	changes=false commit='' pushed=false author='' start=''
	trap 'publication_exit "$?"' 0
	require_git
	start=$(cat "$run/start" 2>/dev/null) || die "command starting commit is missing"
	if ! valid_commit "$start"; then
		start=
		die "command starting commit is invalid"
	fi
	case ${HIB_PUBLISH:-} in
	draft-pr|none) ;;
	*) die "publish policy must be draft-pr or none" ;;
	esac
	repo_git check-ref-format "refs/heads/$HIB_BRANCH" >/dev/null 2>&1 ||
		die "invalid task branch $HIB_BRANCH"
	read_head
	if [ "$head" != "$start" ]; then changes=true; commit=$head; fi
	repo_git -C "$HIB_WORKDIR" add -A >/dev/null 2>&1 || die "cannot stage task work"
	if repo_git -C "$HIB_WORKDIR" diff --cached --quiet --exit-code >/dev/null 2>&1; then
		:
	else
		status=$?
		[ "$status" -eq 1 ] || die "cannot inspect staged task work"
		changes=true
		repo_git -C "$HIB_WORKDIR" var GIT_AUTHOR_IDENT >/dev/null 2>&1 ||
			die "configured git identity is required to commit"
		printf 'commit task work\n'
		repo_git -C "$HIB_WORKDIR" commit --quiet -m 'hib: Save task work' >/dev/null 2>&1 ||
			die "cannot commit task work"
		read_head
	fi
	if [ "$head" != "$start" ]; then changes=true; commit=$head; fi
	count=$(repo_git -C "$HIB_WORKDIR" rev-list --count "$start..$head" 2>/dev/null) ||
		die "cannot count task commits"
	printf 'finish commits %s since %s\n' "$count" "$start"
	if [ "$changes" = false ] || [ "$HIB_PUBLISH" = none ]; then return 0; fi
	attempt=0
	for delay in 0 5 15; do
		if [ "$delay" -ne 0 ]; then sleep "$delay"; fi
		attempt=$((attempt + 1))
		printf 'push branch %s commit %s attempt %s\n' "$HIB_BRANCH" "$commit" "$attempt"
		if repo_git -C "$HIB_WORKDIR" push --quiet "https://github.com/$HIB_REPO" \
			"HEAD:refs/heads/$HIB_BRANCH" >/dev/null 2>&1; then
			pushed=true
			return 0
		fi
	done
	die "cannot push branch $HIB_BRANCH after 3 attempts"
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
