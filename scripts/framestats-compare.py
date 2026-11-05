#!/usr/bin/env python3
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
"""Compare two frame-time captures written by TIPSY_PPROF_DIR.

Reads framestats.json (or framestats.txt as a fallback) from each capture
directory and prints present-return interval p50/p95/p99, the 1% low, and the
overflow count. Writes nothing and never opens a browser or the client.

A capture with any overflow samples is reported as invalid: the native ring
wrapped between polls, so the retained intervals are not a complete record.
A capture with contended, out-of-order, clamped, reset, or unreadable samples
is invalid. Captures with different interval or 1% low definitions are not comparable.
Exit status: 0 compared, 1 a capture is invalid or unreadable, 2 usage.
"""

import argparse
import json
import os
import sys

METRIC = "present-return interval"


def load_capture(path):
    """Return the summary dict from framestats.json, or a parsed .txt fallback."""
    json_path = os.path.join(path, "framestats.json")
    txt_path = os.path.join(path, "framestats.txt")
    if os.path.isfile(json_path):
        with open(json_path, encoding="utf-8") as handle:
            return json.load(handle)
    if os.path.isfile(txt_path):
        return parse_text(txt_path)
    raise FileNotFoundError(
        f"{path}: no framestats.json or framestats.txt (was Tipsy stopped normally?)"
    )


def parse_text(path):
    """Parse the flat key: value framestats.txt into the JSON-shaped summary."""
    fields = {}
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            key, sep, value = line.rstrip("\n").partition(": ")
            if not sep:
                continue
            fields.setdefault(key, value)
    percentiles = {}
    for rank in ("p50", "p95", "p99", "max"):
        percentiles[rank] = parse_ms(fields.get(rank, ""))

    def number(key):
        raw = fields.get(key, "")
        match = ""
        for token in raw.split():
            if token.isdigit():
                match = token
                break
        return int(match) if match else 0

    presents = number("present-return intervals")
    low_fps = 0.0
    low = fields.get("1% low", "")
    if low:
        first = low.split()[0]
        try:
            low_fps = float(first)
        except ValueError:
            low_fps = 0.0
    return {
        "metric": fields.get("metric", ""),
        "definition": fields.get("definition", ""),
        "source": fields.get("source", ""),
        "path": fields.get("path", ""),
        "percentiles_us": percentiles,
        "one_percent_low": {
            "definition": fields.get("1% low definition", ""),
            "mean_fps": low_fps,
        },
        "presents": presents,
        "overflow_samples": number("overflow_samples"),
        "contended_samples": number("contended_samples"),
        "out_of_order_samples": number("out_of_order_samples"),
        "clamped_samples": number("clamped_samples"),
        "ring_resets": number("ring_resets"),
        "read_errors": number("read_errors"),
        "attempts": number("attempts"),
    }


def parse_ms(value):
    if not value or value == "n/a":
        return 0
    try:
        return int(round(float(value.split()[0]) * 1000))
    except (ValueError, IndexError):
        return 0


def metric_us(summary, rank):
    """Read a percentile in microseconds from either field naming."""
    direct = summary.get("percentiles_us", {}).get(rank)
    if direct is not None:
        return int(direct)
    legacy = summary.get("percentiles", {}).get(rank)
    return int(legacy) if legacy is not None else 0


def one_percent_low(summary):
    low = summary.get("one_percent_low") or {}
    fps = low.get("mean_fps", low.get("meanFPS", 0))
    definition = low.get("definition", "")
    return float(fps or 0), definition


def fmt_us(value):
    return "n/a" if value == 0 else f"{value / 1000:.3f} ms"


