// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/version"
)

// The AppRun handoff (TIPSY_RELEASE_ARTIFACT / TIPSY_RELEASE_APPDIR) and the
// payload build-info.json are packaging evidence only. Authority comes from
// the compile-time official_release identity, so no AppImage-shaped runtime
// marker can make a local build official.

func TestAppImageHandoffWithoutOfficialBuildStaysUnavailable(t *testing.T) {
	unstampOfficialRelease(t)
	t.Setenv("TIPSY_RELEASE_ARTIFACT", "")
	t.Setenv("TIPSY_RELEASE_APPDIR", "")
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("no handoff err=%v", err)
	}

	appDir := t.TempDir()
	infoPath := filepath.Join(appDir, "usr", "share", "tipsy", "build-info.json")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte(`{"format":"tipsy.build-info.v1","releaseKind":"release-candidate-keyless"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIPSY_RELEASE_ARTIFACT", filepath.Join(appDir, "Tipsy-1.2.3-x86_64.AppImage"))
	t.Setenv("TIPSY_RELEASE_APPDIR", appDir)
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("forged AppImage handoff err=%v, want unavailable", err)
	}
}

func TestAppImageDevelopmentPayloadNeverGrantsAuthority(t *testing.T) {
	unstampOfficialRelease(t)
	appDir := t.TempDir()
	infoPath := filepath.Join(appDir, "usr", "share", "tipsy", "build-info.json")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte(`{"format":"tipsy.build-info.v1","releaseKind":"development-unrestricted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIPSY_RELEASE_APPDIR", appDir)
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("development payload err=%v", err)
	}
}

func TestOfficialAppImageIdentityIgnoresPayloadMarkers(t *testing.T) {
	stampOfficialRelease(t)
	// No handoff at all: a stamped official_release binary is official even
	// when run outside an AppImage mount.
	t.Setenv("TIPSY_RELEASE_ARTIFACT", "")
	t.Setenv("TIPSY_RELEASE_APPDIR", "")
	err := identifyOfficialRelease(context.Background())
	if version.IsOfficialBuild() {
		if err != nil {
			t.Fatalf("stamped official_release build must be official without markers: %v", err)
		}
	} else if !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("local build err=%v, want unavailable", err)
	}
}
