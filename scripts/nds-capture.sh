#!/usr/bin/env bash
# Copyright 2026 The Tipsy Authors
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Prepares one frame-time capture of Natural Disaster Survival (NDS).
#
# This script never launches Roblox or Tipsy and never signs in: you start the
# client and play NDS yourself. It only creates a timestamped capture directory
# and a run.env file with the process-profile environment. Source run.env in
# the shell that launches Tipsy, play the baseline, then stop Tipsy normally so
# the profile stop() writes framestats.json/framestats.txt.
#
# usage: scripts/nds-capture.sh --label baseline|control|NAME [--out DIRECTORY] [--contention]
set -euo pipefail

usage() {
	cat >&2 <<USAGE
usage: $0 --label LABEL [--out DIRECTORY] [--contention]

  --label LABEL  short capture label, for example baseline, control, or gc-arm.
                 It becomes part of the capture directory name.
  --out DIR      parent directory for captures. Default: \$HOME/tipsy-captures
  --contention   also enable TIPSY_PPROF_CONTENTION=1 (mutex/block profiles).
                 Use only on an instrumented arm; it changes scheduling cost.

Creates DIR/nds-LABEL-TIMESTAMP/ containing run.env. Nothing else is written
until you launch Tipsy with that environment and stop it normally.
USAGE
	exit 2
}

label=
out="$HOME/tipsy-captures"
contention=0
while [[ $# -gt 0 ]]; do
	case "$1" in
		--label)
			[[ $# -ge 2 ]] || usage
			label=$2
			shift 2
			;;
		--out)
			[[ $# -ge 2 ]] || usage
			out=$2
			shift 2
			;;
		--contention)
			contention=1
			shift
			;;
		-h|--help) usage ;;
		*) usage ;;
	esac
done

[[ -n "$label" ]] || usage
[[ "$label" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || {
	printf 'nds-capture: label must match [0-9A-Za-z][0-9A-Za-z._-]*\n' >&2
	exit 2
}

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
mkdir -p "$out"
out=$(CDPATH= cd -- "$out" && pwd -P)

# The capture directory is 0700 and must be new: a repeated label at the same
# second is an error, not a silent overwrite of another run's evidence.
stamp=$(date -u +%Y%m%dT%H%M%SZ)
dir="$out/nds-$label-$stamp"
n=1
while [[ -e "$dir" ]]; do
	n=$((n + 1))
	dir="$out/nds-$label-$stamp-$n"
done
mkdir -m 0700 -- "$dir"

revision=unknown
if git -C "$repo" rev-parse --short HEAD >/dev/null 2>&1; then
	revision=$(git -C "$repo" rev-parse --short HEAD)
fi

{
	printf '# Frame-time capture for Natural Disaster Survival.\n'
	printf '# Source this file in the shell that launches Tipsy, then stop Tipsy normally.\n'
	printf 'export TIPSY_PPROF_DIR=%q\n' "$dir"
	if [[ "$contention" == 1 ]]; then
		printf 'export TIPSY_PPROF_CONTENTION=1\n'
	else
		printf 'export TIPSY_PPROF_CONTENTION=0\n'
	fi
	printf '# captured from %s at %s (source revision %s)\n' "$repo" "$stamp" "$revision"
} >"$dir/run.env"
chmod 0600 "$dir/run.env"

cat <<STEPS
nds-capture: prepared $dir

What was written:
  run.env   environment for one capture (TIPSY_PPROF_DIR, TIPSY_PPROF_CONTENTION)

What you do:
  1. Build or install the Tipsy binaries from this checkout (revision $revision).
     Official packages are GOAMD64=v3; a local build should use the same.
  2. In a terminal, run:
         set -a; . "$dir/run.env"; set +a
  3. Launch Tipsy from that same terminal and sign in as usual.
  4. Open Natural Disaster Survival and play one steady baseline session.
     Keep the same graphics settings, window size, and vsync/pacing for every
     arm you intend to compare. Note the settings in $dir/session.txt.
  5. Close Tipsy normally (window close, not SIGKILL). While it runs, the
     capture refreshes these two files every 200 ms (tmp + rename), so a crash
     still leaves the latest numbers; the normal stop writes the final totals:
         $dir/framestats.json   p50/p95/p99, the 1% low, and overflow_samples
         $dir/framestats.txt    the same numbers with their definitions
         $dir/cpu.pprof, $dir/heap.pprof, ... (pprof artifacts)
  6. Merge the captured CPU profiles into the PGO profile. GUI gameplay runs
     in-process in tipsy-gui, so install it there and then commit it:
         scripts/pgo-merge.sh --gui "$dir"
         git add cmd/tipsy-gui/default.pgo
     A CLI-only capture goes to cmd/tipsy/default.pgo instead:
         scripts/pgo-merge.sh --cli "$dir"
  7. Compare two captures:
         scripts/framestats-compare.py "$dir" OTHER_CAPTURE_DIR

What is measured:
  'present-return interval': time between two successive returns from a
  successful present call, on host CLOCK_MONOTONIC. It is not display
  scan-out and not input-to-photon latency. The 1% low is the mean FPS of the
  slowest 1% of those intervals.

This script does not launch Roblox or Tipsy and does not read account state.
If framestats.json holds overflow_samples above 0, the capture is invalid:
the ring wrapped between polls, so shorten the session or treat it as an error.

Steps 3-6 are manual and are not recorded by this script.
STEPS
