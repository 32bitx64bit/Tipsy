// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

type launchGeneration interface {
	runtime.AuthorizedGeneration
	Authorization() setupsvc.Authorization
	Close() error
}

type generationDependencies struct {
	open   func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error)
	derive func(context.Context, []string, string, setupsvc.TrustPolicy) (string, error)
	launch func(context.Context, runtime.LaunchOptions) error
}

func defaultGenerationDependencies() generationDependencies {
	return generationDependencies{
		open: func(ctx context.Context, store integrity.Store, trust setupsvc.TrustPolicy) (launchGeneration, error) {
			return setupsvc.OpenAuthorizedGeneration(ctx, store, trust)
		},
		derive: setupsvc.DeriveAndActivateRetainedAPKs,
		launch: runtime.Launch,
	}
}

func openOrRepairGeneration(ctx context.Context, authority authorityResolution, deps generationDependencies) (launchGeneration, error) {
	if deps.open == nil || deps.derive == nil {
		return nil, fmt.Errorf("runtime generation integration is incomplete")
	}
	storeRoot := generationStoreRoot()
	store := setupsvc.StoreForTrust(storeRoot, authority.Trust)
	generation, openErr := deps.open(ctx, store, authority.Trust)
	if openErr == nil {
		if err := requireGenerationMode(generation, authority.Mode); err == nil {
			return generation, nil
		}
		if generation != nil {
			_ = generation.Close()
		}
	}
	retained, err := retainedAPKCandidates(storeRoot, authority.Mode == setupsvc.DevelopmentUnrestricted)
	if err != nil {
		return nil, fmt.Errorf("inspect retained package set: %w", err)
	}
	retained, err = selectRetainedAPK(retained)
	if err != nil {
		return nil, fmt.Errorf("inspect retained package set: %w", err)
	}
	if len(retained) == 0 {
		if openErr != nil {
			return nil, fmt.Errorf("runtime not set up; run: %s: %w", setupCommand(authority.Mode), openErr)
		}
		return nil, fmt.Errorf("runtime generation authorization differs from the selected mode")
	}
	if _, err := deps.derive(ctx, retained, storeRoot, authority.Trust); err != nil {
		return nil, fmt.Errorf("repair authenticated runtime generation: %w", err)
	}
	generation, err = deps.open(ctx, store, authority.Trust)
	if err != nil {
		return nil, fmt.Errorf("open repaired authenticated runtime generation: %w", err)
	}
	if err := requireGenerationMode(generation, authority.Mode); err != nil {
		_ = generation.Close()
		return nil, err
	}
	return generation, nil
}

func requireGenerationMode(generation launchGeneration, mode setupsvc.AuthorizationMode) error {
	if generation == nil {
		return fmt.Errorf("authorized generation is nil")
	}
	authorization := generation.Authorization()
	if authorization.Mode != mode {
		return fmt.Errorf("runtime generation authorization differs from the selected mode")
	}
	if mode == setupsvc.OfficialVerified && !authorization.PolicyAuthorized {
		return fmt.Errorf("official runtime generation lacks authenticated policy authorization")
	}
	if mode == setupsvc.DevelopmentUnrestricted && authorization.PolicyAuthorized {
		return fmt.Errorf("development runtime generation claims official policy authorization")
	}
	return nil
}

var retainedBlobName = regexp.MustCompile(`^[a-f0-9]{64}\.apk$`)

func retainedAPKCandidates(storeRoot string, allowLegacyDevelopment bool) ([]string, error) {
	retainedDir := filepath.Join(storeRoot, "apks", "sha256")
	paths, err := regularAPKFiles(retainedDir, true)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(paths) != 0 || !allowLegacyDevelopment {
		return paths, nil
	}
	// This is a one-way development migration source only. The selected files
	// are cryptographically reverified and copied into a fresh generation;
	// neither their path nor the legacy tree becomes launch authority.
	legacyDir := filepath.Join(runtimeDir(), "apk")
	paths, err = regularAPKFiles(legacyDir, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return paths, err
}

func regularAPKFiles(dir string, contentAddressed bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if contentAddressed {
			if !retainedBlobName.MatchString(name) {
				continue
			}
		} else if !strings.EqualFold(filepath.Ext(name), ".apk") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("retained package candidate is not a regular file")
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func setupCommand(mode setupsvc.AuthorizationMode) string {
	if mode == setupsvc.OfficialVerified {
		return "tipsy setup <official-apk-or-dir>"
	}
	return "tipsy setup --development <official-apk-or-dir>"
}

// selectRetainedAPK keeps a single package for repair. Content-addressed
// blobs are named {sha256}.apk, so passing every retained version as a split
// set makes keepX86FromSplitSet look for base.apk and fail.
func selectRetainedAPK(paths []string) ([]string, error) {
	if len(paths) <= 1 {
		return paths, nil
	}
	best := ""
	var bestTime time.Time
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("retained package candidate is not a regular file")
		}
		if best == "" || info.ModTime().After(bestTime) || (info.ModTime().Equal(bestTime) && path > best) {
			best = path
			bestTime = info.ModTime()
		}
	}
	return []string{best}, nil
}
