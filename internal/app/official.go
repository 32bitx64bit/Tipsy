// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tipsy-linux/tipsy/internal/desktop"
)

// releaseKind values the build scripts write into build-info.json. Every
// local build of every medium is development-unrestricted; only GitHub
// Actions passes the mode that produces the other kinds.
const (
	releaseKindDevelopment = "development-unrestricted"
	// build-appdir.sh --mode official: release-candidate AppImage, no cosign.
	releaseKindCandidateUnsigned = "release-candidate-unsigned"
	// build-appdir.sh --mode github-signed: cosign keyless AppImage.
	releaseKindCandidateKeyless = "release-candidate-keyless"
	// build-deb/rpm/flatpak.sh --mode official: built for the GPG-signed
	// APT/RPM/OSTree repository; apt, dnf and flatpak verify that signature
	// when the package is installed.
	releaseKindRepositorySigned = "release-repository-signed"
)

// errOfficialReleaseUnavailable means this process is not running from a
// GitHub-built artifact: no AppRun handoff, and not installed as the Flatpak
// or a root-owned repository package that carries an official build-info.
var errOfficialReleaseUnavailable = errors.New("official release identity is unavailable outside a GitHub-built AppImage, Flatpak, or repository package")

func officialReleaseKind(kind string) error {
	switch kind {
	case releaseKindCandidateUnsigned, releaseKindCandidateKeyless, releaseKindRepositorySigned:
		return nil
	default:
		return errOfficialReleaseUnavailable
	}
}

// identifyOfficialRelease is the app-level release identity. The AppImage
// branch keeps its AppRun contract; otherwise the executable must live in one
// of the trusted install roots below next to an official build-info.json.
// None of this authenticates the binary cryptographically (that stays in
// release CI and in the package manager's signature check at install time);
// it stops a local build from ever presenting itself as an official release.
func identifyOfficialRelease(ctx context.Context) error {
	err := identifyOfficialAppImage(ctx)
	if err == nil || !errors.Is(err, errOfficialReleaseUnavailable) {
		return err
	}
	return identifyOfficialInstall(ctx)
}

// installRoot is a location only a package manager writes to.
type installRoot struct {
	medium    string
	binDir    string
	buildInfo string
	// flatpakApp requires /.flatpak-info to name this application: the
	// binary was installed by flatpak under our ID, and /app is read-only.
	flatpakApp string
	// rootOwned requires the executable and build-info to be owned by root
	// and not writable by anyone else: the shape dpkg/rpm leave behind, and
	// one a user's own prefix install never has.
	rootOwned bool
}

var officialInstallRoots = []installRoot{
	{medium: "flatpak", binDir: "/app/bin", buildInfo: "/app/share/tipsy/build-info.json", flatpakApp: desktop.Base},
	{medium: "package", binDir: "/usr/bin", buildInfo: "/usr/share/tipsy/build-info.json", rootOwned: true},
}

// Seams for tests; production uses the real process and filesystem.
var (
	flatpakInfoPath   = "/.flatpak-info"
	currentExecutable = os.Executable
	ownedByRoot       = func(info os.FileInfo) bool {
		st, ok := info.Sys().(*syscall.Stat_t)
		return ok && st.Uid == 0
	}
)

func identifyOfficialInstall(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exe, err := currentExecutable()
	if err != nil {
		return fmt.Errorf("resolve running executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	name := filepath.Base(exe)
	if name != "tipsy" && name != "tipsy-gui" {
		return errOfficialReleaseUnavailable
	}
	for _, root := range officialInstallRoots {
		if filepath.Dir(exe) != filepath.Clean(root.binDir) {
			continue
		}
		return root.identify(exe)
	}
	return errOfficialReleaseUnavailable
}

func (root installRoot) identify(exe string) error {
	if root.flatpakApp != "" && flatpakApplication() != root.flatpakApp {
		return errOfficialReleaseUnavailable
	}
	info, err := os.Lstat(root.buildInfo)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Repackaged or hand-installed without our marker: honest dev.
			return errOfficialReleaseUnavailable
		}
		return fmt.Errorf("%s build-info: %w", root.medium, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxBuildInfoBytes {
		return fmt.Errorf("%s build-info is not a small regular file", root.medium)
	}
	if root.rootOwned {
		exeInfo, err := os.Lstat(exe)
		if err != nil {
			return fmt.Errorf("%s executable: %w", root.medium, err)
		}
		for _, candidate := range []os.FileInfo{exeInfo, info} {
			if !ownedByRoot(candidate) || candidate.Mode().Perm()&0o022 != 0 {
				return errOfficialReleaseUnavailable
			}
		}
	}
	raw, err := os.ReadFile(root.buildInfo)
	if err != nil {
		return fmt.Errorf("%s build-info: %w", root.medium, err)
	}
	var parsed payloadBuildInfo
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Format != "tipsy.build-info.v1" {
		// A marker exists but is not ours: fail closed rather than fall back
		// to development consent, like a broken AppImage payload.
		return fmt.Errorf("%s build-info is not a tipsy.build-info.v1 document", root.medium)
	}
	return officialReleaseKind(parsed.ReleaseKind)
}

// flatpakApplication returns the [Application] name from /.flatpak-info, or
// "" outside a Flatpak sandbox.
func flatpakApplication() string {
	f, err := os.Open(flatpakInfoPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	inApplication := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inApplication = line == "[Application]"
			continue
		}
		if !inApplication {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && strings.TrimSpace(key) == "name" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
