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
  diagnose            Diagnose a subsystem (x11, graphics, audio, jni, loader, roblox, auth, gamepad|pad|controller)
  config              Show or edit XDG config
  version             Print version
  logs                Show log directory and TIPSY_LOG usage
  setup               Verify and install the official x86-64 client
  launch              Load the verified client (X11). --development explicitly authorizes local-build mode
  desktop             Launcher integration: status, adopt, release, render (Tipsy vs Tipsy-Dev identity)
  repair              Repair install (not yet implemented)
  help                Show this help

Machine-readable output:
  tipsy inspect --json <paths...>
  tipsy report --json <paths...>
  tipsy doctor --json

Never commit Roblox APKs or native libraries.
`

const doctorHelp = `Usage: tipsy doctor [--json]

Print a host environment overview (OS, X11, GPU, audio, Qt, gamepad pads, Roblox data dir).
Output is secret-redacted and suitable for sharing.
`

const diagnoseHelp = `Usage: tipsy diagnose [--json] [subsystem]

Subsystems: x11, graphics, audio, jni, loader, roblox, auth, gamepad|pad|controller
Omit the subsystem to print all of them.

Controller environment (headless parity; env wins per key over the
persisted "gamepad" section in the config file, missing JSON = defaults):
  TIPSY_GAMEPAD=0|off               whole subsystem off (engine sees zero pads)
  TIPSY_GAMEPAD_PATH=direct         pad feed-in arm (direct-only; others deliver nothing)
  TIPSY_GAMEPAD_DEADZONE=0.0-0.5    global stick deadzone floor (default: device flat)
  TIPSY_GAMEPAD_DEADZONE_LEFT/RIGHT=…
                                    per-stick override (default: follow global)
  TIPSY_GAMEPAD_INVERT_Y=1          invert both stick-Y axes
  TIPSY_GAMEPAD_INVERT_Y_LEFT/RIGHT=1
                                    invert one stick-Y (ORs with global)
  TIPSY_GAMEPAD_RUMBLE=0|1          rumble preference (default on-if-supported; Phase 4)
  TIPSY_GAMEPAD_DEBUG=1             per-event arg logging (off by default)
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

const setupHelp = `Usage: tipsy setup [--development] <apk-or-dir> [...]

Cryptographically verify and atomically install official Roblox APKs, split
APKs, or .apkm/.xapk/.apks/.zip sets. Pass regular package files obtained
through your own authorized account. Never commit Roblox packages.

  --development   Persist explicit DevelopmentUnrestricted consent for this
                  local build. It never grants OfficialVerified status.
`

const launchHelp = `Usage: tipsy launch [--development] [--probe] [uri]

Load an authenticated Android x86-64 runtime generation. Run tipsy setup first.

  --development   Persist explicit DevelopmentUnrestricted consent, then
                  derive from an existing cryptographically verified retained
                  APK when needed. Every such launch displays a warning.
  --probe   Load libroblox.so, run JNI_OnLoad, then exit (no game loop)
  uri       Optional Roblox website or protocol URI (roblox-player:,
            roblox://experiences/start, or https://www.roblox.com/games/...)

A website Play URI that includes an official authentication ticket signs the
Android session in through the same private cookie store as in-app login.
Ticket and cookie values are never logged.
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
