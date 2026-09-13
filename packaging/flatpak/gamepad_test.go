// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package flatpak_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 5 (controller-plan §6): the sandbox must pass through gamepad
// evdev nodes, or pads are EACCES-denied inside the Flatpak even when the
// host session can open them. --device=input is Flatpak 1.15.6+ and is
// rejected at build-finish on Ubuntu 24.04 / GitHub ubuntu-24.04
// (flatpak 1.14.6: "Unknown device type input"). Pin --device=all, which
// 1.14 understands and includes /dev/input, plus --device=dri. The
// finish-args must not name --device=input or the publish job dies after
// a successful compose. Same render seam as
// TestBuildFlatpakRendersManifestForBothModes.
//
// Like the existing flatpak tests, this relies on go test running with
// the package directory as the working directory.
func TestFlatpakManifestAllowsGamepadInput(t *testing.T) {
	raw, err := os.ReadFile("io.github.tipsy_linux.Tipsy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--device=all") {
		t.Fatal("flatpak manifest must grant --device=all for gamepad evdev nodes on Flatpak 1.14")
	}
	if !strings.Contains(string(raw), "--device=dri") {
		t.Fatal("flatpak manifest must keep --device=dri (GPU passthrough regressed)")
	}
	if strings.Contains(string(raw), "- --device=input") {
		t.Fatal("flatpak finish-args must not list --device=input: Ubuntu 24.04 flatpak 1.14.6 rejects it at build-finish")
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	out := filepath.Join(t.TempDir(), "manifest.yaml")
	cmd := exec.Command("bash", "build-flatpak.sh",
		"--version", "1.2.3", "--tag", "v1.2.3", "--mode", "developer", "--render-manifest", out)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render failed: %v\n%s", err, combined)
	}
	rendered, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "--device=all") {
		t.Fatal("rendered flatpak manifest lost --device=all")
	}
	if strings.Contains(string(rendered), "- --device=input") {
		t.Fatal("rendered flatpak finish-args must not list --device=input")
	}
}
