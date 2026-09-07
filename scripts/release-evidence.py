#!/usr/bin/env python3
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
"""Emit deterministic, unsigned P1 release metadata and evidence."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import re
import stat
import sys
from pathlib import Path
from typing import Any


HEX40 = re.compile(r"^[0-9a-f]{40}$")
SAFE_VERSION = re.compile(r"^[0-9A-Za-z][0-9A-Za-z._+-]*$")


class EvidenceError(ValueError):
    pass


def canonical_bytes(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True) + "\n").encode()


def digest_file(path: Path, algorithm: str = "sha256") -> str:
    digest = hashlib.new(algorithm)
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_exclusive(path: Path, data: bytes) -> None:
    path.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
    except OSError as exc:
        raise EvidenceError(f"refusing to replace evidence output {path.name}: {exc}") from exc
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
    except Exception:
        path.unlink(missing_ok=True)
        raise


def read_lock(path: Path) -> tuple[dict[str, Any], bytes]:
    raw = path.read_bytes()
    value = json.loads(raw)
    canonical = canonical_bytes(value)
    if raw != canonical:
        raise EvidenceError("release input lock is not canonical")
    return value, canonical


def valid_common(args: argparse.Namespace) -> None:
    if not SAFE_VERSION.fullmatch(args.version):
        raise EvidenceError("invalid version")
    if not HEX40.fullmatch(args.source_commit):
        raise EvidenceError("source commit must be 40 lowercase hexadecimal characters")
    if args.source_date_epoch < 0:
        raise EvidenceError("SOURCE_DATE_EPOCH must be non-negative")


def command_build_info(args: argparse.Namespace) -> None:
    valid_common(args)
    lock, canonical = read_lock(args.release_lock)
    release_kind = {
        "developer": "development-unrestricted",
        "official": "release-candidate-unsigned",
        "github-signed": "release-candidate-keyless",
    }[args.mode]
    tree = "dirty" if args.source_dirty else "clean"
    info = {
        "architecture": "x86_64",
        "format": "tipsy.build-info.v1",
        "inputLockSha256": hashlib.sha256(canonical).hexdigest(),
        "name": "Tipsy",
        "releaseKind": release_kind,
        "source": {
            "commit": args.source_commit,
            "repository": lock["source"]["repository"],
            "tree": tree,
        },
        "sourceDateEpoch": args.source_date_epoch,
        "version": args.version,
    }
    write_exclusive(args.output, canonical_bytes(info))


def read_manifest(appdir: Path) -> list[dict[str, Any]]:
    manifest = appdir / "usr/share/tipsy/manifest.sha256"
    rows: list[dict[str, Any]] = []
    for line in manifest.read_text(encoding="ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9._+/@:-]+)", line)
        if not match:
            raise EvidenceError("AppDir manifest is not canonical")
        digest, relative = match.groups()
        target = appdir / relative
        metadata = target.lstat()
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1:
            raise EvidenceError(f"manifest target is not a single-link regular file: {relative}")
        if digest_file(target) != digest:
            raise EvidenceError(f"manifest digest mismatch: {relative}")
        rows.append({"path": relative, "sha256": digest, "size": metadata.st_size})
    return rows


def spdx_id(relative: str) -> str:
    return "SPDXRef-File-" + hashlib.sha256(relative.encode()).hexdigest()[:24]


def command_evidence(args: argparse.Namespace) -> None:
    valid_common(args)
    appdir = args.appdir.resolve(strict=True)
    if appdir.is_symlink() or not appdir.is_dir():
        raise EvidenceError("AppDir must be a real directory")
    lock, canonical_lock = read_lock(args.release_lock)
    lock_digest = hashlib.sha256(canonical_lock).hexdigest()
    payload = read_manifest(appdir)
    artifacts: list[dict[str, Any]] = []
    for path in args.artifact:
        resolved = path.resolve(strict=True)
        metadata = resolved.stat()
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1:
            raise EvidenceError(f"artifact must be a single-link regular file: {path.name}")
        artifacts.append({"name": path.name, "sha256": digest_file(path), "size": metadata.st_size})
    artifacts.sort(key=lambda item: item["name"])
    if len({item["name"] for item in artifacts}) != len(artifacts):
        raise EvidenceError("artifact basenames must be unique")

    created = dt.datetime.fromtimestamp(args.source_date_epoch, tz=dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    namespace = f"{lock['source']['repository']}/spdx/{args.source_commit}/{lock_digest}/{args.version}"
    files = []
    verification_parts = []
    for item in payload:
        relative = item["path"]
        sha1 = digest_file(appdir / relative, "sha1")
        verification_parts.append(sha1)
        files.append(
            {
                "SPDXID": spdx_id(relative),
                "checksums": [
                    {"algorithm": "SHA1", "checksumValue": sha1},
                    {"algorithm": "SHA256", "checksumValue": item["sha256"]},
                ],
                "copyrightText": "NOASSERTION",
                "fileName": "./" + relative,
            }
        )
    package_verification = hashlib.sha1("".join(sorted(verification_parts)).encode()).hexdigest()
    package_id = "SPDXRef-Package-Tipsy"
    spdx = {
        "SPDXID": "SPDXRef-DOCUMENT",
        "creationInfo": {"created": created, "creators": ["Tool: tipsy-release-evidence-v1"]},
        "dataLicense": "CC0-1.0",
        "documentNamespace": namespace,
        "files": files,
        "name": f"Tipsy-{args.version}-x86_64",
        "packages": [
            {
                "SPDXID": package_id,
                "copyrightText": "NOASSERTION",
                "downloadLocation": "NOASSERTION",
                "filesAnalyzed": True,
                "licenseConcluded": "NOASSERTION",
                "licenseDeclared": "GPL-3.0-or-later",
                "name": "Tipsy",
                "packageVerificationCode": {"packageVerificationCodeValue": package_verification},
                "supplier": "Organization: The Tipsy Authors",
                "versionInfo": args.version,
            }
        ],
        "relationships": [
            {"relatedSpdxElement": package_id, "relationshipType": "DESCRIBES", "spdxElementId": "SPDXRef-DOCUMENT"},
            *[
                {"relatedSpdxElement": item["SPDXID"], "relationshipType": "CONTAINS", "spdxElementId": package_id}
                for item in files
            ],
        ],
        "spdxVersion": "SPDX-2.3",
    }

    material = {
        "artifacts": artifacts,
        "format": "tipsy.build-materials.v1",
        "inputLock": lock,
        "inputLockSha256": lock_digest,
        "payload": payload,
        "source": {"commit": args.source_commit, "repository": lock["source"]["repository"]},
    }
    subjects = [{"digest": {"sha256": item["sha256"]}, "name": item["name"]} for item in artifacts]
    provenance_input = {
        "_type": "https://in-toto.io/Statement/v1",
        "predicate": {
            "buildDefinition": {
                "buildType": "https://github.com/32bitx64bit/Tipsy/blob/main/docs/release-build-v1",
                "externalParameters": {"architecture": "x86_64", "mode": args.mode, "version": args.version},
                "internalParameters": {},
                "resolvedDependencies": [
                    {
                        "digest": {"gitCommit": args.source_commit},
                        "uri": "git+" + lock["source"]["repository"],
                    },
                    {
                        "digest": {"sha256": lock_digest},
                        "uri": "pkg:generic/tipsy-release-input-lock@1",
                    },
                ],
            },
            "runDetails": {
                "builder": {"id": str(lock["builder"]["image"])},
                "byproducts": [],
                "metadata": {"finishedOn": created, "invocationId": "UNSIGNED-P1-INPUT", "startedOn": created},
            },
        },
        "predicateType": "https://slsa.dev/provenance/v1",
        "subject": subjects,
    }
    artifact_manifest = {"artifacts": artifacts, "format": "tipsy.artifact-manifest.v1"}
    hashes = "".join(f"{item['sha256']}  {item['name']}\n" for item in artifacts).encode()

    outputs = {
        "artifact-manifest.json": canonical_bytes(artifact_manifest),
        "build-materials.json": canonical_bytes(material),
        "provenance-input.json": canonical_bytes(provenance_input),
        "release-hashes.sha256": hashes,
        "tipsy.spdx.json": canonical_bytes(spdx),
    }
    args.output_dir.mkdir(mode=0o755, parents=True, exist_ok=True)
    if any((args.output_dir / name).exists() for name in outputs):
        raise EvidenceError("evidence output already exists")
    for name in sorted(outputs):
        write_exclusive(args.output_dir / name, outputs[name])


def common_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--version", required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--source-date-epoch", required=True, type=int)
    parser.add_argument("--release-lock", required=True, type=Path)
    parser.add_argument("--mode", choices=("developer", "official", "github-signed"), required=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    build_info = commands.add_parser("build-info")
    common_arguments(build_info)
    build_info.add_argument("--source-dirty", action="store_true")
    build_info.add_argument("--output", required=True, type=Path)
    evidence = commands.add_parser("evidence")
    common_arguments(evidence)
    evidence.add_argument("--appdir", required=True, type=Path)
    evidence.add_argument("--artifact", required=True, action="append", type=Path)
    evidence.add_argument("--output-dir", required=True, type=Path)
    args = parser.parse_args()
    try:
        if args.command == "build-info":
            command_build_info(args)
        else:
            command_evidence(args)
    except (EvidenceError, OSError, json.JSONDecodeError) as exc:
        print(f"release-evidence: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
