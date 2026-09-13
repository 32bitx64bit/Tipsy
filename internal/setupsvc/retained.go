// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
)

// DeriveAndActivateRetainedAPKs migrates only explicitly selected signed APK
// files into a fresh authenticated generation. It never reads or blesses
// mutable legacy libraries, assets, metadata, or account storage.
func DeriveAndActivateRetainedAPKs(ctx context.Context, retainedPaths []string, storeRoot string, trust TrustPolicy) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(retainedPaths) == 0 || storeRoot == "" {
		return "", fmt.Errorf("setup: retained APK migration requires package paths and a generation store")
	}
	parent := filepath.Dir(storeRoot)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".retained-apk-migration-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return "", err
	}
	frozen, err := copyAndValidateLocal(ctx, retainedPaths, filepath.Join(stage, "frozen"), DefaultLimits(), nil)
	if err != nil {
		return "", err
	}
	frozen, err = keepX86PackageFiles(frozen, filepath.Join(stage, "filtered"), "migrate retained APK")
	if err != nil {
		return "", err
	}
	report, err := apk.Inspect(ctx, frozen)
	if err != nil {
		return "", err
	}
	if err := apk.VerifyReportSignatures(ctx, report); err != nil {
		return "", fmt.Errorf("setup: retained APK signature verification: %w", err)
	}
	if _, err := AuthorizeReport(report, trust); err != nil {
		return "", err
	}
	source := filepath.Join(stage, "runtime")
	if _, err := apk.Extract(ctx, frozen, source); err != nil {
		return "", err
	}
	id, err := PrepareGeneration(ctx, source, storeRoot, report, trust)
	if err != nil {
		return "", err
	}
	store := StoreForTrust(storeRoot, trust)
	generation, err := integrity.OpenGeneration(ctx, storeRoot, id)
	if err != nil {
		return "", err
	}
	authorized, err := authorizeGeneration(ctx, generation, trust, nil)
	if err != nil {
		_ = generation.Close()
		return "", err
	}
	if err := authorized.Close(); err != nil {
		return "", err
	}
	if err := store.Activate(ctx, id); err != nil {
		return "", err
	}
	active, err := OpenAuthorizedGeneration(ctx, store, trust)
	if err != nil {
		return "", fmt.Errorf("setup: verify migrated active generation: %w", err)
	}
	defer active.Close()
	if active.ID() != id {
		return "", fmt.Errorf("setup: migrated active generation identity changed")
	}
	return id, nil
}
