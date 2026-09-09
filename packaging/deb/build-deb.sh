#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build a native tipsy_*.deb from the current source tree by reusing
# scripts/install-desktop.sh (DESTDIR staging) plus dpkg-deb.
#
# Usage:
#   packaging/deb/build-deb.sh --version 1.2.3 --output-dir dist
#
# The .deb contains only Tipsy files and Depends on system Qt/X11/EGL/Pulse
# libraries; nothing is bundled. No source outside the checked-out tree is
# used. Debian arch is amd64 (Tipsy targets Linux x86_64 only).
set -euo pipefail

fail() { printf 'build-deb: %s\n' "$*" >&2; exit 1; }

version=
output_dir=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || fail 'missing --version value'; version=$2; shift 2 ;;
    --output-dir) [[ $# -ge 2 ]] || fail 'missing --output-dir value'; output_dir=$2; shift 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+~-]*$ ]] || fail 'invalid version (must be Debian-safe)'
[[ -n "$output_dir" ]] || fail '--output-dir is required'
command -v dpkg-deb >/dev/null 2>&1 || fail 'dpkg-deb is missing'
command -v go >/dev/null 2>&1 || fail 'go is missing'

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
maintainer=${MAINTAINER:-"Tipsy Contributors <32bitx64bit@users.noreply.github.com>"}
deb_arch=amd64
deb_name="tipsy_${version}_${deb_arch}.deb"

mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
[[ ! -e "$output_dir/$deb_name" ]] || fail "output already exists: $output_dir/$deb_name"

work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-deb.XXXXXXXX")
cleanup() {
  if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-deb.* ]]; then
    find "$work" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

stage="$work/stage"
mkdir -p "$stage"

# Reuse the canonical install layout: binaries, desktop files, icons,
# metainfo, licenses under PREFIX with DESTDIR staging.
DESTDIR="$stage" PREFIX=/usr VERSION="$version" "$repo/scripts/install-desktop.sh"

# Remove the per-user URI registration side effects if any leaked into stage
# (install-desktop.sh only touches the user session when DESTDIR is empty).
mkdir -p "$stage/DEBIAN"
installed_size=$(du -sk "$stage/usr" | cut -f1)
cat > "$stage/DEBIAN/control" <<EOF
Package: tipsy
Version: $version
Section: games
Priority: optional
Architecture: $deb_arch
Maintainer: $maintainer
Description: Run the official Roblox Android client on Linux
 Tipsy is an open-source Linux compatibility runtime for the official
 unmodified Roblox Android x86-64 client. Roblox itself is not included
 and must be installed through the in-app setup assistant.
Depends: libqt6core6 | libqt6core6t64, libqt6gui6 | libqt6gui6t64, libqt6widgets6 | libqt6widgets6t64, libx11-6, libxext6, libxrandr2, libxtst6, libxi6, libegl1, libgles2, libpango-1.0-0, libcairo2, libpulse0
Homepage: https://github.com/32bitx64bit/Tipsy
Installed-Size: $installed_size
EOF
cat > "$stage/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications || true
fi
#DEBHELPER#
exit 0
EOF
chmod 0755 "$stage/DEBIAN/postinst"

dpkg-deb --root-owner-group --build "$stage" "$work/$deb_name"
mv -- "$work/$deb_name" "$output_dir/$deb_name"

# Sanity: package opens, ships both binaries and both desktop files.
dpkg-deb --contents "$output_dir/$deb_name" | grep -q 'usr/bin/tipsy$' || fail 'deb is missing usr/bin/tipsy'
dpkg-deb --contents "$output_dir/$deb_name" | grep -q 'usr/bin/tipsy-gui$' || fail 'deb is missing usr/bin/tipsy-gui'
dpkg-deb --contents "$output_dir/$deb_name" | grep -q 'Tipsy.Play.desktop' || fail 'deb is missing the Play desktop file'
if command -v lintian >/dev/null 2>&1; then
  lintian --no-tag-display-limit "$output_dir/$deb_name" || \
    printf 'build-deb: WARNING: lintian reported issues\n' >&2
fi

printf 'DEB: %s\n' "$output_dir/$deb_name"
