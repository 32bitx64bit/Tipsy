// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import "github.com/tipsy-linux/tipsy/internal/syssoname"

// IsSystemSoname reports whether a DT_NEEDED entry names an Android system
// library that Open and OpenFD serve only through the Resolver. Any other
// DT_NEEDED entry must be a bundled APK library that the loader maps and
// searches (lookupLoaded) before it asks the Resolver for an import. Static
// callers use this to classify imports exactly as the loader would, without
// mapping or running anything. The set lives in internal/syssoname so setup
// preflight can read it without importing the loader.
func IsSystemSoname(soname string) bool {
	return syssoname.Is(soname)
}
