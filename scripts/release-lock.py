#!/usr/bin/env python3
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
"""Validate and query Tipsy's data-only deterministic release input lock."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any


SCHEMA = "tipsy.release-input-lock.v1"
HEX40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")


class LockError(ValueError):
    pass


def canonical_bytes(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True) + "\n").encode()


def read_lock(path: Path) -> tuple[dict[str, Any], bytes]:
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise LockError(f"cannot read release input lock: {exc}") from exc
    if len(raw) > 256 * 1024:
        raise LockError("release input lock is too large")
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise LockError(f"release input lock is not valid UTF-8 JSON: {exc}") from exc
    if not isinstance(value, dict):
        raise LockError("release input lock root must be an object")
    canonical = canonical_bytes(value)
    if raw != canonical:
        raise LockError("release input lock is not canonical sorted compact JSON")
    return value, canonical


def object_at(value: dict[str, Any], key: str) -> dict[str, Any]:
    child = value.get(key)
    if not isinstance(child, dict):
        raise LockError(f"{key} must be an object")
    return child


def validate(value: dict[str, Any], mode: str) -> None:
    required_root = {
        "actions",
        "builder",
        "downloads",
        "nativePackages",
        "packageSnapshot",
        "policy",
        "schema",
        "source",
        "status",
        "target",
    }
    if set(value) != required_root:
        raise LockError("release input lock has missing or unknown top-level fields")
    if value["schema"] != SCHEMA:
        raise LockError(f"unsupported release input lock schema: {value['schema']!r}")
    if value["status"] not in {"bootstrap-unverified", "reviewed"}:
        raise LockError("status must be bootstrap-unverified or reviewed")

    source = object_at(value, "source")
    if set(source) != {"refPattern", "repository"}:
        raise LockError("source has missing or unknown fields")
    repository = source["repository"]
    if not isinstance(repository, str) or not repository.startswith("https://github.com/"):
        raise LockError("source.repository must be a public canonical GitHub HTTPS URL")
    if "@" in repository.removeprefix("https://") or repository.endswith(".git"):
        raise LockError("source.repository must not contain credentials or a .git suffix")
    if source["refPattern"] != "refs/tags/v*":
        raise LockError("source.refPattern must be refs/tags/v*")

    target = object_at(value, "target")
    if set(target) != {"architecture", "go", "operatingSystem"}:
        raise LockError("target has missing or unknown fields")
    if target["architecture"] != "x86_64" or target["operatingSystem"] != "linux":
        raise LockError("only the linux/x86_64 release target is supported")
    if not isinstance(target["go"], str) or not re.fullmatch(r"1\.[0-9]+\.[0-9]+", target["go"]):
        raise LockError("target.go must be one exact stable Go version")

    actions = object_at(value, "actions")
    if set(actions) != {"attest", "checkout", "setup-go", "upload-artifact"}:
        raise LockError("actions must contain exactly the release workflow dependencies")
    for name, action in actions.items():
        if not isinstance(name, str) or not isinstance(action, dict) or set(action) != {"commit", "release"}:
            raise LockError("each action must contain only release and commit")
        if not isinstance(action["release"], str) or not re.fullmatch(r"v[0-9]+(?:\.[0-9]+){1,2}", action["release"]):
            raise LockError(f"action {name} has a non-exact release")
        if not isinstance(action["commit"], str) or not HEX40.fullmatch(action["commit"]):
            raise LockError(f"action {name} is not pinned by a full commit SHA")

    policy = object_at(value, "policy")
    if policy != {"network": "forbidden", "sourceMount": "read-only"}:
        raise LockError("official release policy must require a networkless build and read-only source")

    builder = object_at(value, "builder")
    snapshot = object_at(value, "packageSnapshot")
    downloads = object_at(value, "downloads")
    if set(builder) != {"image", "sha256"}:
        raise LockError("builder has missing or unknown fields")
    if not isinstance(builder["image"], str) or not builder["image"]:
        raise LockError("builder.image must be a non-empty string")
    if builder["sha256"] is not None and (not isinstance(builder["sha256"], str) or not HEX64.fullmatch(builder["sha256"])):
        raise LockError("builder.sha256 is invalid")
    if set(snapshot) != {"sha256", "uri"}:
        raise LockError("packageSnapshot has missing or unknown fields")
    if not isinstance(snapshot["uri"], str) or not snapshot["uri"]:
        raise LockError("packageSnapshot.uri must be a non-empty string")
    if snapshot["sha256"] is not None and (not isinstance(snapshot["sha256"], str) or not HEX64.fullmatch(snapshot["sha256"])):
        raise LockError("packageSnapshot.sha256 is invalid")
    if set(downloads) != {"appimagetool", "type2-runtime"}:
        raise LockError("downloads must contain exactly appimagetool and type2-runtime")
    packages = value["nativePackages"]
    if not isinstance(packages, list) or not all(isinstance(item, dict) for item in packages):
        raise LockError("nativePackages must be an array of objects")
    for package in packages:
        if set(package) != {"name", "sha256", "version"}:
            raise LockError("each native package must contain only name, version, and sha256")
        if not all(isinstance(package.get(k), str) and package[k] for k in ("name", "version")):
            raise LockError("native package name and version must be non-empty strings")
        if not isinstance(package["sha256"], str) or not HEX64.fullmatch(package["sha256"]):
            raise LockError(f"native package {package.get('name', '?')} lacks a pinned SHA-256")
    for name, material in downloads.items():
        if not isinstance(name, str) or not isinstance(material, dict):
            raise LockError("downloaded material entries must be objects")
        if set(material) != {"sha256", "status"} or material["status"] not in {"pinned", "required-at-invocation"}:
            raise LockError(f"downloaded material {name} is malformed")
        digest = material["sha256"]
        if digest is not None and (not isinstance(digest, str) or not HEX64.fullmatch(digest)):
            raise LockError(f"downloaded material {name} has an invalid SHA-256")

    if mode == "official":
        if value["status"] != "reviewed":
            raise LockError("official candidate requires a reviewed release input lock")
        if not isinstance(builder.get("image"), str) or builder["image"] == "UNAPPROVED":
            raise LockError("official candidate requires an approved builder image")
        if not isinstance(builder.get("sha256"), str) or not HEX64.fullmatch(builder["sha256"]):
            raise LockError("official candidate requires a pinned builder image digest")
        if not isinstance(snapshot.get("uri"), str) or snapshot["uri"] == "UNAPPROVED":
            raise LockError("official candidate requires an approved immutable package snapshot")
        if not isinstance(snapshot.get("sha256"), str) or not HEX64.fullmatch(snapshot["sha256"]):
            raise LockError("official candidate requires a pinned package snapshot digest")
        if not packages:
            raise LockError("official candidate requires exact native package pins")
        for name, material in downloads.items():
            if material["status"] != "pinned" or not isinstance(material["sha256"], str) or not HEX64.fullmatch(material["sha256"]):
                raise LockError(f"official candidate requires a pinned digest for {name}")


def lookup(value: Any, dotted: str) -> Any:
    current = value
    for component in dotted.split("."):
        if not isinstance(current, dict) or component not in current:
            raise LockError(f"unknown lock field: {dotted}")
        current = current[component]
    return current


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--lock", required=True, type=Path)
    parser.add_argument("--mode", choices=("developer", "official"), default="developer")
    parser.add_argument("--get")
    parser.add_argument("--digest", action="store_true")
    args = parser.parse_args()
    try:
        value, canonical = read_lock(args.lock)
        validate(value, args.mode)
        if args.get:
            result = lookup(value, args.get)
            if not isinstance(result, (str, int, float, bool)) and result is not None:
                raise LockError("requested field is not a scalar")
            if result is not None:
                print(str(result).lower() if isinstance(result, bool) else result)
        elif args.digest:
            print(hashlib.sha256(canonical).hexdigest())
    except LockError as exc:
        print(f"release-lock: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
