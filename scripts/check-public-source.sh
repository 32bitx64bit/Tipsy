#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Admit only source that is safe to make public. This is intentionally a Git
# object-database audit rather than a working-tree scan: an artifact built at
# HEAD must not be released from a repository that still exposes private or
# proprietary material through any reachable ref.
set -euo pipefail

usage() {
	cat >&2 <<USAGE
usage: $0 [--repo DIRECTORY] [--commit COMMIT]

Checks the selected Git commit and every commit reachable from every local ref
for private source, credentials, and proprietary payloads. Diagnostics name
only a commit and a path; source contents are never printed.
USAGE
	exit 2
}

fail() {
	printf 'check-public-source: %s\n' "$*" >&2
	exit 1
}

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
commit=HEAD
while [[ $# -gt 0 ]]; do
	case "$1" in
		--repo) [[ $# -ge 2 ]] || usage; repo=$2; shift 2 ;;
		--commit) [[ $# -ge 2 ]] || usage; commit=$2; shift 2 ;;
		-h|--help) usage ;;
		*) usage ;;
	esac
done

[[ -d "$repo" && ! -L "$repo" ]] || fail 'repository is not a directory'
repo=$(CDPATH= cd -- "$repo" && pwd -P)

git_repo() {
	env -u GIT_DIR -u GIT_WORK_TREE git -C "$repo" "$@"
}

top_level=$(git_repo rev-parse --show-toplevel 2>/dev/null) || fail 'repository is not a Git work tree'
top_level=$(CDPATH= cd -- "$top_level" && pwd -P)
[[ "$top_level" == "$repo" ]] || fail 'repository path is not the Git top level'

selected_commit=$(git_repo rev-parse --verify "${commit}^{commit}" 2>/dev/null) || fail 'selected commit is not a Git commit'
[[ "$selected_commit" =~ ^[0-9a-f]{40}$ ]] || fail 'selected commit is not a full Git commit'
checked_out_commit=$(git_repo rev-parse --verify 'HEAD^{commit}' 2>/dev/null) || fail 'checked out HEAD is not a Git commit'
[[ "$checked_out_commit" == "$selected_commit" ]] || fail 'selected commit is not the checked out HEAD'
if [[ -n $(git_repo status --porcelain=v1 --untracked-files=all) ]]; then
	fail 'work tree is not clean for selected commit'
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-public-source.XXXXXXXX")
cleanup() {
	if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-public-source.* ]]; then
		find "$work" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

commits_file="$work/commits"
candidates_file="$work/blob-candidates"
tree_file="$work/tree"
: > "$candidates_file"
git_repo rev-list --topo-order --all "$selected_commit" > "$commits_file" || fail 'cannot enumerate reachable Git commits'
[[ -s "$commits_file" ]] || fail 'no reachable Git commits found'

is_synthetic_fixture() {
	local path=$1
	# APK/SO fixtures are only admitted when both their location and name make
	# their synthetic status explicit. The ZIP exception is for the existing
	# synthetic split-package fixture, whose members are synthetic APKs.
	[[ "$path" =~ ^testdata/(.*/)?synthetic[-_.][^/]+\.(apk|so|zip)$ ]]
}

report_path() {
	local kind=$1
	local commit=$2
	local path=$3
	# %q makes control characters in hostile paths inert while still identifying
	# the path. Do not print blob data or matching values here.
	printf 'check-public-source: %s: %s %q\n' "$kind" "$commit" "$path" >&2
}

violations=0
# Private working material is deliberately allowed to exist locally, but it
# must be ignored and never enter the index. Use --no-index to test the ignore
# rule itself even if a bad commit already tracks this path. Reachable
# historical copies are also rejected below with their committing path and ID.
if ! git_repo check-ignore --no-index -q -- .tipsy-private/; then
	fail '.tipsy-private/ must be ignored'
fi
while IFS= read -r -d '' private_path; do
	report_path 'private material tracked' "$selected_commit" "$private_path"
	violations=1
done < <(git_repo ls-files -z -- .tipsy-private)

declare -A seen_blob_paths=()

while IFS= read -r commit; do
	[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || fail 'Git returned an invalid commit ID'
	git_repo ls-tree -r -z --full-tree "$commit" > "$tree_file" || fail "cannot inspect commit $commit"
	while IFS= read -r -d '' record; do
		meta=${record%%$'\t'*}
		path=${record#*$'\t'}
		read -r mode object_type object_id <<< "$meta"
		[[ "$object_id" =~ ^[0-9a-f]{40}$ ]] || fail "commit $commit has an invalid Git object ID"
		[[ -n "$path" ]] || fail "commit $commit has an empty tracked path"
		# A public source release must consist only of ordinary, self-contained
		# files. Git symlinks can redirect packaging into host paths and gitlinks
		# make the reviewed source depend on another repository, so reject both
		# before treating their object data as source.
		if [[ "$mode" == 120000 ]]; then
			report_path 'Git symlink entry' "$commit" "$path"
			violations=1
			continue
		fi
		if [[ "$object_type" != blob || ( "$mode" != 100644 && "$mode" != 100755 ) ]]; then
			report_path 'non-regular Git entry' "$commit" "$path"
			violations=1
			continue
		fi
		lower_path=${path,,}
		base_name=${lower_path##*/}

		case "/$lower_path/" in
			*/.tipsy-private/*)
				report_path 'private material tracked' "$commit" "$path"
				violations=1
				;;
		esac

		case "$base_name" in
			.env|.env.local|.env.production|.netrc|id_rsa|id_dsa|id_ecdsa|id_ed25519|credentials|credentials.*|credential.*|secrets|secrets.*|secret.*|token.*|auth-token.*|roblosecurity.*|*.pem|*.key|*.p12|*.pfx|*.p7b|*.p7c|*.jks|*.keystore|*.crt|*.cer|*.der)
				report_path 'private-key, certificate, or credential filename' "$commit" "$path"
				violations=1
				;;
		esac
		if [[ "$base_name" == *private*key* || "$base_name" == *credential* ]]; then
			report_path 'private-key, certificate, or credential filename' "$commit" "$path"
			violations=1
		fi

		case "$lower_path" in
			*.apk|*.apkm|*.xapk|*.apks|*.obb|*.rbxm|*.so|*/libroblox.so)
				if ! is_synthetic_fixture "$lower_path"; then
					report_path 'Roblox or proprietary payload filename' "$commit" "$path"
					violations=1
				fi
				;;
		esac

		# Content inspection usually needs only one record per blob. Keep the
		# path in that identity, however: the narrowly reviewed generic-credential
		# fixture exception below is deliberately bound to both a blob ID and its
		# historical source path. The same blob under another path must be scanned
		# and rejected normally.
		blob_path_key=$object_id$'\x1f'$path
		if [[ "$object_type" == blob && -z "${seen_blob_paths[$blob_path_key]+x}" ]]; then
			seen_blob_paths[$blob_path_key]=1
			printf '%s\0%s\0%s\0' "$object_id" "$commit" "$path" >> "$candidates_file"
		fi
	done < "$tree_file"
done < "$commits_file"

# Keep content inspection in one process so that every unique regular blob is
# read through Git plumbing. It recognizes complete, encoded PEM private-key
# blocks and credential values with strong formats/entropy. Markers in
# redaction code are not enough to trigger it. Blob size is checked first: an
# uninspectably large tracked blob is rejected without ever being loaded into
# memory. Diagnostics intentionally contain only commit IDs and escaped paths.
if ! python3 - "$repo" "$candidates_file" <<'PY'
import collections
import io
import json
import math
import os
import re
import subprocess
import sys
import zipfile

repo, candidates_path = sys.argv[1:]

PEM_PRIVATE_KEY = re.compile(
    rb"-----BEGIN(?: [A-Z0-9]+){0,3} PRIVATE KEY-----"
    rb"[ \t\r\n]+[A-Za-z0-9+/= \t\r\n]{32,}?"
    rb"-----END(?: [A-Z0-9]+){0,3} PRIVATE KEY-----",
    re.IGNORECASE,
)
KNOWN_TOKENS = (
    re.compile(rb"(?<![A-Za-z0-9_])ghp_[A-Za-z0-9]{36}(?![A-Za-z0-9_])"),
    re.compile(rb"(?<![A-Za-z0-9_])github_pat_[A-Za-z0-9_]{50,}(?![A-Za-z0-9_])"),
    re.compile(rb"(?<![A-Za-z0-9_])glpat-[A-Za-z0-9_-]{20,}(?![A-Za-z0-9_])"),
    re.compile(rb"(?<![A-Za-z0-9_])sk_live_[A-Za-z0-9]{24,}(?![A-Za-z0-9_])"),
    re.compile(rb"(?<![A-Za-z0-9_])xox[baprs]-[A-Za-z0-9-]{24,}(?![A-Za-z0-9_])"),
)
# These are synthetic redaction fixtures in the logging tests, reviewed as
# historical source. This exception is intentionally limited to the generic
# assigned/bearer high-entropy heuristic: PEM blocks, known provider token
# formats, and proprietary-payload checks remain unconditional. Pair the path
# with the Git blob ID so a new fixture or a copy elsewhere fails closed until
# it is independently reviewed and added here.
GENERIC_CREDENTIAL_FIXTURE_ALLOWLIST = {
    "internal/logging/logging_test.go": frozenset((
        "29b2714b944bce1f3b5abaecf79db92b2c2bad5d",
        "29bcf100cb32fe66841690e688f9848f39a84025",
        "57130ac3c72744cad6dc0c015b68d0399bc2de5f",
        "64435761862bfd222a01795875835c244e882a3e",
        "ccdfafeb8b98b32b1af4513c923489d919d21322",
    )),
}
ASSIGNED_CREDENTIAL = re.compile(
    rb"(?ix)(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|"
    rb"authorization|roblosecurity|aws_secret_access_key)[ \t]*(?:=|:)[ \t]*"
    rb"(?:bearer[ \t]+)?[\"']?([A-Za-z0-9_./+=-]{24,})"
)
BEARER_CREDENTIAL = re.compile(rb"(?i)\bbearer[ \t]+([A-Za-z0-9_./+=-]{24,})")
PLACEHOLDER_PREFIXES = (
    b"redacted", b"example", b"placeholder", b"dummy", b"fake", b"test",
    b"your_", b"replace_", b"changeme", b"<", b"$",
)
PROPRIETARY_MEMBER = re.compile(rb"(?:^|/)(?:libroblox\.so|[^/]+\.(?:apk|apkm|xapk|apks|obb|rbxm|so))$", re.IGNORECASE)
SYNTHETIC_FIXTURE = re.compile(r"^testdata/(?:.*/)?synthetic[-_.][^/]+\.(?:apk|so|zip)$", re.IGNORECASE)
MAX_TRACKED_BLOB_BYTES = 8 * 1024 * 1024


def report(kind: str, commit: str, path: str) -> None:
    # JSON escaping keeps hostile path bytes from altering terminal output.
    print(f"check-public-source: {kind}: {commit} {json.dumps(path, ensure_ascii=True)}", file=sys.stderr)


def entropy(value: bytes) -> float:
    counts = collections.Counter(value)
    size = len(value)
    return -sum((count / size) * math.log2(count / size) for count in counts.values())


def generic_high_entropy_credential(data: bytes) -> bool:
    for pattern in (ASSIGNED_CREDENTIAL, BEARER_CREDENTIAL):
        for match in pattern.finditer(data):
            value = match.group(1)
            lowered = value.lower()
            if lowered.startswith(PLACEHOLDER_PREFIXES):
                continue
            if entropy(value) >= 3.5:
                return True
    return False


def generic_credential_fixture_allowlisted(object_id: str, path: str) -> bool:
    return object_id in GENERIC_CREDENTIAL_FIXTURE_ALLOWLIST.get(path, ())


def synthetic_fixture(path: str) -> bool:
    return SYNTHETIC_FIXTURE.fullmatch(path) is not None


def disguised_proprietary_payload(data: bytes, path: str) -> bool:
    if synthetic_fixture(path):
        return False
    if data.startswith(b"\x7fELF"):
        return True
    if not (data.startswith(b"PK\x03\x04") or data.startswith(b"PK\x05\x06")):
        return False
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            return any(PROPRIETARY_MEMBER.search(member.filename.encode("utf-8", "surrogateescape")) for member in archive.infolist())
    except zipfile.BadZipFile:
        return False


raw = open(candidates_path, "rb").read().split(b"\0")
if raw and raw[-1] == b"":
    raw.pop()
if len(raw) % 3:
    print("check-public-source: malformed internal Git object list", file=sys.stderr)
    sys.exit(1)
records = [tuple(item.decode("utf-8", "surrogateescape") for item in raw[index:index + 3]) for index in range(0, len(raw), 3)]

environment = os.environ.copy()
environment.pop("GIT_DIR", None)
environment.pop("GIT_WORK_TREE", None)
size_process = subprocess.Popen(
    ["git", "-C", repo, "cat-file", "--batch-check"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL,
    env=environment,
)
content_process = subprocess.Popen(
    ["git", "-C", repo, "cat-file", "--batch"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL,
    env=environment,
)
assert size_process.stdin is not None and size_process.stdout is not None
assert content_process.stdin is not None and content_process.stdout is not None
violations = False
for object_id, commit, path in records:
    size_process.stdin.write((object_id + "\n").encode("ascii"))
    size_process.stdin.flush()
    header = size_process.stdout.readline().decode("ascii", "replace").strip().split()
    if len(header) != 3 or header[1] != "blob" or not header[2].isdigit():
        report("cannot inspect tracked blob", commit, path)
        violations = True
        continue
    size = int(header[2])
    if size > MAX_TRACKED_BLOB_BYTES:
        report(f"tracked blob exceeds {MAX_TRACKED_BLOB_BYTES}-byte inspection limit", commit, path)
        violations = True
        continue
    content_process.stdin.write((object_id + "\n").encode("ascii"))
    content_process.stdin.flush()
    content_header = content_process.stdout.readline().decode("ascii", "replace").strip().split()
    if len(content_header) != 3 or content_header[1] != "blob" or content_header[2] != str(size):
        report("cannot inspect tracked blob", commit, path)
        violations = True
        continue
    data = content_process.stdout.read(size)
    if len(data) != size or content_process.stdout.read(1) != b"\n":
        report("cannot read tracked blob", commit, path)
        violations = True
        continue
    if PEM_PRIVATE_KEY.search(data):
        report("encoded PEM private key", commit, path)
        violations = True
    if any(pattern.search(data) for pattern in KNOWN_TOKENS):
        report("high-confidence credential value", commit, path)
        violations = True
    if generic_high_entropy_credential(data) and not generic_credential_fixture_allowlisted(object_id, path):
        report("high-confidence credential value", commit, path)
        violations = True
    if disguised_proprietary_payload(data, path):
        report("Roblox or proprietary binary payload", commit, path)
        violations = True
size_process.stdin.close()
content_process.stdin.close()
if size_process.wait() != 0 or content_process.wait() != 0:
    print("check-public-source: Git object inspection failed", file=sys.stderr)
    sys.exit(1)
sys.exit(1 if violations else 0)
PY
then
	violations=1
fi

[[ "$violations" == 0 ]] || fail 'source admission rejected; see commit/path diagnostics above'
printf 'check-public-source: admitted %s\n' "$selected_commit"
