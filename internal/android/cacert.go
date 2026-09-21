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

// cacertRelativePath is the only path remapped by the bionic open/openat
// shim. The official client opens exactly this path. Exact-match only: no
// prefixes, no globs, no broad rewrite.
const cacertRelativePath = "./exe/cacert.pem"

var (
	cacertMu          sync.RWMutex
	cacertOverride    string
	cacertOverrideSet bool
)

// SetCABundlePath pins the absolute bundle the shim redirects the exact
// relative CA request to.
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

// defaultCABundlePath derives the installed bundle location from the Android
// FilesDir; the shim only reads the bytes the runtime copies there before
// native startup.
func defaultCABundlePath() string {
	return filepath.Join(config.Paths().DataDir, "app-data", "com.roblox.client", "files", "exe", "cacert.pem")
}

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
