// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import "testing"

func TestRobloxUserAgentIsWinInet(t *testing.T) {
	// The UA is version-independent: any versionName yields the same header.
	for _, version := range []string{"", "2.736.1408"} {
		if got := robloxUserAgent(version); got != robloxUserAgentWinInet {
			t.Fatalf("robloxUserAgent(%q)=%q", version, got)
		}
	}
}
