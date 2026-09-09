// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "os"

func diagnosticsEnabled() bool {
	return os.Getenv("TIPSY_DIAG") == "1"
}
