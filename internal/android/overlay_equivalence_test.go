// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import "testing"

// The overlay must render byte-identical pixels whether its GL capabilities are
// cached or queried every frame, and the cache may only remove capability
// queries: every guest-mutable state query must still happen live.
func TestFocusedTextOverlayPixelsIgnoreCapabilityCache(t *testing.T) {
	code, saved := testEGLTextOverlayPixelFixture()
	switch code {
	case 0:
	case -1:
		t.Skip("no headless Mesa EGL/GLES2 context on this host; pixel equivalence " +
			"needs a real GL driver and cannot be proven here")
	default:
		t.Fatalf("overlay pixel equivalence failed with code %d "+
			"(1/2=no GL, 3=empty frame, 100+n=query run, 200=pixels differ, "+
			"201=state queries skipped, 202=capability cache inactive)", code)
	}
	if saved <= 0 {
		t.Fatalf("capability cache saved %d queries, want > 0", saved)
	}
}

func TestEGLForegroundAfterPixelFixture(t *testing.T) {
	code, _ := testEGLTextOverlayPixelFixture()
	if code == -1 {
		t.Skip("no headless Mesa EGL/GLES2 context")
	}
	if code != 0 {
		t.Fatalf("pixel fixture failed: %d", code)
	}
	if !testEGLForegroundFixture() {
		t.Fatal("foreground fixture failed after pixel fixture")
	}
}

// A terminated display frees every context and surface it owns, so a reused
// display handle must not revive cached overlay capabilities.
func TestEGLTextOverlayTerminateDropsCaps(t *testing.T) {
	if code := testEGLTextOverlayTerminateDropsCaps(); code != 0 {
		t.Fatalf("eglTerminate cache invalidation fixture failed: %d "+
			"(1=seed, 2=terminate result, 3=terminate not forwarded, "+
			"4=caps survived, 5=reused display kept caps, "+
			"6=other display dropped, 7=not wrapped)", code)
	}
}
