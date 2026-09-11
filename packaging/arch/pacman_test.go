// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package pacman_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The pacman pkgver must not contain a hyphen (pkgrel is the fixed -1), so a
// mis-tagged version must fail before any build work. Guards the version rule
// the publish workflow depends on.
func TestBuildPacmanRejectsHyphenatedVersion(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	out := filepath.Join(t.TempDir(), "dist")
	cmd := exec.Command("bash", "build-pacman.sh", "--version", "1.2.3-1", "--output-dir", out)
	combined, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("hyphenated version was accepted:\n%s", combined)
	}
	if !strings.Contains(string(combined), "invalid version") {
		t.Fatalf("unexpected failure message:\n%s", combined)
	}
}

// --help must not require any build tool (it runs before the tool checks).
func TestBuildPacmanHelp(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	cmd := exec.Command("bash", "build-pacman.sh", "--help")
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, combined)
	}
	if !strings.Contains(string(combined), "--version") || !strings.Contains(string(combined), "--mode") {
		t.Fatalf("usage text lacks expected options:\n%s", combined)
	}
}
