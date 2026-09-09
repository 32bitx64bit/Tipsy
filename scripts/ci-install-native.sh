#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Install the native headers and Qt 6 surface required to vet, test, and
# build tipsy and tipsy-gui. Supports Debian/Ubuntu apt and Fedora dnf.
# Ubuntu 22.04's qt6-base-dev 6.2.4 ships no Qt6 pkg-config files; those
# are generated from qmake6 when missing.
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

native_modules=(
	x11 x11-xcb xrandr xext xi xtst
	pangocairo pangoft2 cairo-xlib
	egl glesv2
	libpulse libpulse-simple
)
qt_modules=(Qt6Widgets Qt6Gui Qt6Core)
qt_pc_dir=/tmp/tipsy-ci-pkgconfig

missing_modules() {
	local name
	for name in "$@"; do
		if ! pkg-config --exists "$name"; then
			printf '%s\n' "$name"
		fi
	done
}

require_modules() {
	local missing
	missing=$(missing_modules "$@")
	if [ -n "$missing" ]; then
		printf 'ci-install-native: missing pkg-config modules:\n%s\n' "$missing" >&2
		return 1
	fi
	return 0
}

export_qt_pc_path() {
	export PKG_CONFIG_PATH="${qt_pc_dir}${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
	if [ -n "${GITHUB_ENV:-}" ]; then
		printf 'PKG_CONFIG_PATH=%s\n' "$PKG_CONFIG_PATH" >> "$GITHUB_ENV"
	fi
}

# Ubuntu 22.04 qt6-base 6.2.4 omits Qt6*.pc (Launchpad 2067202). MIQT still
# needs them for #cgo pkg-config: Qt6Widgets.
write_qt6_pc() {
	command -v qmake6 >/dev/null 2>&1 || fail 'qmake6 is required to synthesize Qt 6 pkg-config files'
	local headers libs version
	headers=$(qmake6 -query QT_INSTALL_HEADERS)
	libs=$(qmake6 -query QT_INSTALL_LIBS)
	version=$(qmake6 -query QT_VERSION)
	[ -d "$headers/QtWidgets" ] || fail "Qt Widgets headers missing under $headers"
	if [ ! -e "$libs/libQt6Widgets.so" ] && [ ! -e "$libs/libQt6Widgets.so.6" ]; then
		fail "libQt6Widgets missing under $libs"
	fi
	mkdir -p "$qt_pc_dir"
	cat >"$qt_pc_dir/Qt6Core.pc" <<EOF
prefix=/usr
libdir=${libs}
includedir=${headers}
Name: Qt6 Core
Description: Tipsy-generated Qt 6 Core pkg-config
Version: ${version}
Libs: -L\${libdir} -lQt6Core
Cflags: -I\${includedir}/QtCore -I\${includedir} -DQT_CORE_LIB
EOF
	cat >"$qt_pc_dir/Qt6Gui.pc" <<EOF
prefix=/usr
libdir=${libs}
includedir=${headers}
Name: Qt6 Gui
Description: Tipsy-generated Qt 6 Gui pkg-config
Version: ${version}
Libs: -L\${libdir} -lQt6Gui
Cflags: -I\${includedir}/QtGui -I\${includedir} -DQT_GUI_LIB
Requires: Qt6Core
EOF
	cat >"$qt_pc_dir/Qt6Widgets.pc" <<EOF
prefix=/usr
libdir=${libs}
includedir=${headers}
Name: Qt6 Widgets
Description: Tipsy-generated Qt 6 Widgets pkg-config
Version: ${version}
Libs: -L\${libdir} -lQt6Widgets
Cflags: -I\${includedir}/QtWidgets -I\${includedir} -DQT_WIDGETS_LIB
Requires: Qt6Core Qt6Gui
EOF
	export_qt_pc_path
}

if command -v apt-get >/dev/null 2>&1; then
	export DEBIAN_FRONTEND=noninteractive
	as_root apt-get update
	as_root apt-get install -y --no-install-recommends \
		ca-certificates curl gcc g++ git pkg-config xvfb \
		libx11-dev libx11-xcb-dev libxext-dev libxrandr-dev libxtst-dev libxi-dev libxcb1-dev \
		libegl1-mesa-dev libgles2-mesa-dev libgl1-mesa-dev libcairo2-dev \
		libpango1.0-dev libpulse-dev \
		qt6-base-dev qt6-base-dev-tools
elif command -v dnf >/dev/null 2>&1; then
	as_root dnf install -y --setopt=install_weak_deps=False \
		ca-certificates curl gcc gcc-c++ git pkgconf-pkg-config \
		xorg-x11-server-Xvfb \
		libX11-devel libXext-devel libXrandr-devel libXtst-devel libXi-devel libxcb-devel \
		mesa-libEGL-devel mesa-libGLES-devel mesa-libGL-devel \
		pango-devel cairo-devel pulseaudio-libs-devel \
		qt6-qtbase-devel
else
	fail 'need apt-get or dnf'
fi

require_modules "${native_modules[@]}" || fail 'native pkg-config modules missing'
if ! require_modules "${qt_modules[@]}"; then
	printf 'ci-install-native: synthesizing Qt 6 pkg-config from qmake6\n' >&2
	write_qt6_pc
	require_modules "${qt_modules[@]}" || fail 'Qt 6 pkg-config still missing after qmake synthesis'
fi
test -f /usr/include/X11/Xlib-xcb.h || fail 'missing Xlib-xcb.h'
test -f /usr/include/pulse/simple.h || fail 'missing pulse/simple.h'
test -f /usr/include/pulse/error.h || fail 'missing pulse/error.h'
command -v Xvfb >/dev/null 2>&1 || fail 'Xvfb is not on PATH'
command -v gcc >/dev/null 2>&1 || fail 'gcc is not on PATH'
