// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package android

import "testing"

func TestEGLFocusedForegroundRestoresGuestStateAndReusesUploads(t *testing.T) {
	if !testEGLForegroundFixture() {
		t.Fatal("fake EGL/GLES foreground contract failed")
	}
}
