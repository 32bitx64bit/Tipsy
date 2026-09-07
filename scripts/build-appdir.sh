#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail

usage() {
	cat >&2 <<USAGE
usage: $0 --version VERSION [--output-dir DIRECTORY] [--mode developer|official|github-signed]
  [--release-lock FILE] [--source-commit COMMIT]

Builds a clean x86_64 AppDir and reproducible .tar.gz archive. The build uses
the installed Qt 6 runtime and never downloads tools, APKs, or dependencies.
USAGE
	exit 2
}

fail() {
	printf 'build-appdir: %s\n' "$*" >&2
	exit 1
}

version=
output_dir=
mode=developer
release_lock=
source_commit=
while [[ $# -gt 0 ]]; do
	case "$1" in
		--version)
			[[ $# -ge 2 ]] || usage
			version=$2
			shift 2
			;;
		--output-dir)
			[[ $# -ge 2 ]] || usage
			output_dir=$2
			shift 2
			;;
		--mode)
			[[ $# -ge 2 ]] || usage
			mode=$2
			shift 2
			;;
		--release-lock)
			[[ $# -ge 2 ]] || usage
			release_lock=$2
			shift 2
			;;
		--source-commit)
			[[ $# -ge 2 ]] || usage
			source_commit=$2
			shift 2
			;;
		-h|--help)
			usage
			;;
		*)
			usage
			;;
	esac
done

[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]] || fail 'VERSION must contain only release-safe characters'
[[ "$mode" == developer || "$mode" == official || "$mode" == github-signed ]] || fail 'mode must be developer, official, or github-signed'

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [[ -z "$release_lock" ]]; then
	release_lock="$repo/scripts/release-inputs.lock.json"
fi
[[ -f "$release_lock" && ! -L "$release_lock" ]] || fail 'release input lock is not a regular file'
release_lock=$(readlink -f -- "$release_lock")
"$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode"
release_lock_sha256=$("$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode" --digest)
canonical_repository=$("$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode" --get source.repository)
head_commit=$(git -C "$repo" rev-parse HEAD 2>/dev/null) || fail 'source tree has no reviewed Git commit'
[[ "$head_commit" =~ ^[0-9a-f]{40}$ ]] || fail 'source HEAD is not a full Git commit'
if [[ -z "$source_commit" ]]; then
	source_commit=$head_commit
fi
[[ "$source_commit" =~ ^[0-9a-f]{40}$ ]] || fail 'source commit must contain 40 lowercase hexadecimal characters'
source_dirty=false
if [[ -n $(git -C "$repo" status --porcelain=v1 --untracked-files=all) ]]; then
	source_dirty=true
fi
if [[ "$mode" != developer ]]; then
	[[ "$source_commit" == "$head_commit" ]] || fail 'official candidate source commit does not match HEAD'
	[[ "$source_dirty" == false ]] || fail 'official candidate requires a completely clean source tree'
	origin=$(git -C "$repo" remote get-url origin 2>/dev/null || true)
	origin=${origin%.git}
	[[ "$origin" == "$canonical_repository" ]] || fail 'Git origin does not match the canonical release-input repository'
fi
if [[ -z "$output_dir" ]]; then
	output_dir="$repo/dist"
fi
mkdir -p -- "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)

arch=$(uname -m)
case "$arch" in
	x86_64|amd64)
		arch=x86_64
		;;
	*)
		fail "unsupported host architecture: $arch (Tipsy packages the Android x86-64 client)"
		;;
esac

appdir_name="Tipsy-${version}-${arch}.AppDir"
archive_name="${appdir_name}.tar.gz"
final_appdir="$output_dir/$appdir_name"
final_archive="$output_dir/$archive_name"
[[ ! -e "$final_appdir" ]] || fail "output already exists: $final_appdir"
[[ ! -e "$final_archive" ]] || fail "output already exists: $final_archive"

required_commands=(go gcc pkg-config qmake6 patchelf ldd readelf file getcap install python3 readlink sha256sum stat tar gzip)
for command_name in "${required_commands[@]}"; do
	command -v "$command_name" >/dev/null 2>&1 || fail "required command is missing: $command_name"
done
required_pkg_modules=(Qt6Widgets Qt6Gui Qt6Core x11 xext pangocairo pangoft2 cairo-xlib)
pkg-config --exists "${required_pkg_modules[@]}" || \
	fail 'Qt 6, X11/Xext, or Pango/Cairo development files are missing'
[[ $(go env GOOS) == linux ]] || fail 'the active Go toolchain is not targeting Linux'
if [[ "$mode" != developer ]]; then
	expected_go="go$("$repo/scripts/release-lock.py" --lock "$release_lock" --mode "$mode" --get target.go)"
	actual_go=$(go env GOVERSION)
	# GitHub's setup-go toolchain reports the exact locked version.  Permit a
	# distributor build suffix only for the local, reproducible validation path;
	# it must still name the same upstream Go release.
	[[ "$actual_go" == "$expected_go" || "$actual_go" == "$expected_go"-* ]] || \
		fail "signed Go toolchain mismatch (expected $expected_go, got $actual_go)"
fi

qt_plugins=$(qmake6 -query QT_INSTALL_PLUGINS)
[[ -d "$qt_plugins" ]] || fail "Qt plugin directory does not exist: $qt_plugins"

source_date_epoch=${SOURCE_DATE_EPOCH:-}
if [[ -z "$source_date_epoch" ]]; then
	source_date_epoch=$(git -C "$repo" show -s --format=%ct "$source_commit" 2>/dev/null || true)
fi
[[ "$source_date_epoch" =~ ^[0-9]+$ ]] || fail 'set SOURCE_DATE_EPOCH to a non-negative integer'
commit_epoch=$(git -C "$repo" show -s --format=%ct "$source_commit" 2>/dev/null || true)
[[ "$commit_epoch" =~ ^[0-9]+$ ]] || fail 'source commit has no valid timestamp'
if [[ "$mode" != developer && "$source_date_epoch" != "$commit_epoch" ]]; then
	fail 'signed SOURCE_DATE_EPOCH must equal the reviewed commit timestamp'
fi

umask 022
export LC_ALL=C.UTF-8
export TZ=UTC
export GOOS=linux
export GOARCH=amd64
export CGO_ENABLED=1
export GOTOOLCHAIN=local
export GOPROXY=off
export GOSUMDB=off

work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-appdir.XXXXXXXX")
cleanup() {
	if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-appdir.* ]]; then
		find "$work" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

appdir="$work/$appdir_name"
install -d \
	"$appdir/usr/bin" \
	"$appdir/usr/lib" \
	"$appdir/usr/plugins/platforms" \
	"$appdir/usr/plugins/platforminputcontexts" \
	"$appdir/usr/share/applications" \
	"$appdir/usr/share/icons/hicolor/512x512/apps" \
	"$appdir/usr/share/licenses/tipsy" \
	"$appdir/usr/share/licenses/dependencies" \
	"$appdir/usr/share/metainfo" \
	"$appdir/usr/share/tipsy"

ldflags="-buildid= -s -w -X github.com/tipsy-linux/tipsy/internal/version.Version=$version"
(cd "$repo" && go build -buildvcs=false -mod=readonly -trimpath -ldflags "$ldflags" -o "$appdir/usr/bin/tipsy" ./cmd/tipsy)
(cd "$repo" && go build -buildvcs=false -mod=readonly -trimpath -ldflags "$ldflags" -o "$appdir/usr/bin/tipsy-gui" ./cmd/tipsy-gui)

install -m 0755 "$repo/packaging/appimage/AppRun" "$appdir/AppRun"
install -m 0644 "$repo/packaging/appimage/qt.conf" "$appdir/usr/bin/qt.conf"
install -m 0644 "$repo/tipsy.png" "$appdir/tipsy.png"
install -m 0644 "$repo/tipsy.png" "$appdir/.DirIcon"
install -m 0644 "$repo/tipsy.png" "$appdir/usr/share/icons/hicolor/512x512/apps/tipsy.png"
install -m 0644 "$repo/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop" "$appdir/io.github.tipsy_linux.Tipsy.Play.desktop"
install -m 0644 "$repo/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop" "$appdir/usr/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop"
install -m 0644 "$repo/share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop" "$appdir/usr/share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop"

# appimagetool injects X-AppImage-Version into the AppDir-root desktop after
# the payload manifest is sealed. Write the same key first so an extracted
# wrap still matches usr/share/tipsy/manifest.sha256.
inject_appimage_version() {
	local desktop=$1
	local tmp
	tmp=$(mktemp)
	awk -v ver="$version" '
		$0 == "[Desktop Entry]" { in_entry=1; print; next }
		in_entry && /^\[/ {
			if (!seen) print "X-AppImage-Version=" ver
			in_entry=0
			seen=1
			print
			next
		}
		in_entry && /^X-AppImage-Version=/ { next }
		{ print }
		END {
			if (in_entry && !seen) print "X-AppImage-Version=" ver
		}
	' "$desktop" > "$tmp"
	install -m 0644 "$tmp" "$desktop"
	rm -f -- "$tmp"
}
inject_appimage_version "$appdir/io.github.tipsy_linux.Tipsy.Play.desktop"
install -m 0644 "$repo/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml" "$appdir/usr/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml"
install -m 0644 "$repo/LICENSE" "$appdir/usr/share/licenses/tipsy/LICENSE"
install -m 0644 "$repo/NOTICE" "$appdir/usr/share/licenses/tipsy/NOTICE"

plugins=(
	platforms/libqxcb.so
	platforms/libqoffscreen.so
	platforminputcontexts/libcomposeplatforminputcontextplugin.so
	platforminputcontexts/libibusplatforminputcontextplugin.so
)
roots=("$appdir/usr/bin/tipsy" "$appdir/usr/bin/tipsy-gui")
for relative in "${plugins[@]}"; do
	source_plugin="$qt_plugins/$relative"
	[[ -f "$source_plugin" ]] || fail "required Qt plugin is missing: $source_plugin"
	install -m 0755 "$source_plugin" "$appdir/usr/plugins/$relative"
	roots+=("$source_plugin")
done

declare -A seen_libraries=()
declare -A seen_packages=()
queue=("${roots[@]}")

is_host_library() {
	case "$1" in
		ld-linux-*.so.*|libc.so.*|libdl.so.*|libm.so.*|libpthread.so.*|libresolv.so.*|librt.so.*|libutil.so.*|libanl.so.*|libnss_*.so.*|libsystemd.so.*|libEGL.so.*|libGL.so.*|libGLX.so.*|libGLdispatch.so.*|libOpenGL.so.*|libGLES*.so.*|libvulkan.so.*|libdrm.so.*|libgbm.so.*|libglapi.so.*)
			return 0
			;;
	esac
	return 1
}

package_owner() {
	local library=$1
	local owner=
	if command -v pacman >/dev/null 2>&1; then
		owner=$(pacman -Qqo "$library" 2>/dev/null || true)
	elif command -v dpkg-query >/dev/null 2>&1; then
		owner=$(dpkg-query -S "$(readlink -f "$library")" 2>/dev/null | head -n 1 | cut -d: -f1 || true)
	elif command -v rpm >/dev/null 2>&1; then
		owner=$(rpm -qf --qf '%{NAME}' "$library" 2>/dev/null || true)
	fi
	printf '%s' "$owner"
}

copy_package_license() {
	local library=$1
	local package package_path destination copied=0 candidate destination_name
	package=$(package_owner "$library")
	[[ -n "$package" ]] || fail "cannot identify the package that owns bundled library: ${library##*/}"
	package_path=${package//:/_}
	[[ -z "${seen_packages[$package_path]:-}" ]] || return 0
	destination="$appdir/usr/share/licenses/dependencies/$package_path"
	install -d "$destination"
	for candidate in \
		"/usr/share/licenses/$package" \
		"/usr/share/doc/$package/copyright"; do
		if [[ -d "$candidate" ]]; then
			while IFS= read -r -d '' license_file; do
				destination_name=${license_file##*/}
				case "$destination_name" in
					*.md) destination_name=${destination_name%.md}.txt ;;
				esac
				install -m 0644 "$license_file" "$destination/$destination_name"
				copied=1
			done < <(find "$candidate" -maxdepth 1 -type f -print0)
		elif [[ -f "$candidate" ]]; then
			install -m 0644 "$candidate" "$destination/COPYRIGHT"
			copied=1
		fi
	done
	if (( copied == 0 )) && command -v pacman >/dev/null 2>&1; then
		while IFS= read -r license_id; do
			license_id=${license_id%% WITH *}
			# libasyncns' Arch metadata uses the historical generic "LGPL"
			# label, while its installed public header specifies LGPL-2.1-or-
			# later. Resolve that package-specific alias to the matching SPDX
			# text instead of either omitting its notice or guessing globally.
			if [[ "$package:$license_id" == "libasyncns:LGPL" ]]; then
				license_id=LGPL-2.1-or-later
			fi
			candidate="/usr/share/licenses/spdx/$license_id.txt"
			if [[ -f "$candidate" ]]; then
				install -m 0644 "$candidate" "$destination/${license_id}.txt"
				copied=1
			fi
		done < <(pacman -Qi "$package" 2>/dev/null | sed -n 's/^Licenses[[:space:]]*:[[:space:]]*//p' | tr ' ' '\n')
	fi
	(( copied == 1 )) || fail "no redistributable license notice found for dependency package: $package"
	seen_packages[$package_path]=1
}

index=0
while (( index < ${#queue[@]} )); do
	object=${queue[$index]}
	index=$((index + 1))
	ldd_output=$(ldd "$object" 2>&1) || fail "cannot inspect dynamic dependencies of: $object"
	if grep -q 'not found' <<<"$ldd_output"; then
		printf '%s\n' "$ldd_output" >&2
		fail "unresolved build-host dependency in: $object"
	fi
	while IFS= read -r library; do
		[[ -n "$library" && -f "$library" ]] || continue
		basename=${library##*/}
		is_host_library "$basename" && continue
		if [[ -n "${seen_libraries[$basename]:-}" ]]; then
			current="$appdir/usr/lib/$basename"
			[[ $(sha256sum "$current" | cut -d' ' -f1) == $(sha256sum "$library" | cut -d' ' -f1) ]] || fail "two different libraries share basename: $basename"
			continue
		fi
		install -m 0755 "$library" "$appdir/usr/lib/$basename"
		seen_libraries[$basename]=1
		copy_package_license "$library"
		queue+=("$library")
	done < <(awk '/=> \// {print $3} /^[[:space:]]*\// {print $1}' <<<"$ldd_output")
done

patchelf --set-rpath '$ORIGIN/../lib' "$appdir/usr/bin/tipsy" "$appdir/usr/bin/tipsy-gui"
while IFS= read -r -d '' library; do
	patchelf --set-rpath '$ORIGIN' "$library"
done < <(find "$appdir/usr/lib" -type f -print0)
while IFS= read -r -d '' plugin; do
	patchelf --set-rpath '$ORIGIN/../../lib' "$plugin"
done < <(find "$appdir/usr/plugins" -type f -name '*.so' -print0)

qt_version=$(pkg-config --modversion Qt6Core)
go_version=$(go env GOVERSION)
case "$mode" in
	developer) release_kind=development-unrestricted ;;
	official) release_kind=release-candidate-unsigned ;;
	github-signed) release_kind=release-candidate-keyless ;;
esac
cat > "$appdir/usr/share/tipsy/build-info" <<BUILD_INFO
format=tipsy.build-info.v1
name=Tipsy
version=$version
architecture=$arch
source_date_epoch=$source_date_epoch
source_repository=$canonical_repository
source_commit=$source_commit
source_tree=$([[ "$source_dirty" == true ]] && printf dirty || printf clean)
release_kind=$release_kind
input_lock_sha256=$release_lock_sha256
go=$go_version
qt=$qt_version
BUILD_INFO
build_info_args=(
	build-info
	--version "$version"
	--source-commit "$source_commit"
	--source-date-epoch "$source_date_epoch"
	--release-lock "$release_lock"
	--mode "$mode"
	--output "$appdir/usr/share/tipsy/build-info.json"
)
if [[ "$source_dirty" == true ]]; then
	build_info_args+=(--source-dirty)
fi
"$repo/scripts/release-evidence.py" "${build_info_args[@]}"

manifest="$appdir/usr/share/tipsy/manifest.sha256"
(
	cd "$appdir"
	find . -type f ! -path './usr/share/tipsy/manifest.sha256' -print0 \
		| sort -z \
		| while IFS= read -r -d '' relative; do
			sha256sum "${relative#./}"
		done
) > "$manifest"

"$repo/scripts/check-release-tree.sh" "$appdir"

while IFS= read -r -d '' entry; do
	touch --no-dereference --date "@$source_date_epoch" "$entry"
done < <(find "$appdir" -print0)

mv -- "$appdir" "$final_appdir"
tar --sort=name --mtime="@$source_date_epoch" --owner=0 --group=0 --numeric-owner \
	-C "$output_dir" -cf - "$appdir_name" | gzip -n > "$work/$archive_name"
mv -- "$work/$archive_name" "$final_archive"

printf 'AppDir: %s\n' "$final_appdir"
printf 'Archive: %s\n' "$final_archive"
