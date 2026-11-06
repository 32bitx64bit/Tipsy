// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package updatecheck

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/desktop"
)

const (
	// FlatpakID is the application ID used by the Flatpak build.
	FlatpakID = "io.github.tipsy_linux.Tipsy"
	// PackageName is the native package name used by deb, rpm and pacman.
	PackageName = "tipsy"

	ownershipTimeout = 3 * time.Second
)

// Method is how the running Tipsy binary was installed.
type Method string

const (
	AppImage      Method = "appimage"
	Flatpak       Method = "flatpak"
	Deb           Method = "deb"
	RPM           Method = "rpm"
	Pacman        Method = "pacman"
	SystemUnknown Method = "system-unknown"
	Source        Method = "source"
)

// InstallMethod is an alias for Method.
type InstallMethod = Method

// DetectDeps overrides the environment probes DetectInstallWith uses. Nil
// fields fall back to the real desktop, executable and package-manager checks.
type DetectDeps struct {
	Medium     func() (desktop.Medium, string)
	Executable func() (string, error)
	OwnedBy    func(manager, executable string) bool
}

var (
	packageManagers = []struct {
		name   string
		method Method
	}{
		{"dpkg", Deb},
		{"rpm", RPM},
		{"pacman", Pacman},
	}
)

// DetectInstall reports how the running binary was installed. It starts from
// desktop.CurrentMedium and, for a system install, asks dpkg/rpm/pacman which
// package owns os.Executable.
func DetectInstall() Method {
	return DetectInstallWith(DetectDeps{})
}

// DetectInstallWith is DetectInstall with injectable probes for tests.
func DetectInstallWith(deps DetectDeps) Method {
	medium := desktop.CurrentMedium
	if deps.Medium != nil {
		medium = deps.Medium
	}
	executable := os.Executable
	if deps.Executable != nil {
		executable = deps.Executable
	}
	ownedBy := packageOwned
	if deps.OwnedBy != nil {
		ownedBy = deps.OwnedBy
	}

	current, _ := medium()
	switch current {
	case desktop.MediumAppImage:
		return AppImage
	case desktop.MediumFlatpak:
		return Flatpak
	case desktop.MediumSource:
		return Source
	case desktop.MediumSystem:
		path, err := executable()
		if err != nil {
			return SystemUnknown
		}
		path = strings.TrimSuffix(path, " (deleted)")
		if path == "" {
			return SystemUnknown
		}
		for _, manager := range packageManagers {
			if ownedBy(manager.name, path) {
				return manager.method
			}
		}
		return SystemUnknown
	default:
		return SystemUnknown
	}
}

func packageOwned(manager, executable string) bool {
	var args []string
	switch manager {
	case "dpkg":
		args = []string{"-S", executable}
	case "rpm":
		args = []string{"-qf", executable}
	case "pacman":
		args = []string{"-Qo", executable}
	default:
		return false
	}
	if _, err := exec.LookPath(manager); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownershipTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, manager, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// Instructions describes how to update one install method. command is empty
// when Tipsy cannot safely update that method itself. url is the AppImage asset
// for AppImage installs, and empty otherwise.
func Instructions(method Method, release Release) (message, command, url string) {
	switch method {
	case AppImage:
		return "A newer Tipsy release is available. Download the latest AppImage.", "", appImageURL(release)
	case Flatpak:
		return "A newer Tipsy release is available. Update it with Flatpak.",
			"flatpak update " + FlatpakID, ""
	case Deb:
		return "A newer Tipsy release is available. Upgrade the Debian package.",
			"sudo apt update && sudo apt install --only-upgrade " + PackageName, ""
	case RPM:
		return "A newer Tipsy release is available. Upgrade the RPM package.",
			"sudo dnf upgrade " + PackageName, ""
	case Pacman:
		return "A newer Tipsy release is available. Upgrade the Arch package.",
			"sudo pacman -Syu " + PackageName, ""
	case Source:
		return "This is a source build. Rebuild from the latest source to update.", "", ""
	default:
		return "Tipsy is installed as a system package. Update it with your distribution's package manager.", "", ""
	}
}

func appImageURL(release Release) string {
	var fallback string
	for _, asset := range release.Assets {
		if asset.BrowserDownloadURL == "" {
			continue
		}
		name := strings.ToLower(asset.Name)
		if !strings.HasSuffix(name, ".appimage") {
			continue
		}
		if strings.Contains(name, "x86_64") || strings.Contains(name, "amd64") {
			return asset.BrowserDownloadURL
		}
		if fallback == "" {
			fallback = asset.BrowserDownloadURL
		}
	}
	if fallback != "" {
		return fallback
	}
	return release.HTMLURL
}
