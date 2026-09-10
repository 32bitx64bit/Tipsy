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

// The publish workflow once failed at build-flatpak.sh's own sanity check
// after the manifest's go build line wrapped: the rewrite was fine, the
// grep expected the old shape. Render through the script (no flatpak needed)
// so the manifest and its checks are pinned together.
func TestBuildFlatpakRendersManifestForBothModes(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	for _, tc := range []struct {
		mode string
		kind string
	}{
		{mode: "developer", kind: "development-unrestricted"},
		{mode: "official", kind: "release-repository-signed"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "manifest.yaml")
			cmd := exec.Command("bash", "build-flatpak.sh",
				"--version", "1.2.3", "--tag", "v1.2.3", "--mode", tc.mode, "--render-manifest", out)
			if combined, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("render failed: %v\n%s", err, combined)
			}
			raw, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			manifest := string(raw)
			for _, want := range []string{
				"tag: v1.2.3\n",
				"internal/version.Version=1.2.3\n",
				"internal/version.Channel=stable\"",
				`"releaseKind":"` + tc.kind + `"`,
				`"version":"1.2.3"`,
				"/app/share/tipsy/build-info.json",
			} {
				if !strings.Contains(manifest, want) {
					t.Errorf("rendered manifest lacks %q", want)
				}
			}
			for _, stale := range []string{"@VERSION@", "@RELEASE_KIND@", "\n        tag: main\n"} {
				if strings.Contains(manifest, stale) {
					t.Errorf("rendered manifest still contains %q", stale)
				}
			}
		})
	}
}

// The default is a local, unverified build; only CI passes --mode official.
func TestBuildFlatpakDefaultsToDeveloperMode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	out := filepath.Join(t.TempDir(), "manifest.yaml")
	cmd := exec.Command("bash", "build-flatpak.sh", "--version", "0.0.0-dev", "--render-manifest", out)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render failed: %v\n%s", err, combined)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"releaseKind":"development-unrestricted"`) {
		t.Fatalf("default mode must stay development-unrestricted:\n%s", raw)
	}
	if strings.Contains(string(raw), `"releaseKind":"release-repository-signed"`) {
		t.Fatal("default mode must never mark the bundle official")
	}
}
