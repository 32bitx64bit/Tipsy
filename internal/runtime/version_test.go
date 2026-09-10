// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import "testing"

func TestRobloxUserAgentIsWinInet(t *testing.T) {
	// The licensed UA is version-independent: installedVersionName was removed
	// after the user-agent simplification, and callers pass "" or the installed
	// versionName for DeviceParams without changing the header.
	for _, version := range []string{"", "2.736.1408"} {
		if got := robloxUserAgent(version); got != robloxUserAgentWinInet {
			t.Fatalf("robloxUserAgent(%q)=%q", version, got)
		}
	}
}
