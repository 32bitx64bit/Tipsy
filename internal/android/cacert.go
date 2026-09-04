// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include <stdlib.h>
*/
import "C"

import (
	"path/filepath"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/config"
)

// cacertRelativePath is the ONLY filesystem input remapped by the bionic
// open/openat shim (see bionic_compat.c). The official client opens exactly
// "./exe/cacert.pem" (stable Landing FLog: 231/231 cacert mentions carry the
// "./" prefix; the bare "exe/cacert.pem" form was never observed, so it is
// intentionally NOT mapped). Exact-match only: no prefixes, no globs, no
// broad rewrite.
const cacertRelativePath = "./exe/cacert.pem"

var (
	cacertMu          sync.RWMutex
	cacertOverride    string
	cacertOverrideSet bool
)

// SetCABundlePath pins the absolute FilesDir bundle the shim redirects the
// exact relative CA request to. It is a test seam and the documented wiring
// point for a future runtime owner (launch.go currently cannot be touched by
// the Android ABI pass); production otherwise derives the same path from the
// existing config plumbing via defaultCABundlePath.
func SetCABundlePath(p string) {
	cacertMu.Lock()
	defer cacertMu.Unlock()
	cacertOverride = p
	cacertOverrideSet = true
}

// ResetCABundlePath clears an override set by SetCABundlePath.
func ResetCABundlePath() {
	cacertMu.Lock()
	defer cacertMu.Unlock()
	cacertOverride = ""
	cacertOverrideSet = false
}

// defaultCABundlePath derives the installed official-APK bundle location from
// the stable, version-independent Android FilesDir. Runtime copies the
// official assets/ssl/cacert.pem bytes there before native startup; the shim
// only ever reads those bytes.
func defaultCABundlePath() string {
	return filepath.Join(config.Paths().DataDir, "app-data", "com.roblox.client", "files", "exe", "cacert.pem")
}

// cacertBundlePath returns the explicit override when set, else the
// config-derived default.
func cacertBundlePath() string {
	cacertMu.RLock()
	defer cacertMu.RUnlock()
	if cacertOverrideSet {
		return cacertOverride
	}
	return defaultCABundlePath()
}

//export GoAndroid_CACertBundle
func GoAndroid_CACertBundle() *C.char {
	p := cacertBundlePath()
	if p == "" {
		return nil
	}
	return C.CString(p)
}
