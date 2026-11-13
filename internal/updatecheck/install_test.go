// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

import (
	"errors"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/desktop"
)

func TestDetectInstallWithMedium(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		medium desktop.Medium
		owned  map[string]bool
		want   Method
	}{
		{name: "appimage", medium: desktop.MediumAppImage, want: AppImage},
		{name: "flatpak", medium: desktop.MediumFlatpak, want: Flatpak},
		{name: "source", medium: desktop.MediumSource, want: Source},
		{name: "deb", medium: desktop.MediumSystem, owned: map[string]bool{"dpkg": true}, want: Deb},
		{name: "rpm", medium: desktop.MediumSystem, owned: map[string]bool{"rpm": true}, want: RPM},
		{name: "pacman", medium: desktop.MediumSystem, owned: map[string]bool{"pacman": true}, want: Pacman},
		{name: "unknown system", medium: desktop.MediumSystem, want: SystemUnknown},
		{name: "unknown medium", medium: desktop.MediumUnknown, want: SystemUnknown},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps := DetectDeps{
				Medium:     func() (desktop.Medium, string) { return tt.medium, "" },
				Executable: func() (string, error) { return "/usr/bin/tipsy", nil },
				OwnedBy: func(manager, executable string) bool {
					return tt.owned[manager]
				},
			}
			if got := DetectInstallWith(deps); got != tt.want {
				t.Fatalf("DetectInstallWith = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectInstallQueriesExecutableWithoutDeletedSuffix(t *testing.T) {
	t.Parallel()

	var gotPath string
	deps := DetectDeps{
		Medium:     func() (desktop.Medium, string) { return desktop.MediumSystem, "" },
		Executable: func() (string, error) { return "/usr/bin/tipsy (deleted)", nil },
		OwnedBy: func(manager, executable string) bool {
			gotPath = executable
			return manager == "rpm"
		},
	}
	if got := DetectInstallWith(deps); got != RPM {
		t.Fatalf("DetectInstallWith = %q, want %q", got, RPM)
	}
	if gotPath != "/usr/bin/tipsy" {
		t.Fatalf("ownership path = %q", gotPath)
	}
}

func TestDetectInstallHandlesUnavailableExecutable(t *testing.T) {
	t.Parallel()

	deps := DetectDeps{
		Medium:     func() (desktop.Medium, string) { return desktop.MediumSystem, "" },
		Executable: func() (string, error) { return "", errors.New("no executable") },
		OwnedBy:    func(manager, executable string) bool { return true },
	}
	if got := DetectInstallWith(deps); got != SystemUnknown {
		t.Fatalf("DetectInstallWith = %q, want %q", got, SystemUnknown)
	}
}

func TestInstructions(t *testing.T) {
	t.Parallel()

	release := Release{
		HTMLURL: "https://github.com/32bitx64bit/Tipsy/releases/tag/v1.5.0",
		Assets: []Asset{
			{Name: "checksums.txt", BrowserDownloadURL: "https://example.test/checksums.txt"},
			{Name: "Tipsy-1.5.0-aarch64.AppImage", BrowserDownloadURL: "https://example.test/aarch64.AppImage"},
			{Name: "Tipsy-1.5.0-x86_64.AppImage", BrowserDownloadURL: "https://example.test/Tipsy-1.5.0-x86_64.AppImage"},
		},
	}

	tests := []struct {
		name    string
		method  Method
		command string
		url     string
	}{
		{name: "appimage", method: AppImage, url: "https://example.test/Tipsy-1.5.0-x86_64.AppImage"},
		{name: "flatpak", method: Flatpak, command: "flatpak update " + FlatpakID},
		{name: "deb", method: Deb, command: "apt"},
		{name: "rpm", method: RPM, command: "dnf"},
		{name: "pacman", method: Pacman, command: "pacman"},
		{name: "source", method: Source},
		{name: "unknown system", method: SystemUnknown},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message, command, url := Instructions(tt.method, release)
			if message == "" {
				t.Fatal("message is empty")
			}
			if tt.command == "" {
				if command != "" {
					t.Fatalf("command = %q, want empty", command)
				}
			} else if !strings.Contains(command, tt.command) {
				t.Fatalf("command = %q, want it to contain %q", command, tt.command)
			}
			if url != tt.url {
				t.Fatalf("url = %q, want %q", url, tt.url)
			}
		})
	}
}

func TestInstructionsAppImageFallsBackToReleasePage(t *testing.T) {
	t.Parallel()

	release := Release{HTMLURL: "https://github.com/32bitx64bit/Tipsy/releases/tag/v1.5.0"}
	_, _, url := Instructions(AppImage, release)
	if url != release.HTMLURL {
		t.Fatalf("url = %q, want %q", url, release.HTMLURL)
	}
}
