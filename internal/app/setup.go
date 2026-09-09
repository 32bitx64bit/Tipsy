// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"

	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

// PackageAuthorizationTrust is the package-install trust policy for a Tipsy
// session. It uses the same authority decision as launch, except that a
// source or development AppImage without persisted consent still authorizes
// the official Roblox APK as DevelopmentUnrestricted instead of claiming
// OfficialVerified with no authenticated release or Roblox policy.
//
// A broken official AppImage still fails closed; that path never becomes
// development package authority.
func PackageAuthorizationTrust(ctx context.Context, approveDevelopment bool) (setupsvc.TrustPolicy, error) {
	return packageAuthorizationTrust(ctx, approveDevelopment, defaultAuthorityDependencies())
}

func packageAuthorizationTrust(ctx context.Context, approveDevelopment bool, deps authorityDependencies) (setupsvc.TrustPolicy, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := loadConfigWithDevelopmentConsent(approveDevelopment)
	if err != nil {
		return setupsvc.TrustPolicy{}, err
	}
	installedVersion := int64(0)
	if snapshot, snapshotErr := setupsvc.New().Snapshot(ctx); snapshotErr == nil {
		installedVersion = snapshot.VersionCode
	}
	authority, err := resolveAuthority(ctx, cfg, installedVersion, deps)
	if err == nil {
		return authority.Trust, nil
	}
	if errors.Is(err, ErrDevelopmentConsentRequired) {
		return setupsvc.DevelopmentTrustPolicy(), nil
	}
	return setupsvc.TrustPolicy{}, err
}
