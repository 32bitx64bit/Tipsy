#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Full package vet, test suite, and CLI/GUI build. Starts a private Xvfb so
# X11 tests that only skip when DISPLAY is unset actually run.
#
# mode: "full" (default) runs vet, the complete test suite with the race
# passes, and builds both mains. "smoke" only builds both mains, for the
# secondary CI legs that must still prove a CGO install works.
set -euo pipefail

mode="${1:-full}"

fail() {
	printf 'ci-test-build: %s\n' "$*" >&2
	exit 1
}

case "$mode" in
full | smoke) ;;
*) fail "unknown mode '$mode': expected full or smoke" ;;
esac

command -v go >/dev/null 2>&1 || fail 'go is not on PATH'
command -v gcc >/dev/null 2>&1 || fail 'gcc is not on PATH'
command -v Xvfb >/dev/null 2>&1 || fail 'Xvfb is not on PATH'

export CGO_ENABLED="${CGO_ENABLED:-1}"
export GOAMD64="${GOAMD64:-v3}"
export QT_QPA_PLATFORM="${QT_QPA_PLATFORM:-offscreen}"
if [ -d /tmp/tipsy-ci-pkgconfig ]; then
	export PKG_CONFIG_PATH="/tmp/tipsy-ci-pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
fi

display_num=99
export DISPLAY=":${display_num}"
Xvfb "$DISPLAY" -screen 0 1280x720x24 -nolisten tcp >/tmp/tipsy-xvfb.log 2>&1 &
xvfb_pid=$!
cleanup() {
	kill "$xvfb_pid" >/dev/null 2>&1 || true
}
trap cleanup EXIT

socket="/tmp/.X11-unix/X${display_num}"
for _ in $(seq 1 50); do
	if [ -S "$socket" ]; then
		break
	fi
	sleep 0.1
done
[ -S "$socket" ] || fail 'Xvfb did not create a display socket'

mkdir -p bin
if [ "$mode" = full ]; then
	# unsafeptr is disabled because the JNI bindings deliberately pack integers
	# into C pointer types (unsafe.Pointer(uintptr(x))); checkptr/UB coverage of
	# those conversions remains in the race passes below.
	go vet -unsafeptr=false ./...
	# Plain pass: every package except those exercised by the race passes
	# below, so no package is compiled and run twice in the same job.
	mod="$(go list -m)"
	plain_pkgs=()
	while IFS= read -r pkg; do
		case "${pkg#"$mod"/}" in
		internal/android | internal/config | internal/loader | internal/x11 | internal/jni | internal/runtime) ;;
		*) plain_pkgs+=("$pkg") ;;
		esac
	done < <(go list ./...)
	[ "${#plain_pkgs[@]}" -gt 0 ] || fail 'plain test pass has no packages'
	go test -count=1 -timeout 20m "${plain_pkgs[@]}"
	# Race pass for the packages that own shared concurrency state.
	# internal/jni and internal/runtime are the deliberate integer-packed-pointer
	# packages: checkptr's instrumentation aborts on those conversions, so their
	# race pass disables only checkptr diagnostics (not the race detector), and
	# only for those two packages so no dependency loses checkptr coverage.
	go test -race -count=1 -timeout 10m ./internal/android ./internal/config ./internal/loader ./internal/x11
	go test -race -count=1 -timeout 10m \
		-gcflags="${mod}/internal/jni=-d=checkptr=0" \
		-gcflags="${mod}/internal/runtime=-d=checkptr=0" \
		./internal/jni ./internal/runtime
fi
go build -o bin/tipsy ./cmd/tipsy
go build -o bin/tipsy-gui ./cmd/tipsy-gui
test -x bin/tipsy || fail 'CLI binary was not built'
test -x bin/tipsy-gui || fail 'GUI binary was not built'
