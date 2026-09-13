// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// This owns a separate host window and uses only synthetic local content.
// It validates real GTK cursor transitions without mouse injection or Roblox.
func TestWebViewHostCursorPreservesParentPolicy(t *testing.T) {
	if os.Getenv("TIPSY_WEBVIEW_HOST_TEST") != "1" {
		t.Skip("set TIPSY_WEBVIEW_HOST_TEST=1 on an X11 display")
	}
	w, err := Open("Tipsy WebView cursor test", 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		CloseWebViewOverlay()
		testWebViewCursor(-1)
		SetWebViewAssetsDir("")
		w.Close()
	})
	assets := t.TempDir()
	dir := filepath.Join(assets, "content", "textures", "Cursors", "KeyboardMouse")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"ArrowFarCursor.png", "ArrowCursor.png", "IBeamCursor.png"} {
		im := image.NewNRGBA(image.Rect(0, 0, 64, 64))
		im.Set(32+i, 32+i, color.White)
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, im)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, withAssets := range []bool{true, false} {
		CloseWebViewOverlay()
		if withAssets {
			SetWebViewAssetsDir(assets)
		} else {
			SetWebViewAssetsDir("")
		}
		for _, visible := range []bool{false, true, false} {
			SetCursorVisible(visible)
			w.mu.Lock()
			before := w.cursor
			w.mu.Unlock()
			if err := ShowWebViewOverlay(WebViewOpen{
				URL:   "data:text/html,<title>Cursor test</title><p>Local cursor test</p>",
				Theme: "Dark", Title: "Cursor test",
			}); err != nil {
				t.Fatal(err)
			}
			for kind := 0; kind < 3; kind++ {
				want := 1 // GTK fallback when no artwork is installed.
				if withAssets {
					want = 2
				}
				if testWebViewCursor(kind) != want {
					t.Fatalf("cursor kind %d failed (assets=%t)", kind, withAssets)
				}
			}
			w.mu.Lock()
			afterOpen := w.cursor
			w.mu.Unlock()
			HideWebViewOverlay()
			if testWebViewCursor(-1) != 1 {
				t.Fatal("child did not unmap")
			}
			w.mu.Lock()
			afterClose := w.cursor
			w.mu.Unlock()
			if afterOpen != before || afterClose != before {
				t.Fatalf("WebView changed parent cursor policy (visible=%t)", visible)
			}
		}
	}
}
