#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail

usage() {
	cat >&2 <<USAGE
usage: $0 --appdir DIRECTORY --version VERSION --tool APPIMAGETOOL \\
  --tool-sha256 SHA256 [--runtime-file FILE --runtime-sha256 SHA256] [--output FILE] \\
  [--mode developer|official|github-signed] [--release-lock FILE]

The appimagetool binary is never downloaded. Supply a pinned local binary and
its independently verified SHA-256 digest. If the tool would otherwise fetch an
AppImage type-2 runtime, also pass a pinned local --runtime-file.
USAGE
	exit 2
}

fail() {
	printf 'build-appimage: %s\n' "$*" >&2
	exit 1
}

appdir=
version=
tool=
tool_sha256=
runtime_file=
runtime_sha256=
output=
mode=developer
release_lock=
while [[ $# -gt 0 ]]; do
	case "$1" in
		--appdir) [[ $# -ge 2 ]] || usage; appdir=$2; shift 2 ;;
		--version) [[ $# -ge 2 ]] || usage; version=$2; shift 2 ;;
		--tool) [[ $# -ge 2 ]] || usage; tool=$2; shift 2 ;;
		--tool-sha256) [[ $# -ge 2 ]] || usage; tool_sha256=$2; shift 2 ;;
		--runtime-file) [[ $# -ge 2 ]] || usage; runtime_file=$2; shift 2 ;;
		--runtime-sha256) [[ $# -ge 2 ]] || usage; runtime_sha256=$2; shift 2 ;;
		--output) [[ $# -ge 2 ]] || usage; output=$2; shift 2 ;;
		--mode) [[ $# -ge 2 ]] || usage; mode=$2; shift 2 ;;
		--release-lock) [[ $# -ge 2 ]] || usage; release_lock=$2; shift 2 ;;
		-h|--help) usage ;;
		*) usage ;;
	esac
done

[[ -d "$appdir" ]] || fail 'AppDir does not exist'
[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]] || fail 'invalid version'
[[ "$mode" == developer || "$mode" == official || "$mode" == github-signed ]] || fail 'mode must be developer, official, or github-signed'
[[ -f "$tool" && -x "$tool" ]] || fail 'appimagetool is not an executable regular file'
[[ "$tool_sha256" =~ ^[0-9a-fA-F]{64}$ ]] || fail 'tool SHA-256 must contain exactly 64 hexadecimal characters'

actual_sha256=$(sha256sum "$tool")
actual_sha256=${actual_sha256%% *}
[[ "${actual_sha256,,}" == "${tool_sha256,,}" ]] || fail 'appimagetool SHA-256 mismatch'

runtime_args=()
if [[ -n "$runtime_file" || -n "$runtime_sha256" ]]; then
	[[ -n "$runtime_file" && -n "$runtime_sha256" ]] || fail 'runtime file and SHA-256 must be supplied together'
	[[ -f "$runtime_file" && -s "$runtime_file" ]] || fail 'AppImage runtime is not a non-empty regular file'
	[[ "$runtime_sha256" =~ ^[0-9a-fA-F]{64}$ ]] || fail 'runtime SHA-256 must contain exactly 64 hexadecimal characters'
	actual_runtime=$(sha256sum "$runtime_file")
	actual_runtime=${actual_runtime%% *}
	[[ "${actual_runtime,,}" == "${runtime_sha256,,}" ]] || fail 'AppImage runtime SHA-256 mismatch'
	runtime_args=(--runtime-file "$runtime_file")
fi

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [[ -z "$release_lock" ]]; then
	release_lock="$repo/scripts/release-inputs.lock.json"
fi
[[ -f "$release_lock" && ! -L "$release_lock" ]] || fail 'release input lock is not a regular file'
"$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode"
if [[ "$mode" != developer ]]; then
	locked_tool=$("$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode" --get downloads.appimagetool.sha256)
	[[ "$actual_sha256" == "$locked_tool" ]] || fail 'appimagetool digest does not match the reviewed release input lock'
	[[ -n "$runtime_file" ]] || fail 'signed AppImage build requires a pinned explicit type-2 runtime'
	locked_runtime=$("$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode" --get downloads.type2-runtime.sha256)
	[[ "$actual_runtime" == "$locked_runtime" ]] || fail 'runtime digest does not match the reviewed release input lock'
fi
"$repo/scripts/check-release-tree.sh" "$appdir"

if [[ -z "$output" ]]; then
	output="$(dirname -- "$appdir")/Tipsy-${version}-x86_64.AppImage"
fi
[[ ! -e "$output" ]] || fail "output already exists: $output"

source_date_epoch=${SOURCE_DATE_EPOCH:-}
if [[ -z "$source_date_epoch" ]]; then
	source_date_epoch=$(sed -n 's/^source_date_epoch=//p' "$appdir/usr/share/tipsy/build-info")
fi
[[ "$source_date_epoch" =~ ^[0-9]+$ ]] || fail 'SOURCE_DATE_EPOCH is missing or invalid'

# The pinned tool is typically itself an AppImage. Prefer extract-and-run so a
# missing FUSE mount does not look like a Tipsy packaging failure.
export APPIMAGE_EXTRACT_AND_RUN=1
ARCH=x86_64 VERSION="$version" SOURCE_DATE_EPOCH="$source_date_epoch" \
	"$tool" "${runtime_args[@]}" "$appdir" "$output"
[[ -s "$output" ]] || fail 'appimagetool did not produce an artifact'
[[ -f "$output" && ! -L "$output" && $(stat -c '%h' "$output") == 1 ]] || fail 'appimagetool output is not a single-link regular file'
chmod 0755 "$output"
printf 'AppImage: %s\n' "$output"
