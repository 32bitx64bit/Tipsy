#!/usr/bin/env bash
# Detect a usable Tipsy development environment. Never fail with a one-liner.
set -euo pipefail

ok=0
warn=0
fail=0

say() { printf '%s\n' "$*"; }
pass() { say "  OK     $*"; ok=$((ok + 1)); }
note() { say "  WARN   $*"; warn=$((warn + 1)); }
bad()  { say "  FAIL   $*"; fail=$((fail + 1)); }

say "Tipsy bootstrap"
say

if command -v go >/dev/null 2>&1; then
	ver=$(go version)
	pass "Go: $ver"
else
	bad "Go not found. Install Go 1.23+ and retry."
fi

arch=$(uname -m)
if [ "$arch" = "x86_64" ]; then
	pass "Architecture: $arch"
else
	bad "Architecture $arch is not x86_64. Tipsy runs the official Android x86-64 client natively."
fi

if command -v pkg-config >/dev/null 2>&1; then
	pass "pkg-config: $(command -v pkg-config)"
else
	note "pkg-config missing (required to build the Qt GUI)."
fi

if pkg-config --exists Qt6Widgets Qt6Gui Qt6Core 2>/dev/null; then
	pass "Qt 6 Widgets: $(pkg-config --modversion Qt6Widgets)"
else
	note "Qt 6 Widgets not found. CLI still builds. GUI needs qt6-base (or equivalent)."
fi

if [ "${XDG_SESSION_TYPE:-}" = "x11" ] || [ -n "${DISPLAY:-}" ]; then
	pass "X11 session: XDG_SESSION_TYPE=${XDG_SESSION_TYPE:-?} DISPLAY=${DISPLAY:-unset}"
else
	note "No DISPLAY / not an X11 session. Native X11 is the primary runtime target."
fi

if command -v gcc >/dev/null 2>&1 || command -v cc >/dev/null 2>&1; then
	pass "C compiler: $(command -v gcc || command -v cc)"
else
	note "No gcc/cc. Required for the Qt GUI (cgo) and later ABI test libraries."
fi

say
say "Summary: $ok ok, $warn warnings, $fail failures"
if [ "$fail" -ne 0 ]; then
	say "Install the FAIL items, then rerun ./scripts/bootstrap.sh"
	exit 1
fi

say "Next: go test ./... && go build -o bin/tipsy ./cmd/tipsy"
