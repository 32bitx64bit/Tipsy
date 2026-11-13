// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package syssoname

import "testing"

func TestIs(t *testing.T) {
	for name, want := range map[string]bool{
		"libc.so":                 true,
		"/system/lib64/libm.so":   true,
		"libGLESv3.so":            true,
		"libc++.so":               true,
		"libroblox.so":            false,
		"libc.so.6":               false,
		"":                        false,
		"/system/lib64/libfoo.so": false,
		"libc.so/":                false,
	} {
		if got := Is(name); got != want {
			t.Errorf("Is(%q)=%v want %v", name, got, want)
		}
	}
}