def compare(name, baseline, candidate):
    print(f"{name}:")
    print(f"  p50   {fmt_us(metric_us(baseline, 'p50')):>12} -> {fmt_us(metric_us(candidate, 'p50')):>12}")
    print(f"  p95   {fmt_us(metric_us(baseline, 'p95')):>12} -> {fmt_us(metric_us(candidate, 'p95')):>12}")
    print(f"  p99   {fmt_us(metric_us(baseline, 'p99')):>12} -> {fmt_us(metric_us(candidate, 'p99')):>12}")
    base_low, _ = one_percent_low(baseline)
    cand_low, _ = one_percent_low(candidate)
    print(f"  1% low {base_low:>11.3f} -> {cand_low:>11.3f} FPS")
    base_n = baseline.get("presents", 0)
    cand_n = candidate.get("presents", 0)
    print(f"  intervals {base_n} -> {cand_n}")


def presents_path(summary):
    """Return the present path a capture covers, if the summary names one."""
    path = summary.get("path")
    if path:
        return str(path)
    for line in summary.get("notes") or []:
        if isinstance(line, str) and "GLES" in line:
            return "vulkan-present (inferred from notes)"
    return "unknown"


def gles_disclosed(summary):
    """True when the capture states that the GLES present path was not recorded."""
    definition = summary.get("definition") or ""
    if "GLES present path is not captured" in definition:
        return True
    for line in summary.get("notes") or []:
        if isinstance(line, str) and "GLES present path not captured" in line:
            return True
    return False


def failures(path, summary):
    """Return the reasons this capture cannot be compared."""
    problems = []
    if summary.get("metric") != METRIC:
        problems.append(f"metric is {summary.get('metric')!r}, want {METRIC!r}")
    if presents_path(summary) != "vulkan-present":
        problems.append(
            "capture does not name path=vulkan-present, so it cannot be compared as a Vulkan present-path result"
        )
    if not gles_disclosed(summary):
        problems.append(
            "capture does not disclose that the GLES present path is not captured"
        )
    overflow = int(summary.get("overflow_samples") or 0)
    if overflow:
        problems.append(
            f"overflow_samples is {overflow}: the ring wrapped between polls, so the capture is incomplete"
        )
    contended = int(summary.get("contended_samples") or 0)
    if contended:
        problems.append(
            f"contended_samples is {contended}: a dropped present inflates the following interval, so the capture is incomplete"
        )
    for key in ("out_of_order_samples", "clamped_samples", "ring_resets", "read_errors"):
        value = int(summary.get(key) or 0)
        if value:
            problems.append(f"{key} is {value}: the capture is not reliable")
    if not summary.get("presents"):
        problems.append("no present-return intervals were captured")
    return problems


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("baseline", help="capture directory to compare from")
    parser.add_argument("candidate", help="capture directory to compare to")
    parser.add_argument("--label", default="candidate vs baseline", help="heading for the table")
    args = parser.parse_args(argv)

    loaded = []
    for path in (args.baseline, args.candidate):
        try:
            loaded.append(load_capture(path))
        except (OSError, ValueError) as err:
            print(f"framestats-compare: {err}", file=sys.stderr)
            return 1

    baseline, candidate = loaded
    status = 0
    for path, summary in zip((args.baseline, args.candidate), (baseline, candidate)):
        problems = failures(path, summary)
        if problems:
            status = 1
            for problem in problems:
                print(f"framestats-compare: {path}: {problem}", file=sys.stderr)

    for label, left, right in (
        ("interval definition", baseline.get("definition"), candidate.get("definition")),
        ("1% low definition", one_percent_low(baseline)[1], one_percent_low(candidate)[1]),
    ):
        if not left or left != right:
            status = 1
            print(f"framestats-compare: {label} missing or differs between captures", file=sys.stderr)

    print(f"metric: {METRIC}")
    print(f"present path: {presents_path(baseline)}")
    print(f"definition: {baseline.get('definition', '')}")
    print(f"1% low: {one_percent_low(baseline)[1]}")
    compare(args.label, baseline, candidate)
    if status:
        print("framestats-compare: this comparison is not a performance result (see the errors above).")
    return status


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
