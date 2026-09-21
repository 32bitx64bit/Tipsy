// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

// robloxUserAgentWinInet is the Windows-player `Roblox/` UA token shared by
// InitParams and HTTP. It is version-independent; client-settings URLs still
// use the Android form factor.
const robloxUserAgentWinInet = "Roblox/WinInet"

// robloxUserAgent returns the WinInet UA token regardless of versionName;
// callers retain versionName only for DeviceParams.
func robloxUserAgent(versionName string) string {
	_ = versionName
	return robloxUserAgentWinInet
}
