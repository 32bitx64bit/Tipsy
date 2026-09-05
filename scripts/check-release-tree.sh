#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail

usage() {
	printf 'usage: %s <Tipsy.AppDir>\n' "$0" >&2
	exit 2
}

fail() {
	printf 'release-tree guard: %s\n' "$*" >&2
	exit 1
}

[[ $# -eq 1 ]] || usage
appdir=$1
[[ -d "$appdir" ]] || usage
appdir=$(CDPATH= cd -- "$appdir" && pwd)

for command_name in file find ldd readelf sha256sum; do
	command -v "$command_name" >/dev/null 2>&1 || fail "required command is missing: $command_name"
done

required=(
	AppRun
	.DirIcon
	io.github.tipsy_linux.Tipsy.Play.desktop
	tipsy.png
	usr/bin/tipsy
	usr/bin/tipsy-gui
	usr/bin/qt.conf
	usr/plugins/platforms/libqoffscreen.so
	usr/plugins/platforms/libqxcb.so
	usr/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop
	usr/share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop
	usr/share/icons/hicolor/512x512/apps/tipsy.png
	usr/share/licenses/tipsy/LICENSE
	usr/share/licenses/tipsy/NOTICE
	usr/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml
	usr/share/tipsy/build-info
	usr/share/tipsy/manifest.sha256
)
for relative in "${required[@]}"; do
	[[ -f "$appdir/$relative" ]] || fail "required file is missing: $relative"
done
[[ -x "$appdir/AppRun" ]] || fail 'AppRun is not executable'
[[ -x "$appdir/usr/bin/tipsy" ]] || fail 'tipsy is not executable'
[[ -x "$appdir/usr/bin/tipsy-gui" ]] || fail 'tipsy-gui is not executable'

while IFS= read -r -d '' entry; do
	relative=${entry#"$appdir"/}
	case "$relative" in
		AppRun|.DirIcon|io.github.tipsy_linux.Tipsy.Play.desktop|tipsy.png|usr|usr/*)
			;;
		*)
			fail "unexpected top-level content: $relative"
			;;
	esac

	lower=${relative,,}
	case "$lower" in
		.git|.git/*|*/.git|*/.git/*|.tipsy-private|.tipsy-private/*|*/.tipsy-private|*/.tipsy-private/*|docs|docs/*|*/docs|*/docs/*|appdata|appdata/*|*/appdata|*/appdata/*|app-data|app-data/*|*/app-data|*/app-data/*|cache|cache/*|*/cache|*/cache/*|logs|logs/*|*/logs|*/logs/*|runtime|runtime/*|*/runtime|*/runtime/*|*.apk|*.apkm|*.xapk|*.apks|*.obb|*.rbxm|*libroblox.so|*.log|*.pem|*.key|*.p12|*.pfx|*.env|*.env.*|*cookies.sqlite|*cookies.txt|*.roblosecurity*)
			fail "forbidden private or proprietary path: $relative"
			;;
	esac

	[[ ! -L "$entry" ]] || fail "symbolic links are not permitted in the release tree: $relative"
	if [[ -e "$entry" ]]; then
		mode=$(stat -c '%a' "$entry")
		other_digit=${mode: -1}
		(( (other_digit & 2) == 0 )) || fail "world-writable entry: $relative"
	fi
done < <(find "$appdir" -mindepth 1 -print0)

while IFS= read -r -d '' elf; do
	relative=${elf#"$appdir"/}
	if ! readelf -h "$elf" | grep -q 'Machine:.*Advanced Micro Devices X86-64'; then
		fail "non-x86-64 ELF object: $relative"
	fi
	if readelf -d "$elf" 2>/dev/null | grep -E 'RPATH|RUNPATH' | grep -E '/home/|/tmp/|/usr/lib' >/dev/null; then
		fail "host path leaked into ELF RPATH: $relative"
	fi
	ldd_output=$(LD_LIBRARY_PATH="$appdir/usr/lib" ldd "$elf" 2>&1) || fail "ldd failed for $relative"
	if grep -q 'not found' <<<"$ldd_output"; then
		printf '%s\n' "$ldd_output" >&2
		fail "unresolved dynamic dependency: $relative"
	fi
done < <(find "$appdir" -type f -print0 | while IFS= read -r -d '' candidate; do
	if file -Lb "$candidate" | grep -q '^ELF '; then
		printf '%s\0' "$candidate"
	fi
done)

manifest="$appdir/usr/share/tipsy/manifest.sha256"
declare -A manifest_paths=()
manifest_count=0
while read -r expected relative; do
	[[ -n "$expected" && -n "$relative" ]] || fail 'malformed manifest entry'
	relative=${relative#\*}
	[[ "$relative" != /* && "$relative" != *'..'* ]] || fail "unsafe manifest path: $relative"
	[[ -z "${manifest_paths[$relative]:-}" ]] || fail "duplicate manifest path: $relative"
	[[ -f "$appdir/$relative" ]] || fail "manifest target is missing: $relative"
	actual=$(sha256sum "$appdir/$relative")
	actual=${actual%% *}
	[[ "$actual" == "$expected" ]] || fail "manifest digest mismatch: $relative"
	manifest_paths[$relative]=1
	manifest_count=$((manifest_count + 1))
done < "$manifest"
file_count=$(find "$appdir" -type f ! -path "$manifest" | wc -l)
[[ $manifest_count -eq $file_count ]] || fail "manifest covers $manifest_count of $file_count files"

printf 'release-tree guard: OK (%s)\n' "$appdir"
