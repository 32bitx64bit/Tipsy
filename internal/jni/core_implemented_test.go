// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

// TestImplementedMethodsCoverCoreHandlers keeps the hand-maintained
// implementedMethods list honest: every registerCore identity must be
// reported implemented so GetMethodID never logs a missing method for a
// method the dispatcher answers.
func TestImplementedMethodsCoverCoreHandlers(t *testing.T) {
	for key := range coreHandlers {
		i := strings.IndexByte(key, '(')
		if i <= 0 {
			t.Fatalf("malformed core handler key %q", key)
		}
		name, sig := key[:i], key[i:]
		if !implementedMethods[key] && lookupCoreHandler(name, sig) == nil {
			t.Errorf("core handler %s missing from implementedMethods", key)
		}
		if !isImplementedMethod(name, sig) {
			t.Errorf("core handler %s not reported implemented", key)
		}
	}
}
