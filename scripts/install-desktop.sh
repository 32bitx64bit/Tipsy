#!/bin/sh
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
set -eu

tipsy_repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tipsy_prefix=${PREFIX:-"${HOME}/.local"}
tipsy_destdir=${DESTDIR:-}
tipsy_version=${VERSION:-0.0.0-dev}
tipsy_target=${tipsy_destdir}${tipsy_prefix}

case "$tipsy_version" in
	''|*[!0-9A-Za-z._+-]*)
		printf '%s\n' "Invalid VERSION: ${tipsy_version}" >&2
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
tipsy_ldflags="-buildid= -s -w -X github.com/tipsy-linux/tipsy/internal/version.Version=${tipsy_version}"
go build -buildvcs=false -mod=readonly -trimpath -ldflags "$tipsy_ldflags" -o "${tipsy_work}/tipsy" ./cmd/tipsy
go build -buildvcs=false -mod=readonly -trimpath -ldflags "$tipsy_ldflags" -o "${tipsy_work}/tipsy-gui" ./cmd/tipsy-gui

mkdir -p \
	"${tipsy_target}/bin" \
	"${tipsy_target}/share/applications" \
	"${tipsy_target}/share/icons/hicolor/512x512/apps" \
	"${tipsy_target}/share/licenses/tipsy" \
	"${tipsy_target}/share/metainfo"

install -m 0755 "${tipsy_work}/tipsy" "${tipsy_target}/bin/tipsy"
install -m 0755 "${tipsy_work}/tipsy-gui" "${tipsy_target}/bin/tipsy-gui"
install -m 0644 tipsy.png "${tipsy_target}/share/icons/hicolor/512x512/apps/tipsy.png"
install -m 0644 share/applications/io.github.tipsy_linux.Tipsy.Play.desktop "${tipsy_target}/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop"
install -m 0644 share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop "${tipsy_target}/share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop"
install -m 0644 share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml "${tipsy_target}/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml"
install -m 0644 LICENSE "${tipsy_target}/share/licenses/tipsy/LICENSE"
install -m 0644 NOTICE "${tipsy_target}/share/licenses/tipsy/NOTICE"

printf '%s\n' "Installed Tipsy ${tipsy_version} to ${tipsy_target}."
printf '%s\n' "If ${tipsy_prefix}/bin is not on PATH, add it before launching the desktop entry."

if [ -z "${tipsy_destdir}" ] && command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database "${tipsy_target}/share/applications" >/dev/null 2>&1 || true
fi
if [ -z "${tipsy_destdir}" ] && command -v xdg-mime >/dev/null 2>&1; then
	xdg-mime default io.github.tipsy_linux.Tipsy.Play.desktop x-scheme-handler/roblox-player >/dev/null 2>&1 || true
	xdg-mime default io.github.tipsy_linux.Tipsy.Play.desktop x-scheme-handler/roblox >/dev/null 2>&1 || true
fi
