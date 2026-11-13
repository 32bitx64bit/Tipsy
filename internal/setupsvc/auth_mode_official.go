// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package setupsvc

import (
	"fmt"
	"os"

	"github.com/tipsy-linux/tipsy/internal/integrity"
)

func validateAuthorizationPolicy(policy TrustPolicy) (AuthorizationMode, error) {
	if policy.Mode != OfficialVerified {
		return "", setupError(ErrPolicy, "validate package", "package authorization mode is invalid", nil)
	}
	if !policy.ReleaseAuthenticated {
		return "", setupError(ErrPolicy, "validate package", "official verification requires an authenticated release", nil)
	}
	return OfficialVerified, nil
}

func authorizeLineage(mode AuthorizationMode, allowedCerts map[string]bool, terminus string) (string, uint64, bool, error) {
	if mode != OfficialVerified {
		return "", 0, false, setupError(ErrPolicy, "validate package", "package authorization mode is invalid", nil)
	}
	if !allowedCerts[terminus] {
		return "", 0, false, setupError(ErrUntrustedSigner, "validate package", "the authenticated Roblox policy does not authorize the verified signer lineage", nil)
	}
	return "compiled-keyless-release", 0, true, nil
}

func activeSlotForTrust(_ TrustPolicy) string {
	return integrity.ActiveSlotOfficial
}

func readinessTrust(inventory integrity.Inventory, configured TrustPolicy) (TrustPolicy, error) {
	if configured.Mode != OfficialVerified || !configured.ReleaseAuthenticated {
		return TrustPolicy{}, setupError(ErrPolicy, "installation readiness", "official verification requires an authenticated release", nil)
	}
	if AuthorizationMode(inventory.AuthorizationMode) != OfficialVerified {
		return TrustPolicy{}, setupError(ErrPolicy, "installation readiness", "the active generation has an unauthorized mode", nil)
	}
	if inventory.SignerLineageID == "compiled-keyless-release" && inventory.PolicyAuthorized && inventory.PolicySequence == 0 {
		if configured.PackageName != "" {
			return configured, nil
		}
		return KeylessReleaseTrustPolicy(), nil
	}
	return TrustPolicy{}, setupError(ErrPolicy, "installation readiness", "the active official generation requires an authenticated release", nil)
}

func inspectActiveClientForTrust(storeRoot string, trust TrustPolicy) (activeClient, error) {
	if trust.Mode != OfficialVerified {
		return activeClient{}, setupError(ErrPolicy, "inspect client imports", "package authorization mode is invalid", nil)
	}
	store := StoreForTrust(storeRoot, trust)
	active, err := readActiveClientRecord(store.Root, "active.json")
	if err != nil {
		return activeClient{}, err
	}
	if active.inventory.AuthorizationMode != string(OfficialVerified) || !active.inventory.PolicyAuthorized {
		return activeClient{}, fmt.Errorf("integrity: official active record points at an unauthorized generation: %w", os.ErrNotExist)
	}
	return active, nil
}
