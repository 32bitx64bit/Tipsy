#!/bin/sh
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build tipsy and tipsy-gui from this checkout and install them with their
# launcher entries into PREFIX (default ~/.local), or stage them under
# DESTDIR for packaging (deb/rpm do this with PREFIX=/usr).
#
#   VERSION       version string stamped into the binaries (default 0.0.0-dev)
#   CHANNEL       stable (default) or dev. dev installs as "Tipsy-Dev" with its
#                 own desktop-file identity, so it never replaces an installed
#                 Tipsy (Flatpak/package) in menus or as the roblox:// handler.
#   RELEASE_KIND  development-unrestricted (default) or release-repository-signed.
#                 Written to share/tipsy/build-info.json. Only GitHub Actions
#                 passes the official kind (packaging/*/build-*.sh --mode
#                 official); a local build of any medium stays development and
#                 the app asks for --development consent before launching.
#   MEDIUM        package (default), deb, rpm, or pacman — recorded in build-info.json.
#
# Launcher entries are rendered by the freshly built `tipsy desktop render`
# so every medium shares one source (share/applications must match stable).
set -eu

tipsy_repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tipsy_prefix=${PREFIX:-"${HOME}/.local"}
tipsy_destdir=${DESTDIR:-}
tipsy_version=${VERSION:-0.0.0-dev}
tipsy_channel=${CHANNEL:-stable}
tipsy_release_kind=${RELEASE_KIND:-development-unrestricted}
tipsy_medium=${MEDIUM:-package}
tipsy_target=${tipsy_destdir}${tipsy_prefix}

case "$tipsy_version" in
	''|*[!0-9A-Za-z._+-]*)
		printf '%s\n' "Invalid VERSION: ${tipsy_version}" >&2
		exit 2
		;;
esac
case "$tipsy_channel" in
	stable|dev) ;;
	*)
		printf '%s\n' "Invalid CHANNEL: ${tipsy_channel} (stable or dev)" >&2
		exit 2
		;;
esac
case "$tipsy_release_kind" in
	development-unrestricted|release-repository-signed) ;;
	*)
		printf '%s\n' "Invalid RELEASE_KIND: ${tipsy_release_kind} (development-unrestricted or release-repository-signed)" >&2
		exit 2
		;;
esac
case "$tipsy_medium" in
	package|deb|rpm|pacman) ;;
	*)
		printf '%s\n' "Invalid MEDIUM: ${tipsy_medium} (package, deb, rpm, or pacman)" >&2
		exit 2
		;;
esac

for tipsy_tool in go install mktemp; do
	if ! command -v "$tipsy_tool" >/dev/null 2>&1; then
		printf '%s\n' "Missing required command: ${tipsy_tool}" >&2
		exit 1
	fi
done

if command -v desktop-file-validate >/dev/null 2>&1; then
	desktop-file-validate "${tipsy_repo}/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop"
	desktop-file-validate "${tipsy_repo}/share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop"
fi
if command -v appstreamcli >/dev/null 2>&1; then
	appstreamcli validate --no-net "${tipsy_repo}/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml"
fi

tipsy_work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-install.XXXXXXXX")
cleanup() {
	if [ -n "${tipsy_work:-}" ] && [ -d "$tipsy_work" ]; then
		find "$tipsy_work" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

cd "${tipsy_repo}"
export GOAMD64=v2
tipsy_ldflags="-buildid= -s -w -X github.com/tipsy-linux/tipsy/internal/version.Version=${tipsy_version} -X github.com/tipsy-linux/tipsy/internal/version.Channel=${tipsy_channel}"
go build -buildvcs=false -mod=readonly -trimpath -ldflags "$tipsy_ldflags" -o "${tipsy_work}/tipsy" ./cmd/tipsy
go build -buildvcs=false -mod=readonly -trimpath -ldflags "$tipsy_ldflags" -o "${tipsy_work}/tipsy-gui" ./cmd/tipsy-gui

mkdir -p \
	"${tipsy_target}/bin" \
	"${tipsy_target}/share/applications" \
	"${tipsy_target}/share/icons/hicolor/512x512/apps" \
	"${tipsy_target}/share/licenses/tipsy" \
	"${tipsy_target}/share/metainfo" \
	"${tipsy_target}/share/tipsy"

# build-info.json is the release identity the app reads at start (see
# internal/app/official.go): a root-owned /usr install (or the Flatpak) whose
# marker says release-repository-signed is OfficialVerified; anything else
# asks for --development consent. Same tipsy.build-info.v1 document the
# AppDir carries (scripts/release-evidence.py), minus the input lock.
tipsy_source_commit=$(git -C "$tipsy_repo" rev-parse HEAD 2>/dev/null || true)
tipsy_source_tree=clean
if [ -z "$tipsy_source_commit" ] || [ -n "$(git -C "$tipsy_repo" status --porcelain=v1 --untracked-files=no 2>/dev/null)" ]; then
	tipsy_source_tree=dirty
fi
case "$tipsy_source_commit" in
	*[!0-9a-f]*|'') tipsy_source_commit=unknown ;;
esac
tipsy_build_info="${tipsy_work}/build-info.json"
printf '{"architecture":"x86_64","format":"tipsy.build-info.v1","medium":"%s","name":"Tipsy","releaseKind":"%s","source":{"commit":"%s","tree":"%s"},"version":"%s"}\n' \
	"$tipsy_medium" "$tipsy_release_kind" "$tipsy_source_commit" "$tipsy_source_tree" "$tipsy_version" > "$tipsy_build_info"
if command -v python3 >/dev/null 2>&1; then
	python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$tipsy_build_info"
fi

install -m 0755 "${tipsy_work}/tipsy" "${tipsy_target}/bin/tipsy"
install -m 0755 "${tipsy_work}/tipsy-gui" "${tipsy_target}/bin/tipsy-gui"
install -m 0644 tipsy.png "${tipsy_target}/share/icons/hicolor/512x512/apps/tipsy.png"
"${tipsy_work}/tipsy" desktop render --channel "$tipsy_channel" --out "${tipsy_work}/applications" >/dev/null
for tipsy_entry in "${tipsy_work}"/applications/*.desktop; do
	install -m 0644 "$tipsy_entry" "${tipsy_target}/share/applications/$(basename -- "$tipsy_entry")"
done
install -m 0644 share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml "${tipsy_target}/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml"
install -m 0644 LICENSE "${tipsy_target}/share/licenses/tipsy/LICENSE"
install -m 0644 NOTICE "${tipsy_target}/share/licenses/tipsy/NOTICE"
install -m 0644 "$tipsy_build_info" "${tipsy_target}/share/tipsy/build-info.json"

tipsy_name=Tipsy
[ "$tipsy_channel" = stable ] || tipsy_name=Tipsy-Dev
printf '%s\n' "Installed ${tipsy_name} ${tipsy_version} (${tipsy_release_kind}) to ${tipsy_target}."
printf '%s\n' "If ${tipsy_prefix}/bin is not on PATH, add it before launching the desktop entry."

if [ -z "${tipsy_destdir}" ] && command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database "${tipsy_target}/share/applications" >/dev/null 2>&1 || true
fi
# A direct install into the user's own prefix registers itself as the
# launcher (and, for the stable channel, the roblox:// handler) unless a
# Flatpak or distro package already provides that identity. Package staging
# and system prefixes never touch the user session; `tipsy desktop status`
# shows who provides the launcher.
case "$tipsy_destdir$tipsy_prefix" in
	"${HOME}"/*)
		"${tipsy_target}/bin/tipsy" desktop adopt --if-unowned --icon "${tipsy_repo}/tipsy.png" || true
		;;
esac
