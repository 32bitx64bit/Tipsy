// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

// robloxUserAgentWinInet is the only `Roblox/` token in official
// libroblox.so 2.736.1408 (read-only strings). Ticket redeem already sends
// the same header. Owner-licensed Playable-Devices spoof: InitParams
// and HTTP use this Windows-player UA instead of the Android/tipsy
// suffix. Client-settings URL stays AndroidApp.
const robloxUserAgentWinInet = "Roblox/WinInet"

// robloxUserAgent is the HTTP / InitParams UA. VersionName is accepted
// so callers keep the installed APK version for DeviceParams; the UA
// itself is the licensed WinInet token, not an Android form-factor
// string.
func robloxUserAgent(versionName string) string {
	_ = versionName
	return robloxUserAgentWinInet
}
