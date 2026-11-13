// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package app

import (
	"errors"
	"fmt"
	"io"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

const DevelopmentUnrestrictedWarning = "WARNING: DevelopmentUnrestricted mode is active; this is not an OfficialVerified Tipsy session."

var ErrDevelopmentConsentRequired = errors.New("official release authority is unavailable; run once with --development to explicitly authorize a non-official (local or source) build")

func parseLaunchDevelopmentFlag(arg string) bool {
	return arg == "--development"
}

func parseSetupFlags(args []string) (flags, bool, error) {
	var ordinary []string
	development := false
	for _, arg := range args {
		if arg == "--development" {
			development = true
			continue
		}
		ordinary = append(ordinary, arg)
	}
	f, err := parseFlags(ordinary)
	return f, development, err
}

func setupHelpText() string {
	return setupHelp
}

func launchHelpText() string {
	return launchHelp
}

func loadConfigWithDevelopmentConsent(requested bool) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if !requested || cfg.DevelopmentApproved() {
		return cfg, nil
	}
	// Recording consent is a read-modify-write: Update holds the cross-process
	// config lock across load→mutate→save so a concurrent `config set` on the
	// same file cannot be clobbered by this stale snapshot.
	err = config.Update(func(c *config.Config) error {
		c.ApproveDevelopment()
		cfg = c
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("record explicit development authorization: %w", err)
	}
	return cfg, nil
}

func resolveFallbackAuthority(cfg *config.Config) (authorityResolution, error) {
	if cfg != nil && cfg.DevelopmentApproved() {
		return authorityResolution{Mode: setupsvc.DevelopmentUnrestricted, Trust: setupsvc.DevelopmentTrustPolicy()}, nil
	}
	return authorityResolution{}, ErrDevelopmentConsentRequired
}

func fallbackPackageAuthorizationTrust(err error) (setupsvc.TrustPolicy, bool) {
	if errors.Is(err, ErrDevelopmentConsentRequired) {
		return setupsvc.DevelopmentTrustPolicy(), true
	}
	return setupsvc.TrustPolicy{}, false
}

func isDevelopmentConsentRequired(err error) bool {
	return errors.Is(err, ErrDevelopmentConsentRequired)
}

func authorityWarningForMode(mode setupsvc.AuthorizationMode) string {
	if mode == setupsvc.DevelopmentUnrestricted {
		return DevelopmentUnrestrictedWarning
	}
	return ""
}

func printAuthorityWarning(w io.Writer, mode setupsvc.AuthorizationMode) {
	if mode == setupsvc.DevelopmentUnrestricted {
		fmt.Fprintln(w, DevelopmentUnrestrictedWarning)
	}
}

func allowLegacyDevelopmentAPK(mode setupsvc.AuthorizationMode) bool {
	return mode == setupsvc.DevelopmentUnrestricted
}

func validateGenerationModeAuthorization(mode setupsvc.AuthorizationMode, authorization setupsvc.Authorization) error {
	if mode == setupsvc.OfficialVerified && !authorization.PolicyAuthorized {
		return fmt.Errorf("official runtime generation lacks authenticated policy authorization")
	}
	if mode == setupsvc.DevelopmentUnrestricted && authorization.PolicyAuthorized {
		return fmt.Errorf("development runtime generation claims official policy authorization")
	}
	return nil
}

func setupCommand(mode setupsvc.AuthorizationMode) string {
	if mode == setupsvc.OfficialVerified {
		return "tipsy setup <official-apk-or-dir>"
	}
	return "tipsy setup --development <official-apk-or-dir>"
}
