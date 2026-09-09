#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Install the native headers and Qt 6 surface required to vet, test, and
# build tipsy and tipsy-gui. Supports Debian/Ubuntu apt and Fedora dnf.
set -euo pipefail

fail() {
	printf 'ci-install-native: %s\n' "$*" >&2
	exit 1
}

as_root() {
	if [ "$(id -u)" -eq 0 ]; then
		"$@"
	else
		sudo "$@"
	fi
}

pkg_modules=(
	x11 x11-xcb xrandr xext xi xtst
	pangocairo pangoft2 cairo-xlib
	egl glesv2
	libpulse libpulse-simple
	Qt6Widgets Qt6Gui Qt6Core
)

if command -v apt-get >/dev/null 2>&1; then
	export DEBIAN_FRONTEND=noninteractive
	as_root apt-get update
	as_root apt-get install -y --no-install-recommends \
		ca-certificates curl gcc g++ git pkg-config xvfb \
		libx11-dev libx11-xcb-dev libxext-dev libxrandr-dev libxtst-dev libxi-dev libxcb1-dev \
		libegl1-mesa-dev libgles2-mesa-dev \
		libpango1.0-dev libpulse-dev \
		qt6-base-dev
elif command -v dnf >/dev/null 2>&1; then
	as_root dnf install -y --setopt=install_weak_deps=False \
		ca-certificates curl gcc gcc-c++ git pkgconf-pkg-config \
		xorg-x11-server-Xvfb \
		libX11-devel libXext-devel libXrandr-devel libXtst-devel libXi-devel libxcb-devel \
		mesa-libEGL-devel mesa-libGLES-devel \
		pango-devel cairo-devel pulseaudio-libs-devel \
		qt6-qtbase-devel
else
	fail 'need apt-get or dnf'
fi

pkg-config --exists "${pkg_modules[@]}" || fail 'pkg-config modules missing'
test -f /usr/include/X11/Xlib-xcb.h || fail 'missing Xlib-xcb.h'
test -f /usr/include/pulse/simple.h || fail 'missing pulse/simple.h'
test -f /usr/include/pulse/error.h || fail 'missing pulse/error.h'
command -v Xvfb >/dev/null 2>&1 || fail 'Xvfb is not on PATH'
command -v gcc >/dev/null 2>&1 || fail 'gcc is not on PATH'
