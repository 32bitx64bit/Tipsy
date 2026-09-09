// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	artifactEnvironment = "TIPSY_RELEASE_ARTIFACT"
	appDirEnvironment   = "TIPSY_RELEASE_APPDIR"
	maxBuildInfoBytes   = int64(16 << 10)
)

// errOfficialReleaseUnavailable means this process was not started by AppRun
// from a non-development AppImage payload.
var errOfficialReleaseUnavailable = errors.New("official AppImage identity is unavailable outside a GitHub AppImage")

type payloadBuildInfo struct {
	Format      string `json:"format"`
	ReleaseKind string `json:"releaseKind"`
}

func identifyOfficialAppImage(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	artifactPath := strings.TrimSpace(os.Getenv(artifactEnvironment))
	if artifactPath == "" {
		return errOfficialReleaseUnavailable
	}
	if !filepath.IsAbs(artifactPath) {
		return fmt.Errorf("release artifact path must be absolute")
	}
	if err := validateAppImageProcess(); err != nil {
		return fmt.Errorf("validate AppImage process: %w", err)
	}
	return officialReleaseKind(payloadReleaseKind())
}

func officialReleaseKind(kind string) error {
	if kind == "" || kind == "development-unrestricted" {
		return errOfficialReleaseUnavailable
	}
	return nil
}

func validateAppImageProcess() error {
	appDir := strings.TrimSpace(os.Getenv(appDirEnvironment))
	if appDir == "" || !filepath.IsAbs(appDir) {
		return errors.New("AppImage mount directory is unavailable")
	}
	info, err := os.Lstat(appDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("AppImage mount directory is unsafe")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve running executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve running executable links: %w", err)
	}
	binDir := filepath.Join(appDir, "usr", "bin")
	relative, err := filepath.Rel(binDir, executable)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || (relative != "tipsy" && relative != "tipsy-gui") {
		return errors.New("running executable is outside the AppImage payload")
	}
	return nil
}

func payloadReleaseKind() string {
	appDir := strings.TrimSpace(os.Getenv(appDirEnvironment))
	if appDir == "" || !filepath.IsAbs(appDir) {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(appDir, "usr", "share", "tipsy", "build-info.json"))
	if err != nil || int64(len(raw)) > maxBuildInfoBytes {
		return ""
	}
	var info payloadBuildInfo
	if json.Unmarshal(raw, &info) != nil || info.Format != "tipsy.build-info.v1" {
		return ""
	}
	return info.ReleaseKind
}
