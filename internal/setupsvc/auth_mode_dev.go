// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package setupsvc

import (
	"fmt"
	"os"

	"github.com/tipsy-linux/tipsy/internal/integrity"
)

const DevelopmentUnrestricted AuthorizationMode = "development-unrestricted"

// DevelopmentTrustPolicy is an explicit non-official boundary for source
// builds. It never yields official status and still requires APK signatures
// plus a full rotation lineage whose oldest and current certificates are in
// the compiled signer floor.
func DevelopmentTrustPolicy() TrustPolicy {
	trust := OfficialTrustPolicy()
	trust.Mode = DevelopmentUnrestricted
	return trust
}

func validateAuthorizationPolicy(policy TrustPolicy) (AuthorizationMode, error) {
	mode := policy.Mode
	if mode == "" {
		mode = OfficialVerified
	}
	if mode != OfficialVerified && mode != DevelopmentUnrestricted {
		return "", setupError(ErrPolicy, "validate package", "package authorization mode is invalid", nil)
	}
	if mode == OfficialVerified && !policy.ReleaseAuthenticated {
		return "", setupError(ErrPolicy, "validate package", "official verification requires an authenticated release", nil)
	}
	return mode, nil
}

func authorizeLineage(mode AuthorizationMode, allowedCerts map[string]bool, terminus string) (string, uint64, bool, error) {
	if mode == DevelopmentUnrestricted {
		if !allowedCerts[terminus] {
			return "", 0, false, setupError(ErrUntrustedSigner, "validate package", "the development signer lineage does not terminate in the compiled Roblox identity", nil)
		}
		return "compiled-development", 0, false, nil
	}
	if !allowedCerts[terminus] {
		return "", 0, false, setupError(ErrUntrustedSigner, "validate package", "the authenticated Roblox policy does not authorize the verified signer lineage", nil)
	}
	return "compiled-keyless-release", 0, true, nil
}

func activeSlotForTrust(trust TrustPolicy) string {
	switch trust.Mode {
	case OfficialVerified:
		return integrity.ActiveSlotOfficial
	case DevelopmentUnrestricted:
		return integrity.ActiveSlotDevelopment
	default:
		return ""
	}
}

func readinessTrust(inventory integrity.Inventory, configured TrustPolicy) (TrustPolicy, error) {
	switch AuthorizationMode(inventory.AuthorizationMode) {
	case DevelopmentUnrestricted:
		if configured.Mode == DevelopmentUnrestricted && configured.PackageName != "" {
			return configured, nil
		}
		return DevelopmentTrustPolicy(), nil
	case OfficialVerified:
		if inventory.SignerLineageID == "compiled-keyless-release" && inventory.PolicyAuthorized && inventory.PolicySequence == 0 {
			// This does not authenticate the surrounding Tipsy executable. It
			// only replays the compiled Roblox package identity check and must
			// never be used as app release authority.
			return KeylessReleaseTrustPolicy(), nil
		}
		return TrustPolicy{}, setupError(ErrPolicy, "installation readiness", "the active official generation requires an authenticated release", nil)
	default:
		return TrustPolicy{}, setupError(ErrIntegrity, "installation readiness", "the active generation has an unknown authorization mode", nil)
	}
}

func inspectActiveClientForTrust(storeRoot string, trust TrustPolicy) (activeClient, error) {
	store := StoreForTrust(storeRoot, trust)
	if store.ActiveSlot == integrity.ActiveSlotDevelopment {
		active, err := readActiveClientRecord(store.Root, "active-development.json")
		if err == nil {
			return active, nil
		}
		legacy, legacyErr := readActiveClientRecord(store.Root, "active.json")
		if legacyErr == nil && legacy.inventory.AuthorizationMode == string(DevelopmentUnrestricted) {
			return legacy, nil
		}
		return activeClient{}, err
	}
	active, err := readActiveClientRecord(store.Root, "active.json")
	if err != nil {
		return activeClient{}, err
	}
	if store.ActiveSlot == integrity.ActiveSlotOfficial && active.inventory.AuthorizationMode == string(DevelopmentUnrestricted) {
		return activeClient{}, fmt.Errorf("integrity: official active record points at a development generation: %w", os.ErrNotExist)
	}
	return active, nil
}
