// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// officialFixture stands up fake /app and /usr trees and points the
// identity seams at them. rootOwned decides what the ownership check says.
type officialFixture struct {
	root      string
	rootOwned bool
}

func newOfficialFixture(t *testing.T) *officialFixture {
	t.Helper()
	f := &officialFixture{root: t.TempDir(), rootOwned: true}
	prevRoots, prevInfo, prevExe, prevOwner := officialInstallRoots, flatpakInfoPath, currentExecutable, ownedByRoot
	t.Cleanup(func() {
		officialInstallRoots, flatpakInfoPath, currentExecutable, ownedByRoot = prevRoots, prevInfo, prevExe, prevOwner
	})
	officialInstallRoots = []installRoot{
		{medium: "flatpak", binDir: f.path("app/bin"), buildInfo: f.path("app/share/tipsy/build-info.json"), flatpakApp: "io.github.tipsy_linux.Tipsy"},
		{medium: "package", binDir: f.path("usr/bin"), buildInfo: f.path("usr/share/tipsy/build-info.json"), rootOwned: true},
	}
	flatpakInfoPath = f.path(".flatpak-info")
	ownedByRoot = func(os.FileInfo) bool { return f.rootOwned }
	t.Setenv(artifactEnvironment, "")
	return f
}

func (f *officialFixture) path(rel string) string {
	return filepath.Join(f.root, filepath.FromSlash(rel))
}

func (f *officialFixture) write(t *testing.T, rel, body string, mode os.FileMode) string {
	t.Helper()
	path := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f *officialFixture) runFrom(rel string) {
	exe := f.path(rel)
	currentExecutable = func() (string, error) { return exe, nil }
}

const officialRepositoryInfo = `{"architecture":"x86_64","format":"tipsy.build-info.v1","medium":"flatpak","name":"Tipsy","releaseKind":"release-repository-signed","version":"1.2.0"}`
const developmentInfo = `{"format":"tipsy.build-info.v1","releaseKind":"development-unrestricted"}`

func TestIdentifyOfficialReleaseAcceptsGitHubBuiltFlatpak(t *testing.T) {
	f := newOfficialFixture(t)
	f.write(t, "app/bin/tipsy-gui", "elf", 0o755)
	f.write(t, "app/share/tipsy/build-info.json", officialRepositoryInfo, 0o644)
	f.write(t, ".flatpak-info", "[Application]\nname=io.github.tipsy_linux.Tipsy\nruntime=runtime/org.kde.Platform/x86_64/6.10\n", 0o644)
	f.runFrom("app/bin/tipsy-gui")
	if err := identifyOfficialRelease(context.Background()); err != nil {
		t.Fatalf("published Flatpak must be official: %v", err)
	}

	// The same files under a different Flatpak ID (someone else's manifest)
	// are not our release.
	f.write(t, ".flatpak-info", "[Application]\nname=org.example.Fork\n", 0o644)
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("foreign Flatpak ID err=%v", err)
	}
	// Outside a sandbox /app/bin is just a directory.
	os.Remove(f.path(".flatpak-info"))
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("no .flatpak-info err=%v", err)
	}
}

func TestIdentifyOfficialReleaseLocalFlatpakBuildStaysDevelopment(t *testing.T) {
	f := newOfficialFixture(t)
	f.write(t, "app/bin/tipsy", "elf", 0o755)
	f.write(t, "app/share/tipsy/build-info.json", developmentInfo, 0o644)
	f.write(t, ".flatpak-info", "[Application]\nname=io.github.tipsy_linux.Tipsy\n", 0o644)
	f.runFrom("app/bin/tipsy")
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("build-flatpak.sh default mode err=%v, want development fallback", err)
	}
}

func TestIdentifyOfficialReleaseAcceptsRootOwnedRepositoryPackage(t *testing.T) {
	f := newOfficialFixture(t)
	f.write(t, "usr/bin/tipsy", "elf", 0o755)
	f.write(t, "usr/share/tipsy/build-info.json", officialRepositoryInfo, 0o644)
	f.runFrom("usr/bin/tipsy")
	if err := identifyOfficialRelease(context.Background()); err != nil {
		t.Fatalf("dpkg/rpm install must be official: %v", err)
	}

	// Writable by group or others: not what a package manager leaves behind.
	f.write(t, "usr/share/tipsy/build-info.json", officialRepositoryInfo, 0o666)
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("world-writable build-info err=%v", err)
	}
	f.write(t, "usr/share/tipsy/build-info.json", officialRepositoryInfo, 0o644)

	// Not owned by root: a copied marker in a user-owned /usr overlay.
	f.rootOwned = false
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("non-root owner err=%v", err)
	}
	f.rootOwned = true

	// Repackaged without our marker: honest development fallback.
	os.Remove(f.path("usr/share/tipsy/build-info.json"))
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("missing build-info err=%v", err)
	}
}

func TestIdentifyOfficialReleaseFailsClosedOnForeignBuildInfo(t *testing.T) {
	f := newOfficialFixture(t)
	f.write(t, "usr/bin/tipsy-gui", "elf", 0o755)
	f.write(t, "usr/share/tipsy/build-info.json", `{"format":"someone-else","releaseKind":"release-repository-signed"}`, 0o644)
	f.runFrom("usr/bin/tipsy-gui")
	err := identifyOfficialRelease(context.Background())
	if err == nil || errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("a marker that is not ours must be a hard error, not development fallback: %v", err)
	}
}

func TestIdentifyOfficialReleaseIgnoresPrefixAndSourceBuilds(t *testing.T) {
	f := newOfficialFixture(t)
	// ~/.local/bin install with a copied official marker next to it.
	f.write(t, "home/.local/bin/tipsy", "elf", 0o755)
	f.write(t, "home/.local/share/tipsy/build-info.json", officialRepositoryInfo, 0o644)
	f.runFrom("home/.local/bin/tipsy")
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("prefix install err=%v", err)
	}
	// A differently named binary in /usr/bin is not Tipsy's contract either.
	f.write(t, "usr/bin/tipsy-fork", "elf", 0o755)
	f.write(t, "usr/share/tipsy/build-info.json", officialRepositoryInfo, 0o644)
	f.runFrom("usr/bin/tipsy-fork")
	if err := identifyOfficialRelease(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("foreign binary name err=%v", err)
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
