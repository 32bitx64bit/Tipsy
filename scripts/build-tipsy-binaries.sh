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

export GOAMD64="${GOAMD64:-v2}"
ldflags="-buildid= -s -w -X github.com/tipsy-linux/tipsy/internal/version.Version=${version} -X github.com/tipsy-linux/tipsy/internal/version.Channel=${channel}"
cd "$repo"
go build -buildvcs=false -mod=readonly -trimpath -ldflags "$ldflags" -o "$output_dir/tipsy" ./cmd/tipsy
go build -buildvcs=false -mod=readonly -trimpath -ldflags "$ldflags" -o "$output_dir/tipsy-gui" ./cmd/tipsy-gui
[[ -x "$output_dir/tipsy" && -x "$output_dir/tipsy-gui" ]] || fail 'binaries were not produced'

printf 'build-tipsy-binaries: %s\n' "$output_dir"
