#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail

usage() {
	cat >&2 <<USAGE
usage: $0 --version VERSION --output-dir DIRECTORY [--mode developer|official|github-signed]
  [--release-lock FILE] [--appimagetool FILE --appimagetool-sha256 SHA256
   --runtime-file FILE --runtime-sha256 SHA256]

Builds the same candidate twice with pre-fetched dependencies, an empty
credential environment, and network-dependent Go tooling disabled. It requires
byte-identical final artifacts, emits deterministic unsigned P1 evidence, and
never replaces an existing output.
USAGE
	exit 2
}

fail() {
	printf 'release-build: %s\n' "$*" >&2
	exit 1
}

version=
output_dir=
mode=developer
release_lock=
appimagetool=
appimagetool_sha256=
runtime_file=
runtime_sha256=
while [[ $# -gt 0 ]]; do
	case "$1" in
		--version) [[ $# -ge 2 ]] || usage; version=$2; shift 2 ;;
		--output-dir) [[ $# -ge 2 ]] || usage; output_dir=$2; shift 2 ;;
		--mode) [[ $# -ge 2 ]] || usage; mode=$2; shift 2 ;;
		--release-lock) [[ $# -ge 2 ]] || usage; release_lock=$2; shift 2 ;;
		--appimagetool) [[ $# -ge 2 ]] || usage; appimagetool=$2; shift 2 ;;
		--appimagetool-sha256) [[ $# -ge 2 ]] || usage; appimagetool_sha256=$2; shift 2 ;;
		--runtime-file) [[ $# -ge 2 ]] || usage; runtime_file=$2; shift 2 ;;
		--runtime-sha256) [[ $# -ge 2 ]] || usage; runtime_sha256=$2; shift 2 ;;
		-h|--help) usage ;;
		*) usage ;;
	esac
done

[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]] || fail 'invalid version'
[[ -n "$output_dir" ]] || usage
[[ "$mode" == developer || "$mode" == official || "$mode" == github-signed ]] || fail 'mode must be developer, official, or github-signed'
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
if [[ -z "$release_lock" ]]; then
	release_lock="$repo/scripts/release-inputs.lock.json"
fi
[[ -f "$release_lock" && ! -L "$release_lock" ]] || fail 'release input lock is not a regular file'
release_lock=$(readlink -f -- "$release_lock")
"$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode"

with_appimage=false
if [[ -n "$appimagetool" || -n "$appimagetool_sha256" || -n "$runtime_file" || -n "$runtime_sha256" ]]; then
	[[ -n "$appimagetool" && -n "$appimagetool_sha256" && -n "$runtime_file" && -n "$runtime_sha256" ]] || \
		fail 'AppImage tool, runtime, and both digests must be supplied together'
	with_appimage=true
	[[ -f "$appimagetool" && ! -L "$appimagetool" ]] || fail 'appimagetool is not a regular file'
	[[ -f "$runtime_file" && ! -L "$runtime_file" ]] || fail 'type-2 runtime is not a regular file'
	appimagetool=$(readlink -f -- "$appimagetool")
	runtime_file=$(readlink -f -- "$runtime_file")
fi

source_commit=$(git -C "$repo" rev-parse HEAD 2>/dev/null) || fail 'source tree has no reviewed Git commit'
[[ "$source_commit" =~ ^[0-9a-f]{40}$ ]] || fail 'source HEAD is not a full Git commit'
source_date_epoch=$(git -C "$repo" show -s --format=%ct "$source_commit" 2>/dev/null)
[[ "$source_date_epoch" =~ ^[0-9]+$ ]] || fail 'source commit has no valid timestamp'
# Source admission must happen before this script creates an output directory
# or a candidate artifact. It audits HEAD plus every reachable historical ref.
"$repo/scripts/check-public-source.sh" --repo "$repo" --commit "$source_commit"

mkdir -p -- "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd -P)
appdir_name="Tipsy-${version}-x86_64.AppDir"
archive_name="${appdir_name}.tar.gz"
appimage_name="Tipsy-${version}-x86_64.AppImage"
evidence_name="Tipsy-${version}-x86_64.evidence"
for output_name in "$appdir_name" "$archive_name" "$evidence_name"; do
	[[ ! -e "$output_dir/$output_name" ]] || fail "output already exists: $output_dir/$output_name"
done
if [[ "$with_appimage" == true ]]; then
	[[ ! -e "$output_dir/$appimage_name" ]] || fail "output already exists: $output_dir/$appimage_name"
fi

module_cache=$(go env GOMODCACHE)
[[ -d "$module_cache" ]] || fail 'the read-only Go module cache is missing'
go_command=$(command -v go) || fail 'the active Go toolchain is missing'
go_command=$(readlink -f -- "$go_command")
[[ -f "$go_command" && -x "$go_command" ]] || fail 'the active Go command is not an executable regular file'
go_bin_dir=$(dirname -- "$go_command")

work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-release-build.XXXXXXXX")
cleanup() {
	if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-release-build.* ]]; then
		find "$work" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM
mkdir -m 0700 "$work/a" "$work/b" "$work/home"

# GitHub-hosted runners prohibit the user and network namespace operations
# Bubblewrap needs. Do not fall back to another privileged namespace launcher.
# Instead, this process is deliberately unprivileged and has an empty
# environment: no GitHub token, OIDC request endpoint, proxy, credential helper,
# or caller configuration reaches source-controlled build commands. The build
# job itself has only contents:read; signing and release publication happen in a
# separate job after this process has exited. No service-manager launcher is
# invoked here.
run_reproducible() {
	env -i \
		HOME="$work/home" \
		PATH="$go_bin_dir:/usr/local/bin:/usr/bin:/bin" \
		LC_ALL=C.UTF-8 \
		TZ=UTC \
		SOURCE_DATE_EPOCH="$source_date_epoch" \
		GOCACHE="$work/go-cache" \
		GOMODCACHE="$module_cache" \
		GOTOOLCHAIN=local \
		GOPROXY=off \
		GOSUMDB=off \
		GONOPROXY='*' \
		GONOSUMDB='*' \
		"$@"
}

assert_clean_source() {
	[[ -z $(git -C "$repo" status --porcelain=v1 --untracked-files=all) ]] || fail 'release build modified the checked-out source tree'
	git -C "$repo" diff --no-ext-diff --quiet || fail 'release build modified tracked source content'
	git -C "$repo" diff --cached --no-ext-diff --quiet || fail 'release build modified staged source content'
}

assert_clean_source

for pass in a b; do
	pass_output="$work/$pass"
	run_reproducible \
		"$repo/scripts/build-appdir.sh" \
		--version "$version" \
		--output-dir "$pass_output" \
		--mode "$mode" \
		--release-lock "$release_lock" \
		--source-commit "$source_commit"
done

if ! cmp -s -- "$work/a/$archive_name" "$work/b/$archive_name"; then
	sha256sum "$work/a/$archive_name" "$work/b/$archive_name" >&2
	fail 'two isolated AppDir archive builds are not byte-identical'
fi

if [[ "$with_appimage" == true ]]; then
	for pass in a b; do
		pass_appdir="$work/$pass/$appdir_name"
		pass_output="$work/$pass/$appimage_name"
		pass_tool=$appimagetool
		pass_runtime=$runtime_file
		run_reproducible \
			"$repo/scripts/build-appimage.sh" \
			--appdir "$pass_appdir" \
			--version "$version" \
			--tool "$pass_tool" \
			--tool-sha256 "$appimagetool_sha256" \
			--runtime-file "$pass_runtime" \
			--runtime-sha256 "$runtime_sha256" \
			--output "$pass_output" \
			--mode "$mode" \
			--release-lock "$release_lock"
	done
	if ! cmp -s -- "$work/a/$appimage_name" "$work/b/$appimage_name"; then
		sha256sum "$work/a/$appimage_name" "$work/b/$appimage_name" >&2
		fail 'two isolated AppImage builds are not byte-identical'
	fi
fi

# A clean admission happens before output creation and is rechecked after all
# source-controlled build commands have completed.
assert_clean_source
"$repo/scripts/check-release-tree.sh" "$work/a/$appdir_name"
mv -- "$work/a/$appdir_name" "$output_dir/$appdir_name"
mv -- "$work/a/$archive_name" "$output_dir/$archive_name"
artifacts=(--artifact "$output_dir/$archive_name")
if [[ "$with_appimage" == true ]]; then
	mv -- "$work/a/$appimage_name" "$output_dir/$appimage_name"
	artifacts+=(--artifact "$output_dir/$appimage_name")
fi
"$repo/scripts/release-evidence.py" evidence \
	--version "$version" \
	--source-commit "$source_commit" \
	--source-date-epoch "$source_date_epoch" \
	--release-lock "$release_lock" \
	--mode "$mode" \
	--appdir "$output_dir/$appdir_name" \
	"${artifacts[@]}" \
	--output-dir "$output_dir/$evidence_name"

printf 'Reproduced AppDir: %s\n' "$output_dir/$appdir_name"
printf 'Reproduced archive: %s\n' "$output_dir/$archive_name"
if [[ "$with_appimage" == true ]]; then
	printf 'Reproduced AppImage: %s\n' "$output_dir/$appimage_name"
fi
printf 'Unsigned deterministic evidence: %s\n' "$output_dir/$evidence_name"
case "$mode" in
	developer) integrity_label=development-unrestricted ;;
	official) integrity_label=release-candidate-unsigned ;;
	github-signed) integrity_label=release-candidate-keyless ;;
esac
printf 'Integrity label: %s\n' "$integrity_label"
