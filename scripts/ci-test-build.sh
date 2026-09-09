#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Full package vet, test suite, and CLI/GUI build. Starts a private Xvfb so
# X11 tests that only skip when DISPLAY is unset actually run.
set -euo pipefail

fail() {
	printf 'ci-test-build: %s\n' "$*" >&2
	exit 1
}

command -v go >/dev/null 2>&1 || fail 'go is not on PATH'
command -v gcc >/dev/null 2>&1 || fail 'gcc is not on PATH'
command -v Xvfb >/dev/null 2>&1 || fail 'Xvfb is not on PATH'

export CGO_ENABLED="${CGO_ENABLED:-1}"
export GOAMD64="${GOAMD64:-v2}"
export QT_QPA_PLATFORM="${QT_QPA_PLATFORM:-offscreen}"

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
go vet ./...
go test -count=1 -timeout 20m ./...
go build -o bin/tipsy ./cmd/tipsy
go build -o bin/tipsy-gui ./cmd/tipsy-gui
test -x bin/tipsy || fail 'CLI binary was not built'
test -x bin/tipsy-gui || fail 'GUI binary was not built'
