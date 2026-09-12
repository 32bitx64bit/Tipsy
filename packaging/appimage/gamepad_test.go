// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package appimage_test

import (
	"os"
	"strings"
	"testing"
)

// Phase 5 (controller-plan §6): the AppImage needs no sandbox change for
// gamepads — it runs unsandboxed on the host, so /dev/input/event* nodes
// stay visible and only the host permission state (input group / logind
// ACL) governs access. Pin that AppRun introduces no sandbox layer that
// could hide host evdev nodes; see packaging/gamepad-input.md.
func TestAppRunExposesHostInputDevices(t *testing.T) {
	data, err := os.ReadFile("AppRun")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, sandbox := range []string{
		"bwrap", "unshare", "firejail", "flatpak-spawn",
		"--dev-bind", "--ro-bind", "PRIVATE_DEV",
	} {
		if strings.Contains(text, sandbox) {
			t.Errorf("AppRun must not sandbox device nodes for gamepads, found %q", sandbox)
		}
	}
	if strings.Contains(text, "/dev/input") {
		t.Error("AppRun must not remap /dev/input (host evdev nodes stay visible as-is)")
	}
}
