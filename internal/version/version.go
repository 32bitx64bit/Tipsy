// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package version

import "strings"

// Version is 0.0.0-dev unless overridden at link time:
//
//	go build -ldflags "-X github.com/tipsy-linux/tipsy/internal/version.Version=1.2.3"
var Version = "0.0.0-dev"

// ChannelStable and ChannelDevelopment are the two release channels a build
// can belong to. The channel decides which desktop identity the build
// integrates under (Tipsy vs Tipsy-Dev) so a developer's day-to-day install
// and their work-in-progress builds never fight over the same launcher.
const (
	ChannelStable      = "stable"
	ChannelDevelopment = "dev"
)

// Channel is dev unless the official packaging stamps stable at link time:
//
//	go build -ldflags "-X github.com/tipsy-linux/tipsy/internal/version.Channel=stable"
//
// An unstamped `go build` is therefore always a development build.
var Channel = ChannelDevelopment

func String() string {
	return Version
}

// Development reports whether this binary is a development-channel build.
func Development() bool {
	return Channel != ChannelStable
}

// ReleaseKind is the release medium stamped at link time by the official
// build scripts. It is empty in an unstamped `go build`. The value alone
// never grants official identity: IsOfficialBuild also requires the
// official_release build tag, the stable channel, and a release version.
//
//	go build -ldflags "-X github.com/tipsy-linux/tipsy/internal/version.ReleaseKind=release-candidate-keyless"
var ReleaseKind = ""

// SourceCommit is the full Git commit the binary was built from, stamped at
// link time by the official build scripts. It is provenance metadata for
// diagnostics; it is empty in an unstamped `go build`.
//
//	go build -ldflags "-X github.com/tipsy-linux/tipsy/internal/version.SourceCommit=<40-hex-commit>"
var SourceCommit = ""

// Release kinds the official build scripts stamp. Every local build of every
// medium is development-unrestricted (or unstamped).
const (
	ReleaseKindDevelopment = "development-unrestricted"
	// build-appdir.sh --mode official: release-candidate AppImage, no cosign.
	ReleaseKindCandidateUnsigned = "release-candidate-unsigned"
	// build-appdir.sh --mode github-signed: cosign keyless AppImage.
	ReleaseKindCandidateKeyless = "release-candidate-keyless"
	// build-tipsy-binaries.sh --official / repository packaging: built for
	// the GPG-signed APT/RPM/pacman/OSTree repository.
	ReleaseKindRepositorySigned = "release-repository-signed"
)

func officialReleaseKind(kind string) bool {
	switch kind {
	case ReleaseKindCandidateUnsigned, ReleaseKindCandidateKeyless, ReleaseKindRepositorySigned:
		return true
	default:
		return false
	}
}

// isReleaseVersion reports whether v is a stamped release version rather than
// a development placeholder ("dev", the default 0.0.0-dev, or any -dev tag).
func isReleaseVersion(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "dev" && !strings.Contains(v, "-dev")
}

// IsOfficialBuild reports whether this binary is an official Tipsy release.
// It is true only when the binary was compiled with -tags official_release
// AND the official build scripts stamped valid release metadata at link time
// (stable channel, an official ReleaseKind, and a non-dev Version). Runtime
// markers such as build-info.json or TIPSY_RELEASE_* environment variables
// never influence it: a local `go build` cannot claim official identity.
func IsOfficialBuild() bool {
	if !officialReleaseCompiled {
		return false
	}
	return Channel == ChannelStable && isReleaseVersion(Version) && officialReleaseKind(ReleaseKind)
}

// UnofficialBuildLabel marks every build that is not an official release in
// version output, logs, window chrome, and Tipsy's own User-Agent.
const UnofficialBuildLabel = "Unofficial / Local Build"

// Provenance is the human-readable release identity for logs and UI chrome.
func Provenance() string {
	if IsOfficialBuild() {
		return "Official Release"
	}
	return UnofficialBuildLabel
}

// UserAgent is Tipsy's own HTTP User-Agent for requests to GitHub and other
// Tipsy services. Unofficial builds carry the UnofficialBuildLabel comment so
// they cannot present as official release traffic. This is not Roblox's
// Roblox/WinInet client UA, which stays untouched.
func UserAgent() string {
	if IsOfficialBuild() {
		return "Tipsy-Linux"
	}
	return "Tipsy-Linux (" + UnofficialBuildLabel + ")"
}
