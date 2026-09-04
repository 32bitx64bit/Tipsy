// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package runtime installs the official Android x86-64 client into the XDG data dir.
package runtime

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// RuntimeDir is $XDG_DATA_HOME/tipsy/runtime (see config.Paths).
func RuntimeDir() string {
	return filepath.Join(config.Paths().DataDir, "runtime")
}

// Setup extracts official APKs / splits / nested packages into RuntimeDir.
func Setup(ctx context.Context, apkPaths []string) (*apk.ExtractResult, error) {
	if len(apkPaths) == 0 {
		return nil, fmt.Errorf("pass official APK/dir")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dest := RuntimeDir()
	_, migration, err := prepareAppStorage(dest)
	if err != nil {
		return nil, fmt.Errorf("persistent app storage: %w", err)
	}
	logAppStorageMigration(migration)
	logging.Logger(logging.CatRuntime).Info("setup", "dest", dest, "inputs", len(apkPaths))
	res, err := apk.Extract(ctx, apkPaths, dest)
	if err != nil {
		return nil, err
	}
	if res != nil && res.AssetsDir != "" {
		if perr := EnsureOfficialPatches(ctx, res.AssetsDir); perr != nil {
			return nil, fmt.Errorf("official ExtraContent: %w", perr)
		}
	}
	return res, nil
}
