// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import "testing"

// TestWindowResizeUpdatesGeometry pins the ANativeWindow resize contract:
// Resize changes getWidth/getHeight exactly once to the requested positive
// geometry, preserves the buffer format, and rejects invalid sizes without
// mutating the window.
func TestWindowResizeUpdatesGeometry(t *testing.T) {
	w := NewWindow(1280, 720, nil)
	if gw, gh := w.Size(); gw != 1280 || gh != 720 {
		t.Fatalf("NewWindow size = %dx%d, want 1280x720", gw, gh)
	}
	if err := w.Resize(1920, 1080); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if gw, gh := w.Size(); gw != 1920 || gh != 1080 {
		t.Fatalf("post-resize size = %dx%d, want 1920x1080", gw, gh)
	}
	if got := w.Format(); got != 1 { // WINDOW_FORMAT_RGBA_8888, android_bridge.h:41
		t.Fatalf("format = %d, want preserved RGBA_8888 (1)", got)
	}
	for _, tc := range [][2]int{{0, 1080}, {1920, 0}, {-1, 100}, {0, 0}} {
		if err := w.Resize(tc[0], tc[1]); err == nil {
			t.Fatalf("Resize(%d,%d) accepted an invalid geometry", tc[0], tc[1])
		}
	}
	if gw, gh := w.Size(); gw != 1920 || gh != 1080 {
		t.Fatalf("size after rejected resizes = %dx%d, want 1920x1080", gw, gh)
	}
	var nilWindow *Window
	if err := nilWindow.Resize(10, 10); err == nil {
		t.Fatal("Resize on an unallocated window must fail")
	}
}
