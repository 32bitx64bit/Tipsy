// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/apk"
)

const robloxUserAgentSuffix = " (Linux; Android 8.0.0; tipsy)"

// installedVersionName returns meta.json versionName from an extracted
// runtime directory. Missing or empty meta is honest: callers get "".
func installedVersionName(runtimeDir string) string {
	if strings.TrimSpace(runtimeDir) == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(runtimeDir, "meta.json"))
	if err != nil {
		return ""
	}
	var meta apk.Meta
	if json.Unmarshal(raw, &meta) != nil {
		return ""
	}
	return strings.TrimSpace(meta.VersionName)
}

// robloxUserAgent is the HTTP / InitParams UA the Android client sends.
// An empty versionName becomes "0" so callers still send a UA without
// inventing a previous APK version.
func robloxUserAgent(versionName string) string {
	v := strings.TrimSpace(versionName)
	if v == "" {
		v = "0"
	}
	return "Roblox/" + v + robloxUserAgentSuffix
}
