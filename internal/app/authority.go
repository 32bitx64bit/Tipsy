// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

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

// resolveAuthority is the single app-level trust decision. A binary compiled
// with -tags official_release and stamped with official release metadata is
// OfficialVerified using the compiled Roblox signer floor. Cryptographic
// admission stays in release CI and in the package manager's signature check
// at install; runtime markers such as build-info.json never grant authority.
// Local builds of any medium and developer wraps require explicit
// --development consent.
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
	return resolveFallbackAuthority(cfg)
}

func generationStoreRoot() string {
	return setupsvc.GenerationStoreDir(filepath.Clean(runtimeDir()))
}

var runtimeDir = func() string {
	return filepath.Join(config.Paths().DataDir, "runtime")
}
