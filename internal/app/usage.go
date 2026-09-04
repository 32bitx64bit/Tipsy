// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"encoding/json"
	"fmt"
	"io"
)

const usageText = `Usage: tipsy <command> [arguments]

Commands:
  doctor              Host environment overview
  inspect             Inspect official Roblox APKs / splits
  diagnose-native     ELF imports/exports for a native library
  compare-roblox      Compare two Roblox package trees
  report              Combined compatibility report
  diagnose            Diagnose a subsystem (x11, graphics, audio, jni, loader, roblox, auth)
  config              Show or edit XDG config
  version             Print version
  logs                Show log directory and TIPSY_LOG usage
  setup               Verify and install the official x86-64 client
  launch              Load the official client (X11). Use --probe to exit after JNI_OnLoad
  repair              Repair install (not yet implemented)
  help                Show this help

Machine-readable output:
  tipsy inspect --json <paths...>
  tipsy report --json <paths...>
  tipsy doctor --json
  tipsy doctor --report

Never commit Roblox APKs or native libraries.
`

const doctorHelp = `Usage: tipsy doctor [--json] [--report]

Print a host environment overview (OS, X11, GPU, audio, Qt, Roblox data dir).
--report is secret-redacted and suitable for sharing.
`

const diagnoseHelp = `Usage: tipsy diagnose [--json] [subsystem]

Subsystems: x11, graphics, audio, jni, loader, roblox, auth
Omit the subsystem to print all of them.
`

const inspectHelp = `Usage: tipsy inspect [--json] <apk-or-dir> [...]

Inspect official Roblox APKs, split APKs, directories, or .apkm/.xapk/.zip sets.
`

const diagnoseNativeHelp = `Usage: tipsy diagnose-native [--json] <lib.so>

Static ELF analysis: DT_NEEDED, imports/exports, TLS, Android notes, JNI entry points.
`

const compareHelp = `Usage: tipsy compare-roblox [--json] <old> <new>

Compare two official Roblox package trees or APKs.
`

const reportHelp = `Usage: tipsy report [--json] <apk-or-dir> [...]

Combined machine-readable compatibility report (APK + ELF + hints).
`

const setupHelp = `Usage: tipsy setup <apk-or-dir> [...]

Cryptographically verify and atomically install official Roblox APKs, split
APKs, or .apkm/.xapk/.apks/.zip sets. Pass regular package files obtained
through your own authorized account. Never commit Roblox packages.
`

const launchHelp = `Usage: tipsy launch [--probe]

Load the extracted official Android x86-64 client. Run tipsy setup first.

  --probe   Load libroblox.so, run JNI_OnLoad, then exit (no game loop)
`

const configHelp = `Usage: tipsy config [path|get|set]

  tipsy config              Print config file path and current JSON
  tipsy config path         Print config file path
  tipsy config get [key]    Print all keys or one key
  tipsy config set key val  Set dataDir, logLevel, or logCategories
`

func printUsage(w io.Writer) {
	fmt.Fprint(w, usageText)
}

func marshalIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
