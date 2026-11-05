#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail

fail() { printf 'pgo-merge: %s\n' "$*" >&2; exit 1; }

usage() {
	printf 'usage: %s [--gui|--cli] NDS_CAPTURE_DIR\n' "$0" >&2
	printf '  --gui  install the profile for cmd/tipsy-gui (the default)\n' >&2
	printf '  --cli  install the profile for cmd/tipsy\n' >&2
	exit 2
}

target=${TIPSY_PGO_TARGET:-}
target_explicit=0
[[ -z "$target" ]] || target_explicit=1
capture_dir=
while [[ $# -gt 0 ]]; do
	case "$1" in
		--gui) target=gui; target_explicit=1; shift ;;
		--cli) target=cli; target_explicit=1; shift ;;
		--target) [[ $# -ge 2 ]] || usage; target=$2; target_explicit=1; shift 2 ;;
		--target=*) target=${1#--target=}; target_explicit=1; shift ;;
		-h|--help) usage ;;
		gui|cli)
			if [[ -z "$capture_dir" ]]; then
				capture_dir=$1
			else
				target=$1
				target_explicit=1
			fi
			shift
			;;
		-*) fail "unknown argument: $1" ;;
		*) [[ -z "$capture_dir" ]] || fail "unexpected argument: $1"; capture_dir=$1; shift ;;
	esac
done
[[ -n "$capture_dir" ]] || usage
if [[ -n "$target" ]]; then
	case "$target" in
		gui|cli) ;;
		*) fail "PGO target must be gui or cli (got $target)" ;;
	esac
fi
[[ -d "$capture_dir" ]] || fail "capture directory does not exist: $capture_dir"
capture_dir=$(CDPATH= cd -- "$capture_dir" && pwd -P)

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
command -v go >/dev/null 2>&1 || fail 'go is not on PATH'

umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-pgo-merge.XXXXXXXX")
cleanup() {
	if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-pgo-merge.* ]]; then
		find "$work" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

# The capture directory also holds heap, alloc, goroutine, threadcreate, mutex
# and block dumps. Merging one of those with a CPU profile makes pprof reject
# the whole set, so select by the profile's own period type, not its name.
profiles=()
skipped=0
while IFS= read -r -d '' profile; do
	raw="$work/profile-raw.txt"
	if go tool pprof -raw "$profile" >"$raw" 2>/dev/null &&
		IFS= read -r first <"$raw" &&
		[[ "$first" == "PeriodType: cpu "* ]]; then
		profiles+=("$profile")
	else
		skipped=$((skipped + 1))
	fi
done < <(find "$capture_dir" -type f \( -name '*.prof' -o -name '*.prof.gz' -o -name '*.pprof' -o -name '*.pprof.gz' -o -name '*.pb.gz' \) -print0 | sort -z)
(( ${#profiles[@]} > 0 )) || fail "no CPU profiles found in $capture_dir (heap/goroutine/mutex dumps are not merged)"
if (( skipped > 0 )); then
	printf 'pgo-merge: skipped %d non-CPU profile(s)\n' "$skipped" >&2
fi

# A CPU profile records the executable that produced it. GUI gameplay runs in
# the in-process tipsy-gui session, while a CLI capture comes from tipsy, so an
# unspecified target follows the recorded binary and falls back to the GUI.
detect_pgo_target() {
	local candidate mapping
	for candidate in "${profiles[@]}"; do
		go tool pprof -raw "$candidate" >"$work/profile-map.txt" 2>/dev/null || continue
		while IFS= read -r mapping; do
			case "$mapping" in
				*tipsy-gui*) printf 'gui\n'; return 0 ;;
				*/tipsy\ *|*/tipsy) printf 'cli\n'; return 0 ;;
			esac
		done < <(sed -n '/^Mappings$/,$p' "$work/profile-map.txt")
	done
	printf 'gui\n'
}

target=${target:-gui}
if (( target_explicit == 0 )); then
	target=$(detect_pgo_target)
fi
if [[ -n "${TIPSY_PGO_OUTPUT:-}" ]]; then
	# TIPSY_PGO_OUTPUT lets the script's own test write to a temporary path.
	output=$TIPSY_PGO_OUTPUT
elif [[ "$target" == cli ]]; then
	output="$repo/cmd/tipsy/default.pgo"
else
	output="$repo/cmd/tipsy-gui/default.pgo"
fi
[[ ! -e "$output" ]] || fail "refusing to replace existing profile: $output (move it first)"

go tool pprof -proto -output="$work/default.pgo" "${profiles[@]}"
[[ -s "$work/default.pgo" ]] || fail 'pprof did not produce a non-empty profile'
mv -n -- "$work/default.pgo" "$output"
[[ ! -e "$work/default.pgo" ]] || fail "profile was not installed; output may already exist: $output"

cat <<EOF
Merged ${#profiles[@]} CPU profile(s) into:
  $output

Review the profile before distributing it. Keep it only if the NDS capture
represents expected gameplay workloads. Commit the reviewed profile (for
example, git add cmd/tipsy-gui/default.pgo) so release, AppImage, and Flatpak
builds pick it up automatically. A build without the file simply omits PGO.
EOF
