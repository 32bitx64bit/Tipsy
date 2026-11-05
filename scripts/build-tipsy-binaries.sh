#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build the tipsy and tipsy-gui binaries with the release ldflags into one
# directory.
#
# install-desktop.sh calls this when no TIPSY_BINARIES_DIR is set (a local
# build), and the publish workflow calls it once so the .deb, .rpm, and
# pacman assemblies all reuse a single compile instead of rebuilding the GUI
# for every format.
#
# Usage:
#   scripts/build-tipsy-binaries.sh --version 1.2.3 [--channel stable] --output-dir DIR
#
# --channel stable (default) or dev picks the desktop-file identity, exactly
# as install-desktop.sh documents.
set -euo pipefail

fail() { printf 'build-tipsy-binaries: %s\n' "$*" >&2; exit 1; }

version=
channel=stable
output_dir=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || fail 'missing --version value'; version=$2; shift 2 ;;
    --channel) [[ $# -ge 2 ]] || fail 'missing --channel value'; channel=$2; shift 2 ;;
    --output-dir) [[ $# -ge 2 ]] || fail 'missing --output-dir value'; output_dir=$2; shift 2 ;;
    -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

case "$version" in
  ''|*[!0-9A-Za-z._+-]*) fail "Invalid VERSION: ${version}" ;;
esac
case "$channel" in
  stable|dev) ;;
  *) fail "Invalid CHANNEL: ${channel} (stable or dev)" ;;
esac
[[ -n "$output_dir" ]] || fail '--output-dir is required'
command -v go >/dev/null 2>&1 || fail 'go is not on PATH'

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)

# Official packages target x86-64-v3 (AVX2). A local build may pick any
# supported GOAMD64 level; the cgo -march flag follows it so the C and Go
# objects keep the same ISA baseline.
export GOAMD64="${GOAMD64:-v3}"
case "$GOAMD64" in
	v1) cgo_march=x86-64 ;;
	v2) cgo_march=x86-64-v2 ;;
	v3) cgo_march=x86-64-v3 ;;
	v4) cgo_march=x86-64-v4 ;;
	*) fail "GOAMD64 must be v1, v2, v3, or v4 (got $GOAMD64)" ;;
esac
# Keep flags the caller already set (hardening, include paths) and append the
# release ISA flags instead of replacing the whole variable.
export CGO_CFLAGS="${CGO_CFLAGS:+$CGO_CFLAGS }-O2 -g -march=$cgo_march -fno-plt -fno-semantic-interposition"
if [ -d /tmp/tipsy-ci-pkgconfig ]; then
	export PKG_CONFIG_PATH="/tmp/tipsy-ci-pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
fi
ldflags="-buildid= -s -w -X github.com/tipsy-linux/tipsy/internal/version.Version=${version} -X github.com/tipsy-linux/tipsy/internal/version.Channel=${channel}"
cd "$repo"
tipsy_build=(-buildvcs=false -mod=readonly -trimpath -ldflags "$ldflags" -o "$output_dir/tipsy")
[[ ! -f "$repo/cmd/tipsy/default.pgo" ]] || tipsy_build+=(-pgo="$repo/cmd/tipsy/default.pgo")
gui_build=(-buildvcs=false -mod=readonly -trimpath -ldflags "$ldflags" -o "$output_dir/tipsy-gui")
[[ ! -f "$repo/cmd/tipsy-gui/default.pgo" ]] || gui_build+=(-pgo="$repo/cmd/tipsy-gui/default.pgo")
go build "${tipsy_build[@]}" ./cmd/tipsy
go build "${gui_build[@]}" ./cmd/tipsy-gui
[[ -x "$output_dir/tipsy" && -x "$output_dir/tipsy-gui" ]] || fail 'binaries were not produced'

printf 'build-tipsy-binaries: %s\n' "$output_dir"
