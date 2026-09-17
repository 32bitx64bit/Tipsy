// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package android

import "testing"

func TestEGLFocusedForegroundRestoresGuestStateAndReusesUploads(t *testing.T) {
	// Live compose, state restore, and the acquired==0 texture delete stay
	// required. A follow-up unpublished swap must not GetCurrent or acquire.
	if !testEGLForegroundFixture() {
		t.Fatal("fake EGL/GLES foreground contract failed")
	}
}
