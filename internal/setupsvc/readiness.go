// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/integrity"
)

// verifyInstalledClientSnapshot replays the security-sensitive package and
// generation checks before any frontend may call the client installed/ready.
// Its positive verdict is limited to authenticated launch inputs.
func verifyInstalledClientSnapshot(ctx context.Context, storeRoot string, configured TrustPolicy) (InstallSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	store := integrity.Store{Root: storeRoot}
	generation, err := store.Active(ctx)
	if err != nil {
		return InstallSnapshot{}, err
	}
	trust, err := readinessTrust(generation.Inventory, configured)
	if err != nil {
		_ = generation.Close()
		return InstallSnapshot{}, err
	}
	authorized, err := authorizeGeneration(ctx, generation, trust, nil)
	if err != nil {
		_ = generation.Close()
		return InstallSnapshot{}, err
	}
	absStore, err := filepath.Abs(storeRoot)
	if err != nil {
		_ = authorized.Close()
		return InstallSnapshot{}, fmt.Errorf("setup: resolve generation store: %w", err)
	}
	authorized.storeRoot = absStore
	defer authorized.Close()

	set, err := authorized.NativeDescriptorSet(ctx)
	if err != nil {
		return InstallSnapshot{}, fmt.Errorf("setup: recheck authenticated native payload: %w", err)
	}
	descriptors, err := set.Descriptors()
	if err != nil {
		return InstallSnapshot{}, fmt.Errorf("setup: validate authenticated native payload: %w", err)
	}
	var root *integrity.NativeDescriptor
	for i := range descriptors {
		descriptor := &descriptors[i]
		if descriptor.SONAME != "libroblox.so" {
			continue
		}
		if root != nil || descriptor.APKEntry != "lib/x86_64/libroblox.so" || descriptor.ExpectedSize <= 0 {
			return InstallSnapshot{}, setupError(ErrMissingX8664, "installation readiness", "the authenticated native payload has an invalid x86-64 libroblox root", nil)
		}
		root = descriptor
	}
	if root == nil {
		return InstallSnapshot{}, setupError(ErrMissingX8664, "installation readiness", "the authenticated native payload omits x86-64 libroblox.so", nil)
	}

	runtimeFiles, err := authorized.AuthorizedRuntimeFiles(ctx)
	if err != nil {
		return InstallSnapshot{}, fmt.Errorf("setup: recheck authenticated runtime assets: %w", err)
	}
	if runtimeFiles.GenerationID != authorized.ID() || runtimeFiles.RootDir == "" || runtimeFiles.AssetsDir == "" || runtimeFiles.BaseAPKPath == "" {
		return InstallSnapshot{}, fmt.Errorf("setup: authenticated runtime file handoff is incomplete")
	}
	if err := validateOpenedClientCompatibility(ctx, root.File.File, root.ExpectedSize); err != nil {
		return InstallSnapshot{}, err
	}
	// Recheck the same pinned descriptor after analysis so a concurrent write
	// cannot earn readiness from a transient byte sequence.
	if err := root.File.Recheck(ctx); err != nil {
		return InstallSnapshot{}, fmt.Errorf("setup: root client changed during compatibility validation: %w", err)
	}

	inventory := authorized.generation.Inventory
	return InstallSnapshot{
		Installed:     true,
		Readiness:     ReadinessLaunchInputs,
		RuntimeDir:    runtimeFiles.RootDir,
		PackageName:   inventory.PackageName,
		VersionName:   inventory.VersionName,
		VersionCode:   inventory.VersionCode,
		Architectures: []string{"x86_64"},
	}, nil
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
		if configured.Mode == OfficialVerified && configured.RobloxPolicy != nil {
			return configured, nil
		}
		return TrustPolicy{}, setupError(ErrPolicy, "installation readiness", "the active official generation requires its authenticated Roblox policy", nil)
	default:
		return TrustPolicy{}, setupError(ErrIntegrity, "installation readiness", "the active generation has an unknown authorization mode", nil)
	}
}
