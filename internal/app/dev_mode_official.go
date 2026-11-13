// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package app

import (
	"errors"
	"fmt"
	"io"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

var (
	ErrOfficialReleaseRequired = errors.New("official release authority is unavailable; unauthenticated builds cannot authorize runtime sessions")
	errDevelopmentDisabled     = errors.New("development authorization is disabled in official release builds")
)

const officialSetupHelp = `Usage: tipsy setup <apk-or-dir> [...]

Cryptographically verify and atomically install official Roblox APKs, split
APKs, or .apkm/.xapk/.apks/.zip sets. Pass regular package files obtained
through your own authorized account. Never commit Roblox packages.
`

const officialLaunchHelp = `Usage: tipsy launch [--probe] [uri]

Load an authenticated Android x86-64 runtime generation. Run tipsy setup first.

  --probe   Load libroblox.so, run JNI_OnLoad, then exit (no game loop)
  uri       Optional Roblox website or protocol URI (roblox-player:,
            roblox://experiences/start, or https://www.roblox.com/games/...)

A website Play URI that includes an official authentication ticket signs the
Android session in through the same private cookie store as in-app login.
Ticket and cookie values are never logged.
`

func parseLaunchDevelopmentFlag(string) bool {
	return false
}

func parseSetupFlags(args []string) (flags, bool, error) {
	f, err := parseFlags(args)
	return f, false, err
}

func setupHelpText() string {
	return officialSetupHelp
}

func launchHelpText() string {
	return officialLaunchHelp
}

func loadConfigWithDevelopmentConsent(requested bool) (*config.Config, error) {
	if requested {
		return nil, errDevelopmentDisabled
	}
	return config.Load()
}

func resolveFallbackAuthority(*config.Config) (authorityResolution, error) {
	return authorityResolution{}, ErrOfficialReleaseRequired
}

func fallbackPackageAuthorizationTrust(error) (setupsvc.TrustPolicy, bool) {
	return setupsvc.TrustPolicy{}, false
}

func isDevelopmentConsentRequired(error) bool {
	return false
}

func authorityWarningForMode(setupsvc.AuthorizationMode) string {
	return ""
}

func printAuthorityWarning(io.Writer, setupsvc.AuthorizationMode) {}

func allowLegacyDevelopmentAPK(setupsvc.AuthorizationMode) bool {
	return false
}

func validateGenerationModeAuthorization(mode setupsvc.AuthorizationMode, authorization setupsvc.Authorization) error {
	if mode != setupsvc.OfficialVerified || authorization.Mode != setupsvc.OfficialVerified {
		return fmt.Errorf("official release builds require OfficialVerified runtime generation authorization")
	}
	if !authorization.PolicyAuthorized {
		return fmt.Errorf("official runtime generation lacks authenticated policy authorization")
	}
	return nil
}

func setupCommand(setupsvc.AuthorizationMode) string {
	return "tipsy setup <official-apk-or-dir>"
}
