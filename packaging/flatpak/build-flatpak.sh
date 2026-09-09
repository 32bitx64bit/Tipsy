#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build the Tipsy Flatpak bundle from the flatpak-builder manifest.
# First-build validation is still pending: run this on a machine with
# flatpak-builder and report the result before wiring bundles into releases.
#
# Usage:
#   packaging/flatpak/build-flatpak.sh --version 1.2.3 --output-dir dist
#                                      [--tag v1.2.3]
set -euo pipefail

fail() { printf 'build-flatpak: %s\n' "$*" >&2; exit 1; }

version=
output_dir=
tag=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || fail 'missing --version value'; version=$2; shift 2 ;;
    --output-dir) [[ $# -ge 2 ]] || fail 'missing --output-dir value'; output_dir=$2; shift 2 ;;
    --tag) [[ $# -ge 2 ]] || fail 'missing --tag value'; tag=$2; shift 2 ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[[ -n "$version" ]] || fail '--version is required'
[[ -n "$output_dir" ]] || fail '--output-dir is required'
[[ -n "$tag" ]] || tag="v$version"
command -v flatpak-builder >/dev/null 2>&1 || fail 'flatpak-builder is missing'
command -v flatpak >/dev/null 2>&1 || fail 'flatpak is missing'

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
bundle="$output_dir/Tipsy-${version}.flatpak"
[[ ! -e "$bundle" ]] || fail "output already exists: $bundle"

work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-flatpak.XXXXXXXX")
cleanup() {
  if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-flatpak.* ]]; then
    find "$work" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

manifest="$work/io.github.tipsy_linux.Tipsy.yaml"
sed "s/^\(\s*\)tag: main$/\1tag: $tag/" "$repo/packaging/flatpak/io.github.tipsy_linux.Tipsy.yaml" > "$manifest"
grep -q "tag: $tag" "$manifest" || fail 'manifest tag rewrite failed'

flatpak-builder --force-clean --repo="$work/repo" "$work/builddir" "$manifest"
flatpak build-bundle "$work/repo" "$bundle" io.github.tipsy_linux.Tipsy
[[ -s "$bundle" ]] || fail 'flatpak bundle was not produced'

printf 'Flatpak: %s\n' "$bundle"
