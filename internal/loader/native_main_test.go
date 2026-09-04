// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import "testing"

func TestCall0RunsOnNativeMain(t *testing.T) {
	EnsureNativeMain()
	if OnNativeMain() {
		t.Fatal("test goroutine should not be the native main pthread")
	}
	call0(testMarkMainAddr())
	if !testTookMain() {
		t.Fatal("tipsy_call0 did not run the target on the native main pthread")
	}
}
