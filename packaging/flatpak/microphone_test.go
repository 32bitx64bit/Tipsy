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

// Voice-chat Phase 6: capture rides the same Pulse/PipeWire server as
// playback. Pin --socket=pulseaudio in the source manifest and in the
// rendered release manifest (no flatpak needed, same render seam as
// TestBuildFlatpakRendersManifestForBothModes / gamepad_test.go).
//
// Like the existing flatpak tests, this relies on go test running with
// the package directory as the working directory.
func TestFlatpakManifestAllowsPulseCapture(t *testing.T) {
	raw, err := os.ReadFile("io.github.tipsy_linux.Tipsy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--socket=pulseaudio") {
		t.Fatal("flatpak manifest must grant --socket=pulseaudio for playback and microphone capture")
	}
	if !strings.Contains(string(raw), "--socket=x11") {
		t.Fatal("flatpak manifest must keep --socket=x11 (X11 passthrough regressed)")
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
	if !strings.Contains(string(rendered), "--socket=pulseaudio") {
		t.Fatal("rendered flatpak manifest lost --socket=pulseaudio")
	}
}
