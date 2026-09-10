#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build a native tipsy-*.rpm from the current source tree by reusing
# scripts/install-desktop.sh (DESTDIR staging) plus rpmbuild.
#
# Usage:
#   packaging/rpm/build-rpm.sh --version 1.2.3 --output-dir dist [--mode developer|official]
#
# --mode official (GitHub Actions only) marks the package release-repository-signed
# so a root-owned install from the GPG-signed repository runs OfficialVerified;
# the default developer build stays development-unrestricted.
#
# Fixed Release 1 keeps the filename deterministic
# (tipsy-<version>-1.x86_64.rpm). Fedora-style Requires on system Qt/X11/EGL.
set -euo pipefail

fail() { printf 'build-rpm: %s\n' "$*" >&2; exit 1; }

version=
output_dir=
mode=developer
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || fail 'missing --version value'; version=$2; shift 2 ;;
    --output-dir) [[ $# -ge 2 ]] || fail 'missing --output-dir value'; output_dir=$2; shift 2 ;;
    --mode) [[ $# -ge 2 ]] || fail 'missing --mode value'; mode=$2; shift 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+~]*$ && "$version" != *-* ]] || \
  fail 'invalid version (RPM Version must not contain a hyphen; use the upstream tag without v)'
[[ -n "$output_dir" ]] || fail '--output-dir is required'
[[ "$mode" == developer || "$mode" == official ]] || fail 'mode must be developer or official'
release_kind=development-unrestricted
[[ "$mode" != official ]] || release_kind=release-repository-signed
command -v rpmbuild >/dev/null 2>&1 || fail 'rpmbuild is missing'
command -v go >/dev/null 2>&1 || fail 'go is missing'

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
rpm_name="tipsy-${version}-1.x86_64.rpm"
[[ ! -e "$output_dir/$rpm_name" ]] || fail "output already exists: $output_dir/$rpm_name"

work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-rpm.XXXXXXXX")
cleanup() {
  if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-rpm.* ]]; then
    find "$work" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$work/rpmbuild"/{BUILD,RPMS,SOURCES,SPECS,SRPMS}
stage="$work/stage"
mkdir -p "$stage"
DESTDIR="$stage" PREFIX=/usr VERSION="$version" CHANNEL=stable RELEASE_KIND="$release_kind" MEDIUM=rpm "$repo/scripts/install-desktop.sh"

cat > "$work/rpmbuild/SPECS/tipsy.spec" <<EOF
# The Go binaries are already stripped (-s -w): no debuginfo subpackage, or
# rpmbuild fails on an empty debugfiles.list / picks the wrong output RPM.
%global debug_package %{nil}
%global _build_id_links none

Name:           tipsy
Version:        $version
Release:        1
Summary:        Run the official Roblox Android client on Linux
License:        GPL-3.0-or-later
URL:            https://github.com/32bitx64bit/Tipsy
BuildArch:      x86_64
Requires:       qt6-qtbase-gui, libX11, libX11-xcb, libxcb, libXext, libXrandr, libXtst, libXi, mesa-libEGL, mesa-libGLES, pango, cairo, pulseaudio-libs
Provides:       tipsy = %{version}-%{release}

%description
Tipsy is an open-source Linux compatibility runtime for the official
unmodified Roblox Android x86-64 client. Roblox itself is not included
and must be installed through the in-app setup assistant.

%install
cp -a "$stage"/* %{buildroot}/

%post
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications || true
fi

%files
%defattr(-,root,root,-)
/usr/bin/tipsy
/usr/bin/tipsy-gui
/usr/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop
/usr/share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop
/usr/share/icons/hicolor/512x512/apps/tipsy.png
/usr/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml
%dir /usr/share/tipsy
/usr/share/tipsy/build-info.json
%doc /usr/share/licenses/tipsy/LICENSE
%doc /usr/share/licenses/tipsy/NOTICE

%changelog
* $(date -u '+%a %b %d %Y') Tipsy Contributors <32bitx64bit@users.noreply.github.com> - $version-1
- Release $version.
EOF

rpmbuild --define "_topdir $work/rpmbuild" -bb "$work/rpmbuild/SPECS/tipsy.spec"
mapfile -t built < <(find "$work/rpmbuild/RPMS" -name '*.rpm')
[[ ${#built[@]} -eq 1 && -f "${built[0]}" ]] || fail "rpmbuild produced ${#built[@]} RPMs, expected exactly one"
mv -- "${built[0]}" "$output_dir/$rpm_name"

# Listing captured once, not piped into `grep -q` (EPIPE + pipefail false failure).
rpm -qpi "$output_dir/$rpm_name" >/dev/null || fail 'built RPM is unreadable'
files=$(rpm -qpl "$output_dir/$rpm_name") || fail 'built RPM cannot be listed'
grep -q '^/usr/bin/tipsy$' <<<"$files" || fail 'rpm is missing /usr/bin/tipsy'
grep -q '^/usr/bin/tipsy-gui$' <<<"$files" || fail 'rpm is missing /usr/bin/tipsy-gui'
grep -q '^/usr/share/tipsy/build-info.json$' <<<"$files" || fail 'rpm is missing build-info.json (release identity)'

printf 'RPM: %s\n' "$output_dir/$rpm_name"
