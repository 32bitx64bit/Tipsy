#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail

usage() {
	cat >&2 <<USAGE
usage: $0 --artifact FILE --bundle FILE --source-ref REF --gh FILE --gh-sha256 SHA256
  [--policy FILE]

Offline-verifies a GitHub/Sigstore artifact-attestation bundle with a pinned
GitHub CLI. Production verification remains disabled until H0 approves the
canonical repository and protected release workflow identity.
USAGE
	exit 2
}

fail() {
	printf 'attestation verification: %s\n' "$*" >&2
	exit 1
}

artifact=
bundle=
gh_cli=
gh_sha256=
policy=
source_ref=
while [[ $# -gt 0 ]]; do
	case "$1" in
		--artifact) [[ $# -ge 2 ]] || usage; artifact=$2; shift 2 ;;
		--bundle) [[ $# -ge 2 ]] || usage; bundle=$2; shift 2 ;;
		--source-ref) [[ $# -ge 2 ]] || usage; source_ref=$2; shift 2 ;;
		--gh) [[ $# -ge 2 ]] || usage; gh_cli=$2; shift 2 ;;
		--gh-sha256) [[ $# -ge 2 ]] || usage; gh_sha256=$2; shift 2 ;;
		--policy) [[ $# -ge 2 ]] || usage; policy=$2; shift 2 ;;
		-h|--help) usage ;;
		*) usage ;;
	esac
done

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
if [[ -z "$policy" ]]; then
	policy="$repo/scripts/attestation-policy.json"
fi
for input in "$artifact" "$bundle" "$gh_cli" "$policy"; do
	[[ -f "$input" && ! -L "$input" ]] || fail "input is not a regular file: $input"
done
[[ -x "$gh_cli" ]] || fail 'pinned GitHub CLI is not executable'
[[ "$gh_sha256" =~ ^[0-9a-f]{64}$ ]] || fail 'GitHub CLI SHA-256 is invalid'
actual_gh=$(sha256sum "$gh_cli")
actual_gh=${actual_gh%% *}
[[ "$actual_gh" == "$gh_sha256" ]] || fail 'GitHub CLI SHA-256 mismatch'

work=$(mktemp -d "${TMPDIR:-/tmp}/tipsy-attestation.XXXXXXXX")
cleanup() {
	if [[ -n "${work:-}" && -d "$work" && "$work" == "${TMPDIR:-/tmp}"/tipsy-attestation.* ]]; then
		find "$work" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM
jq -cS . "$policy" > "$work/policy.canonical"
cmp -s "$policy" "$work/policy.canonical" || fail 'attestation policy is not canonical sorted compact JSON'
[[ $(jq -r '.schema' "$policy") == tipsy.attestation-policy.v1 ]] || fail 'unsupported attestation policy schema'
[[ $(jq -r '.status' "$policy") == reviewed ]] || fail 'attestation policy is bootstrap-unverified; H0 approval is required'
issuer=$(jq -er '.issuer | select(type == "string")' "$policy")
repository=$(jq -er '.repository | select(type == "string")' "$policy")
workflow=$(jq -er '.workflow | select(type == "string")' "$policy")
ref_pattern=$(jq -er '.sourceRefPattern | select(type == "string")' "$policy")
predicate=$(jq -er '.predicateType | select(type == "string")' "$policy")
[[ "$issuer" == https://token.actions.githubusercontent.com ]] || fail 'unexpected OIDC issuer policy'
[[ "$ref_pattern" == 'refs/tags/v*' ]] || fail 'source ref policy must be refs/tags/v*'
[[ "$source_ref" == refs/tags/v* && "$source_ref" =~ ^refs/tags/v[0-9A-Za-z._+-]+$ ]] || fail 'source ref is outside the reviewed release-tag policy'
[[ "$predicate" == https://slsa.dev/provenance/v1 ]] || fail 'unexpected provenance predicate policy'
[[ $(jq -r '.builderEnvironment' "$policy") == github-hosted ]] || fail 'self-hosted release attestations are not accepted'

# The bundle is supplied explicitly and the verifier is denied network access.
# `gh` performs the cryptographic Sigstore verification; this wrapper only
# fixes the expected repository/workflow/ref/runner policy.
command -v bwrap >/dev/null 2>&1 || fail 'bubblewrap is required for offline attestation verification'
bwrap --ro-bind / / --dev /dev --proc /proc --unshare-net --new-session --cap-drop ALL \
	"$gh_cli" attestation verify "$artifact" \
	--bundle "$bundle" \
	--repo "$repository" \
	--signer-workflow "$workflow" \
	--source-ref "$source_ref" \
	--deny-self-hosted-runners \
	--format json > "$work/verification.json"
jq -e 'type == "array" and length > 0' "$work/verification.json" >/dev/null || fail 'verifier returned no accepted attestation'
printf 'attestation verification: OK (%s)\n' "$(basename -- "$artifact")"
