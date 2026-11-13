// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"

	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

// PackageAuthorizationTrust is the package-install trust policy for a Tipsy
// session. Official release builds require authenticated release identity and
// fail closed otherwise; non-official builds allow development package trust
// only when compiled without official_release.
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
	authority, err := resolveAuthority(ctx, cfg, deps)
	if err == nil {
		return authority.Trust, nil
	}
	if trust, ok := fallbackPackageAuthorizationTrust(err); ok {
		return trust, nil
	}
	return setupsvc.TrustPolicy{}, err
}
