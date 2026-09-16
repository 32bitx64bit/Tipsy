// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package android

import "testing"

func TestEGLFocusedForegroundRestoresGuestStateAndReusesUploads(t *testing.T) {
	got := testEGLForegroundFixture()
	if !got.passed {
		t.Fatalf("fake EGL/GLES foreground fixture failed: %+v", got)
	}
	if got.acquireCalls != 6 || got.releaseCalls != 5 || got.drawCalls != 3 {
		t.Fatalf("lease/draw operations=%+v want acquire=6 release=5 draw=3", got)
	}
	if got.textureAllocations != 1 || got.fullTextureUploads != 1 ||
		got.sameSizeTextureUpdates != 1 || got.geometryUploads != 1 {
		t.Fatalf("steady-state operation counts=%+v want one allocation/full upload/same-size update/geometry upload", got)
	}
	if got.guestStateRestoreFailures != 0 || got.drawContractFailures != 0 {
		t.Fatalf("guest state or premultiplied draw contract was violated: %+v", got)
	}
	if got.preservedBufferDraws != 0 || got.contextMismatchAcquires != 0 ||
		got.transformFeedbackDraws != 0 {
		t.Fatalf("fail-closed eligibility result=%+v", got)
	}
	if got.cacheTextureDeletions != 1 || got.liveStateRecords != 0 {
		t.Fatalf("focus/surface cleanup result=%+v", got)
	}
	if got.gles2Draws != 1 || got.gles2StateRestoreFailures != 0 {
		t.Fatalf("GLES2 client-array restoration result=%+v", got)
	}
}
