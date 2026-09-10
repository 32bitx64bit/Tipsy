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

// robloxUserAgentWinInet is the only `Roblox/` token in official
// libroblox.so 2.736.1408 (read-only strings). Ticket redeem already sends
// the same header. Owner-licensed Playable-Devices spoof: InitParams
// and HTTP use this Windows-player UA instead of the Android/tipsy
// suffix. Client-settings URL stays AndroidApp.
const robloxUserAgentWinInet = "Roblox/WinInet"

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

// robloxUserAgent is the HTTP / InitParams UA. VersionName is accepted
// so callers keep the installed APK version for DeviceParams; the UA
// itself is the licensed WinInet token, not an Android form-factor
// string.
func robloxUserAgent(versionName string) string {
	_ = versionName
	return robloxUserAgentWinInet
}
