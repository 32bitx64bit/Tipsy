#!/usr/bin/env bash

set -euo pipefail

usage() {
    echo "usage: $0 [candidate-directory]" >&2
    exit 2
}

fail=0
release_candidate=0

reject() {
    echo "public-tree guard: forbidden path: $1" >&2
    fail=1
}

check_path() {
    path=$1

    if [ "$release_candidate" -eq 1 ]; then
        case "$path" in
            # A built AppDir contains audited, redistributable host libraries.
            # check-release-tree.sh separately rejects libroblox and every
            # private/runtime payload while validating the ELF closure.
            usr/lib/*.so|usr/lib/*.so.*)
                return
                ;;
        esac
    fi

    case "$path" in
        .tipsy-private|.tipsy-private/*|docs|docs/*|AGENTS.md|README.md|CONTRIBUTING.md|.cursor|.cursor/*)
            reject "$path"
            return
            ;;
        appData|appData/*|cache|cache/*|.tipsy|.tipsy/*)
            reject "$path"
            return
            ;;
        *.md|*.mdc)
            reject "$path"
            return
            ;;
    esac

    case "$path" in
        testdata/apk/*)
            return
            ;;
        *.apk|*.apkm|*.xapk|*.apks|*.obb|*.rbxm|libroblox.so|*/libroblox.so|lib/*.so|*/lib/*.so)
            reject "$path"
            return
            ;;
    esac

    base=${path##*/}
    case "$base" in
        .env|.env.*|Cookies|cookies.sqlite|cookies.txt|*.pem|*.key|*.p12|*.pfx)
            reject "$path"
            ;;
    esac
}

secret_pattern='(_\|WARNING:-DO-NOT-SHARE-THIS|-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----)'

check_secret_file() {
    path=$1
    full_path=$2

    # These files intentionally contain inert detection/redaction fixtures.
    case "$path" in
        internal/logging/logging_test.go|scripts/check-public-tree.sh)
            return
            ;;
    esac

    if LC_ALL=C grep -IqE "$secret_pattern" -- "$full_path"; then
        echo "public-tree guard: possible credential material: $path" >&2
        fail=1
    fi
}

check_list() {
    list=$1
    prefix=$2
    while IFS= read -r -d '' path; do
        check_path "$path"
        full_path=$prefix/$path
        if [ -f "$full_path" ] && [ ! -L "$full_path" ]; then
            check_secret_file "$path" "$full_path"
        fi
    done < "$list"
}

if [ "$#" -gt 1 ]; then
    usage
fi

list_file=$(mktemp)
trap 'rm -f "$list_file"' EXIT HUP INT TERM

if [ "$#" -eq 0 ]; then
    root=$(git rev-parse --show-toplevel 2>/dev/null) || usage
    git -C "$root" -c core.quotePath=false \
        ls-files -z --cached --others --exclude-standard > "$list_file"
    check_list "$list_file" "$root"
else
    candidate=$1
    release_candidate=1
    [ -d "$candidate" ] || usage
    (
        cd "$candidate"
        while IFS= read -r -d '' path; do
            printf '%s\0' "${path#./}"
        done < <(find . -path './.git' -prune -o -mindepth 1 -print0)
    ) > "$list_file"
    check_list "$list_file" "$candidate"
fi

if [ "$fail" -ne 0 ]; then
    echo "public-tree guard: FAILED" >&2
    exit 1
fi

echo "public-tree guard: OK"
