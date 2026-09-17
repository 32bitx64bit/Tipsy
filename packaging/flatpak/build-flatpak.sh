#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build the Tipsy Flatpak bundle from the flatpak-builder manifest.
#
# Usage:
#   packaging/flatpak/build-flatpak.sh --version 1.2.3 --output-dir dist
#                                      [--tag v1.2.3] [--mode developer|official]
#                                      [--render-manifest OUT.yaml]
#
# --render-manifest writes the rewritten manifest to OUT.yaml and exits without
# flatpak; packaging/flatpak/flatpak_test.go uses it to pin the rewrite.
#
# --mode official (GitHub Actions only) marks the bundle release-repository-signed
# so the installed Flatpak runs OfficialVerified; the default developer build
# stays development-unrestricted and asks for --development consent.
#
# Runtimes come from Flathub into the user installation (the remote is added
# when missing). Uses the flatpak-builder binary when installed, otherwise the
# org.flatpak.Builder Flatpak (installed on demand).
set -euo pipefail

fail() { printf 'build-flatpak: %s\n' "$*" >&2; exit 1; }

version=
output_dir=
tag=
mode=developer
render_manifest=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) [[ $# -ge 2 ]] || fail 'missing --version value'; version=$2; shift 2 ;;
    --output-dir) [[ $# -ge 2 ]] || fail 'missing --output-dir value'; output_dir=$2; shift 2 ;;
    --tag) [[ $# -ge 2 ]] || fail 'missing --tag value'; tag=$2; shift 2 ;;
    --mode) [[ $# -ge 2 ]] || fail 'missing --mode value'; mode=$2; shift 2 ;;
    --render-manifest) [[ $# -ge 2 ]] || fail 'missing --render-manifest value'; render_manifest=$2; shift 2 ;;
    -h|--help) sed -n '2,21p' "$0"; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+~-]*$ ]] || fail 'invalid version'
[[ -n "$tag" ]] || tag="v$version"
[[ "$tag" =~ ^[0-9A-Za-z][0-9A-Za-z._/+~-]*$ ]] || fail 'invalid tag'
[[ "$mode" == developer || "$mode" == official ]] || fail 'mode must be developer or official'
release_kind=development-unrestricted
[[ "$mode" != official ]] || release_kind=release-repository-signed

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
app_id=io.github.tipsy_linux.Tipsy

# Rewrite the checked-in manifest for this release and verify every
# substitution landed. The version appears twice: in the go ldflags (that
# line wraps after the -X assignment, so match the assignment, not a closing
# quote) and in build-info.json.
render_manifest() {
  local out=$1
  sed -e "s|^\(\s*\)tag: main$|\1tag: $tag|" -e "s|@VERSION@|$version|g" \
    -e "s|@RELEASE_KIND@|$release_kind|g" \
    "$repo/packaging/flatpak/$app_id.yaml" > "$out"
  grep -q "^\s*tag: $tag\$" "$out" || fail 'manifest tag rewrite failed'
  grep -qF -- "internal/version.Version=$version" "$out" || fail 'manifest version rewrite failed'
  grep -qF -- "\"version\":\"$version\"" "$out" || fail 'manifest build-info version rewrite failed'
  grep -qF -- "\"releaseKind\":\"$release_kind\"" "$out" || fail 'manifest release-kind rewrite failed'
  ! grep -q '@VERSION@\|@RELEASE_KIND@' "$out" || fail 'manifest still has an unexpanded placeholder'
}

# org.kde.Sdk has gtk+-3.0 but not webkit2gtk-4.1. GNOME 49 sits on the same
# Freedesktop 25.08 ABI as KDE 6.10, so its WebKitGTK libraries can live in
# /app. Hardcoded /usr libexec paths are rewritten to /app (equal length).
stage_webkit_prefix() {
  local dest=$1
  local gnome=org.gnome.Platform//49
  local header_root
  mkdir -p "$dest/lib" "$dest/libexec" "$dest/include" "$dest/pkgconfig"

  flatpak info --user "$gnome" >/dev/null 2>&1 || \
    flatpak install --user -y --noninteractive flathub "$gnome"

  DEST="$dest" flatpak run --filesystem="$dest:create" --env=DEST="$dest" --command=bash "$gnome" -c '
    set -euo pipefail
    dest=$DEST
    libdir=/usr/lib/x86_64-linux-gnu
    mkdir -p "$dest/lib/webkit2gtk-4.1/injected-bundle" "$dest/libexec/webkit2gtk-4.1"
    for name in \
      libwebkit2gtk-4.1.so.0 \
      libjavascriptcoregtk-4.1.so.0 \
      libenchant-2.so.2 \
      libevdev.so.2 \
      libglycin-2.so.0 \
      libgssapi_krb5.so.2 \
      libhidapi-hidraw.so.0 \
      libk5crypto.so.3 \
      libkrb5.so.3 \
      libkrb5support.so.0 \
      libmanette-0.2.so.0 \
      libtinysparql-3.0.so.0
    do
      src=$libdir/$name
      [[ -e $src ]] || { printf "build-flatpak: missing %s in GNOME 49\n" "$src" >&2; exit 1; }
      cp -a -- "$src" "$dest/lib/"
      if [[ -L $src ]]; then
        cp -a -- "$(readlink -f -- "$src")" "$dest/lib/"
      fi
    done
    ln -sfn libwebkit2gtk-4.1.so.0 "$dest/lib/libwebkit2gtk-4.1.so"
    ln -sfn libjavascriptcoregtk-4.1.so.0 "$dest/lib/libjavascriptcoregtk-4.1.so"
    cp -a /usr/libexec/webkit2gtk-4.1/. "$dest/libexec/webkit2gtk-4.1/"
    cp -a "$libdir/webkit2gtk-4.1/injected-bundle/." "$dest/lib/webkit2gtk-4.1/injected-bundle/"
  '

  header_root=
  if [[ -d /usr/include/webkitgtk-4.1 ]]; then
    header_root=/usr/include/webkitgtk-4.1
  elif command -v apt-get >/dev/null 2>&1; then
    sudo apt-get install -y --no-install-recommends libwebkit2gtk-4.1-dev
    header_root=/usr/include/webkitgtk-4.1
  fi
  [[ -d ${header_root:-} ]] || fail 'webkitgtk-4.1 headers are required (libwebkit2gtk-4.1-dev)'
  cp -a -- "$header_root" "$dest/include/webkitgtk-4.1"

  cat >"$dest/pkgconfig/javascriptcoregtk-4.1.pc" <<'EOF'
prefix=/app
libdir=${prefix}/lib
includedir=${prefix}/include
Name: JavaScriptCoreGTK
Description: Tipsy-staged JavaScriptCoreGTK from GNOME 49
Version: 2.50
Requires: glib-2.0 gobject-2.0
Libs: -L${libdir} -ljavascriptcoregtk-4.1
Cflags: -I${includedir}/webkitgtk-4.1
EOF
  cat >"$dest/pkgconfig/webkit2gtk-4.1.pc" <<'EOF'
prefix=/app
libdir=${prefix}/lib
includedir=${prefix}/include
Name: WebKit2GTK
Description: Tipsy-staged WebKitGTK 4.1 from GNOME 49
Version: 2.50
Requires: gtk+-3.0 libsoup-3.0 javascriptcoregtk-4.1
Libs: -L${libdir} -lwebkit2gtk-4.1
Cflags: -I${includedir}/webkitgtk-4.1
EOF

  python3 - "$dest" <<'PY'
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
replacements = (
    (b"/usr/libexec/webkit2gtk-4.1", b"/app/libexec/webkit2gtk-4.1"),
    (b"/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1", b"/app/lib/webkit2gtk-4.1"),
)
for path in list(root.rglob("*")):
    if not path.is_file() or path.is_symlink():
        continue
    data = path.read_bytes()
    if b"webkit" not in data:
        continue
    updated = data
    for old, new in replacements:
        if len(new) > len(old):
            raise SystemExit(f"replacement longer than original: {new!r}")
        padded = new + b"\0" * (len(old) - len(new))
        updated = updated.replace(old, padded)
    if updated != data:
        path.write_bytes(updated)
PY

  [[ -f $dest/lib/libwebkit2gtk-4.1.so.0 ]] || fail 'staged WebKitGTK library is missing'
  [[ -f $dest/pkgconfig/webkit2gtk-4.1.pc ]] || fail 'staged webkit2gtk-4.1.pc is missing'
  [[ -f $dest/include/webkitgtk-4.1/webkit2/webkit2.h ]] || fail 'staged webkit2.h is missing'
}

if [[ -n "$render_manifest" ]]; then
  render_manifest "$render_manifest"
  exit 0
fi

[[ -n "$output_dir" ]] || fail '--output-dir is required'
command -v flatpak >/dev/null 2>&1 || fail 'flatpak is missing'
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
bundle="$output_dir/Tipsy-${version}.flatpak"
[[ ! -e "$bundle" ]] || fail "output already exists: $bundle"

flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo

builder=()
work_base=${TMPDIR:-/tmp}
if command -v flatpak-builder >/dev/null 2>&1; then
  builder=(flatpak-builder)
else
  flatpak info --user org.flatpak.Builder >/dev/null 2>&1 || \
    flatpak install --user -y --noninteractive flathub org.flatpak.Builder
  builder=(flatpak run org.flatpak.Builder)
  # The sandboxed builder has a private /tmp; stage under the home directory.
  work_base="${XDG_CACHE_HOME:-$HOME/.cache}"
  mkdir -p "$work_base"
fi

work=$(mktemp -d "$work_base/tipsy-flatpak.XXXXXXXX")
cleanup() {
  if [[ -n "${work:-}" && -d "$work" && "$work" == "$work_base"/tipsy-flatpak.* ]]; then
    chmod -R u+w -- "$work" 2>/dev/null || true
    rm -rf -- "$work"
  fi
}
trap cleanup EXIT HUP INT TERM

manifest="$work/$app_id.yaml"
render_manifest "$manifest"
stage_webkit_prefix "$work/webkit-prefix"

# --state-dir keeps flatpak-builder's cache out of the source checkout
# (its default is ./.flatpak-builder in the current directory).
"${builder[@]}" --user --install-deps-from=flathub --disable-rofiles-fuse \
  --state-dir="$work/state" --force-clean --repo="$work/repo" \
  "$work/builddir" "$manifest"
flatpak build-bundle "$work/repo" "$bundle" "$app_id"
[[ -s "$bundle" ]] || fail 'flatpak bundle was not produced'

# Sanity: the bundle carries both binaries and the exported launchers.
files=$(ostree --repo="$work/repo" ls -R "app/$app_id/x86_64/master" /files 2>/dev/null) \
  || fail 'cannot list the built app tree'
grep -q '/files/bin/tipsy$' <<<"$files" || fail 'bundle is missing bin/tipsy'
grep -q '/files/bin/tipsy-gui$' <<<"$files" || fail 'bundle is missing bin/tipsy-gui'
grep -q "/files/share/applications/$app_id.Play.desktop$" <<<"$files" || fail 'bundle is missing the Play desktop file'
grep -q '/files/share/tipsy/build-info.json$' <<<"$files" || fail 'bundle is missing build-info.json (release identity)'

printf 'Flatpak: %s\n' "$bundle"
