// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/version"
)

// stampOfficialRelease points the linker-stamped metadata at a valid official
// release and restores it afterwards. Whether that actually yields official
// identity depends on the build: only -tags official_release binaries can
// ever be official (version.IsOfficialBuild).
func stampOfficialRelease(t *testing.T) {
	t.Helper()
	prevVersion, prevChannel, prevKind, prevCommit := version.Version, version.Channel, version.ReleaseKind, version.SourceCommit
	t.Cleanup(func() {
		version.Version, version.Channel, version.ReleaseKind, version.SourceCommit = prevVersion, prevChannel, prevKind, prevCommit
	})
	version.Version = "1.2.3"
	version.Channel = version.ChannelStable
	version.ReleaseKind = version.ReleaseKindCandidateKeyless
	version.SourceCommit = strings.Repeat("a", 40)
}

// unstampOfficialRelease restores the unstamped local-build metadata.
func unstampOfficialRelease(t *testing.T) {
	t.Helper()
	prevVersion, prevChannel, prevKind, prevCommit := version.Version, version.Channel, version.ReleaseKind, version.SourceCommit
	t.Cleanup(func() {
		version.Version, version.Channel, version.ReleaseKind, version.SourceCommit = prevVersion, prevChannel, prevKind, prevCommit
	})
	version.Version = "0.0.0-dev"
	version.Channel = version.ChannelDevelopment
	version.ReleaseKind = ""
	version.SourceCommit = ""
}

func TestIdentifyOfficialReleaseIsCompileTime(t *testing.T) {
	unstampOfficialRelease(t)
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("unstamped build err=%v, want unavailable", err)
	}

	stampOfficialRelease(t)
	err := identifyOfficialRelease(context.Background())
	if version.IsOfficialBuild() {
		if err != nil {
			t.Fatalf("stamped official_release build must be official: %v", err)
		}
	} else if !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("local build with stamped metadata must stay unavailable, got %v", err)
	}
}

func TestIdentifyOfficialReleaseRejectsDevMetadata(t *testing.T) {
	stampOfficialRelease(t)
	for _, mutate := range []func(){
		func() { version.Channel = version.ChannelDevelopment },
		func() { version.Version = "dev" },
		func() { version.Version = "0.0.0-dev" },
		func() { version.Version = "1.2.3-dev" },
		func() { version.ReleaseKind = "" },
		func() { version.ReleaseKind = version.ReleaseKindDevelopment },
		func() { version.ReleaseKind = "release-official" },
	} {
		stampOfficialRelease(t)
		mutate()
		if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
			t.Fatalf("invalid official metadata err=%v, want unavailable", err)
		}
	}
}

func TestIdentifyOfficialReleaseRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := identifyOfficialRelease(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context err=%v", err)
	}
}

// Forged runtime markers — an AppRun-style env handoff and an official
// build-info.json — must never grant official identity to a build that was
// not compiled and stamped as an official release.
func TestForgedRuntimeMarkersNeverGrantOfficialIdentity(t *testing.T) {
	appDir := t.TempDir()
	infoPath := filepath.Join(appDir, "usr", "share", "tipsy", "build-info.json")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	forged := `{"format":"tipsy.build-info.v1","releaseKind":"release-candidate-keyless","version":"1.2.3"}`
	if err := os.WriteFile(infoPath, []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIPSY_RELEASE_ARTIFACT", filepath.Join(appDir, "Tipsy.AppImage"))
	t.Setenv("TIPSY_RELEASE_APPDIR", appDir)

	unstampOfficialRelease(t)
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("forged markers granted official identity: %v", err)
	}

	// In an official_release build the identity comes from the stamped
	// metadata alone: markers pointing at nonexistent paths change nothing.
	stampOfficialRelease(t)
	t.Setenv("TIPSY_RELEASE_ARTIFACT", "/nonexistent/Tipsy.AppImage")
	t.Setenv("TIPSY_RELEASE_APPDIR", "/nonexistent/appdir")
	err := identifyOfficialRelease(context.Background())
	if version.IsOfficialBuild() {
		if err != nil {
			t.Fatalf("official identity must not depend on runtime markers: %v", err)
		}
	} else if !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("local build err=%v, want unavailable", err)
	}
}

func TestOfficialReleaseKindAcceptsOnlyKnownOfficialKinds(t *testing.T) {
	for _, kind := range []string{releaseKindCandidateUnsigned, releaseKindCandidateKeyless, releaseKindRepositorySigned} {
		if err := officialReleaseKind(kind); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	for _, kind := range []string{"", releaseKindDevelopment, "release-official", "stable"} {
		if err := officialReleaseKind(kind); !errors.Is(err, errOfficialReleaseUnavailable) {
			t.Errorf("%q must be unavailable, got %v", kind, err)
		}
	}
}
