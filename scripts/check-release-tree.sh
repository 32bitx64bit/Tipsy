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
[[ -d "$appdir" && ! -L "$appdir" ]] || usage
appdir=$(CDPATH= cd -- "$appdir" && pwd -P)

for command_name in file find getcap readelf sha256sum stat; do
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
	usr/share/tipsy/build-info.json
	usr/share/tipsy/manifest.sha256
)
for relative in "${required[@]}"; do
	[[ -f "$appdir/$relative" && ! -L "$appdir/$relative" ]] || fail "required regular file is missing: $relative"
done
[[ -x "$appdir/AppRun" ]] || fail 'AppRun is not executable'
[[ -x "$appdir/usr/bin/tipsy" ]] || fail 'tipsy is not executable'
[[ -x "$appdir/usr/bin/tipsy-gui" ]] || fail 'tipsy-gui is not executable'

is_allowed_executable() {
	case "$1" in
		AppRun|usr/bin/tipsy|usr/bin/tipsy-gui|usr/lib/*.so|usr/lib/*.so.*|usr/plugins/*.so|usr/plugins/*/*.so|usr/plugins/*/*.so.*)
			return 0
			;;
	esac
	return 1
}

is_allowed_host_library() {
	case "$1" in
		ld-linux-*.so.*|libc.so.*|libdl.so.*|libm.so.*|libpthread.so.*|libresolv.so.*|librt.so.*|libutil.so.*|libanl.so.*|libnss_*.so.*|libsystemd.so.*|libEGL.so.*|libGL.so.*|libGLX.so.*|libGLdispatch.so.*|libOpenGL.so.*|libGLES*.so.*|libvulkan.so.*|libdrm.so.*|libgbm.so.*|libglapi.so.*)
			return 0
			;;
	esac
	return 1
}

while IFS= read -r -d '' entry; do
	relative=${entry#"$appdir"/}
	case "$relative" in
		AppRun|.DirIcon|io.github.tipsy_linux.Tipsy.Play.desktop|tipsy.png|usr|usr/*)
			;;
		*)
			fail "unexpected top-level content: $relative"
			;;
	esac

	# sha256sum manifests cannot represent control characters safely, and no
	# shipped Tipsy path needs whitespace. A narrow alphabet also closes path
	# confusion between archive, desktop, and verifier implementations.
	[[ "$relative" =~ ^[A-Za-z0-9._+/@:-]+$ ]] || fail "unsafe release path spelling: $relative"
	lower=${relative,,}
	case "$lower" in
		.git|.git/*|*/.git|*/.git/*|.tipsy-private|.tipsy-private/*|*/.tipsy-private|*/.tipsy-private/*|docs|docs/*|*/docs|*/docs/*|appdata|appdata/*|*/appdata|*/appdata/*|app-data|app-data/*|*/app-data|*/app-data/*|cache|cache/*|*/cache|*/cache/*|logs|logs/*|*/logs|*/logs/*|runtime|runtime/*|*/runtime|*/runtime/*|*.apk|*.apkm|*.xapk|*.apks|*.obb|*.rbxm|*libroblox.so|*.log|*.pem|*.key|*.p12|*.pfx|*.env|*.env.*|*cookies.sqlite|*cookies.txt|*.roblosecurity*)
			fail "forbidden private or proprietary path: $relative"
			;;
	esac

	[[ ! -L "$entry" ]] || fail "symbolic links are not permitted in the release tree: $relative"
	if [[ ! -f "$entry" && ! -d "$entry" ]]; then
		fail "special filesystem entry is not permitted: $relative"
	fi

	mode=$(stat -c '%a' "$entry")
	[[ "$mode" =~ ^[0-7]{3,4}$ ]] || fail "cannot interpret mode for: $relative"
	mode_bits=$((8#$mode))
	(( (mode_bits & 0022) == 0 )) || fail "group/world-writable entry: $relative"
	(( (mode_bits & 06000) == 0 )) || fail "setuid/setgid entry: $relative"

	if [[ -f "$entry" ]]; then
		links=$(stat -c '%h' "$entry")
		[[ "$links" == 1 ]] || fail "hard-linked file is not permitted: $relative"
		capabilities=$(getcap -n -- "$entry" 2>/dev/null || true)
		[[ -z "$capabilities" ]] || fail "file capabilities are not permitted: $relative"
		if [[ -x "$entry" ]] && ! is_allowed_executable "$relative"; then
			fail "unexpected executable file: $relative"
		fi
	fi
done < <(find "$appdir" -mindepth 1 -print0)

# These are credential/private material indicators, not a generic source-code
# scanner. Search data/script files, not binaries that legitimately contain
# redaction labels or authentication API names.
secret_file=$(while IFS= read -r -d '' candidate; do
	file_kind=$(file -Lb -- "$candidate")
	case "$file_kind" in
		*text*|*script*|*JSON*|*XML*|*private\ key*|*PEM*|empty)
			if LC_ALL=C grep -aEq \
				-e '-----BEGIN ([A-Z0-9]+ )?PRIVATE KEY-----' \
				-e '\.ROBLOSECURITY' \
				-e 'AWS_SECRET_ACCESS_KEY[[:space:]]*=' \
				-e 'RBXAuthenticationNegotiation[[:space:]]*:' \
				-- "$candidate"; then
				printf '%s\n' "${candidate#"$appdir"/}"
				break
			fi
			;;
	esac
done < <(find "$appdir" -type f -print0))
[[ -z "$secret_file" ]] || fail "release payload contains a private-key, credential, or authentication-ticket marker: $secret_file"

while IFS= read -r -d '' candidate; do
	relative=${candidate#"$appdir"/}
	file_kind=$(file -Lb -- "$candidate")
	case "$file_kind" in
		*Zip\ archive*)
			# Renaming an APK must not bypass the path denylist. Ordinary ZIP
			# payloads are not part of the current AppDir format either.
			fail "unexpected ZIP/APK-like payload: $relative"
			;;
	esac
done < <(find "$appdir" -type f -print0)

while IFS= read -r -d '' elf; do
	relative=${elf#"$appdir"/}
	if ! readelf -h "$elf" | grep -q 'Machine:.*Advanced Micro Devices X86-64'; then
		fail "non-x86-64 ELF object: $relative"
	fi

	runpath=$(readelf -d "$elf" 2>/dev/null | sed -n 's/.*\(RPATH\|RUNPATH\).*\[\(.*\)\]/\2/p')
	case "$relative" in
		usr/bin/*) expected_runpath='$ORIGIN/../lib' ;;
		usr/lib/*) expected_runpath='$ORIGIN' ;;
		usr/plugins/*) expected_runpath='$ORIGIN/../../lib' ;;
		*) fail "ELF object is outside an executable payload directory: $relative" ;;
	esac
	[[ "$runpath" == "$expected_runpath" ]] || fail "unsafe or missing ELF RPATH/RUNPATH in $relative (expected $expected_runpath)"

	while IFS= read -r needed; do
		[[ -n "$needed" ]] || continue
		if [[ -f "$appdir/usr/lib/$needed" ]] || is_allowed_host_library "$needed"; then
			continue
		fi
		fail "unresolved or unapproved dynamic dependency in $relative: $needed"
	done < <(readelf -d "$elf" 2>/dev/null | sed -n 's/.*NEEDED.*\[\(.*\)\]/\1/p')
done < <(find "$appdir" -type f -print0 | while IFS= read -r -d '' candidate; do
	if file -Lb -- "$candidate" | grep -q '^ELF '; then
		printf '%s\0' "$candidate"
	fi
done)

manifest="$appdir/usr/share/tipsy/manifest.sha256"
declare -A manifest_paths=()
manifest_count=0
while IFS= read -r line || [[ -n "$line" ]]; do
	[[ "$line" =~ ^([0-9a-f]{64})[[:space:]][[:space:]]([A-Za-z0-9._+/@:-]+)$ ]] || fail 'malformed or non-canonical manifest entry'
	expected=${BASH_REMATCH[1]}
	relative=${BASH_REMATCH[2]}
	[[ "$relative" != /* && "$relative" != ./* && "$relative" != */../* && "$relative" != ../* && "$relative" != */.. ]] || fail "unsafe manifest path: $relative"
	[[ "$relative" != usr/share/tipsy/manifest.sha256 ]] || fail 'manifest must not contain itself'
	[[ -z "${manifest_paths[$relative]:-}" ]] || fail "duplicate manifest path: $relative"
	[[ -f "$appdir/$relative" && ! -L "$appdir/$relative" ]] || fail "manifest target is missing: $relative"
	actual=$(sha256sum "$appdir/$relative")
	actual=${actual%% *}
	[[ "$actual" == "$expected" ]] || fail "manifest digest mismatch: $relative"
	manifest_paths[$relative]=1
	manifest_count=$((manifest_count + 1))
done < "$manifest"
file_count=$(find "$appdir" -type f ! -path "$manifest" | wc -l)
[[ $manifest_count -eq $file_count ]] || fail "manifest covers $manifest_count of $file_count files"

while IFS= read -r -d '' payload; do
	relative=${payload#"$appdir"/}
	[[ "$relative" == usr/share/tipsy/manifest.sha256 || -n "${manifest_paths[$relative]:-}" ]] || fail "unmanifested file: $relative"
done < <(find "$appdir" -type f -print0)

printf 'release-tree guard: OK (%s)\n' "$appdir"
