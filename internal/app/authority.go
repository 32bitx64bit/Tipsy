// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

var ErrDevelopmentConsentRequired = errors.New("official release authority is unavailable; run once with --development to explicitly authorize a non-official (local or source) build")

type authorityResolution struct {
	Mode  setupsvc.AuthorizationMode
	Trust setupsvc.TrustPolicy
}

type authorityDependencies struct {
	identifyOfficialRelease func(context.Context) error
}

func defaultAuthorityDependencies() authorityDependencies {
	return authorityDependencies{identifyOfficialRelease: identifyOfficialRelease}
}

// resolveAuthority is the single app-level trust decision. A GitHub-built
// artifact — an AppImage that AppRun launched from its own payload, the
// Flatpak, or a root-owned repository package — whose build-info.json carries
// an official releaseKind is OfficialVerified using the compiled Roblox
// signer floor. Cryptographic admission stays in release CI (cosign) and in
// the package manager's signature check at install; the running binary cannot
// usefully attest itself. Local builds of any medium and developer wraps
// require explicit --development consent.
func resolveAuthority(ctx context.Context, cfg *config.Config, deps authorityDependencies) (authorityResolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return authorityResolution{}, err
	}
	if deps.identifyOfficialRelease != nil {
		if err := deps.identifyOfficialRelease(ctx); err == nil {
			return authorityResolution{Mode: setupsvc.OfficialVerified, Trust: setupsvc.KeylessReleaseTrustPolicy()}, nil
		} else if !errors.Is(err, errOfficialReleaseUnavailable) {
			return authorityResolution{}, fmt.Errorf("official release identity: %w", err)
		}
	}
	if cfg != nil && cfg.DevelopmentApproved() {
		return authorityResolution{Mode: setupsvc.DevelopmentUnrestricted, Trust: setupsvc.DevelopmentTrustPolicy()}, nil
	}
	return authorityResolution{}, ErrDevelopmentConsentRequired
}

func generationStoreRoot() string {
	return setupsvc.GenerationStoreDir(filepath.Clean(runtimeDir()))
}

// runtimeDir is factored for app tests while retaining the XDG installation location.
var runtimeDir = func() string {
	return filepath.Join(config.Paths().DataDir, "runtime")
}
