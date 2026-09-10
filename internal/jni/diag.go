// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"os"
	"sync/atomic"
)

// diagnosticsOn caches TIPSY_DIAG=1 once at process start so hot dispatch
// paths (findClass and the stub fallback) never call os.Getenv.
var diagnosticsOn atomic.Bool

func init() {
	if os.Getenv("TIPSY_DIAG") == "1" {
		diagnosticsOn.Store(true)
	}
}

func diagnosticsEnabled() bool {
	return diagnosticsOn.Load()
}
