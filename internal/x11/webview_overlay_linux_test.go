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
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

// Exercise the registered close publisher rather than just an injected hide
// function, without starting GTK or using a Roblox/account-bearing page.
func TestWebViewURIJoinDismissesBeforeStartGame(t *testing.T) {
	for _, raw := range []string{
		"roblox://experiences/start?placeId=1818",
		"robloxmobile://experiences/start?placeId=1818",
		"https://www.roblox.com/games/start?placeId=1818",
		`lp:openUrl{"url":"roblox://experiences/start?placeId=1818"}`,
	} {
		t.Run(ClassifyWebViewURL(raw).Scheme, func(t *testing.T) {
			setTestWebViewPresentation(t)
			var sequence []string
			SetWebViewUserClosed(func() {
				if WebViewOverlayVisible() {
					t.Error("close publisher ran before the child was hidden")
				}
				sequence = append(sequence, "close")
				// The official close publisher may synchronously re-enter the
				// host. Its dismissal was already consumed before publication.
				HandleWebViewPolicyURI("bs:command:close")
			})
			SetWebViewStartGame(func(rbxuri.Request) {
				sequence = append(sequence, "start")
			})
			t.Cleanup(func() {
				SetWebViewUserClosed(nil)
				SetWebViewStartGame(nil)
			})
			if !HandleWebViewPolicyURI(raw) {
				t.Fatal("ordinary join URI was not handled")
			}
			HandleHybridExecuteRoblox(`{"moduleID":"Overlay","functionName":"close"}`)
			if got := strings.Join(sequence, ","); got != "close,start" {
				t.Fatalf("registered callbacks = %q, want close,start exactly once", got)
			}
		})
	}
}

func TestWebViewHybridJoinKeepsDistinctCloseContract(t *testing.T) {
	setTestWebViewPresentation(t)
	var sequence []string
	SetWebViewJavascriptSignal(func(string) { sequence = append(sequence, "signal") })
	SetWebViewUserClosed(func() { sequence = append(sequence, "close") })
	SetWebViewStartGame(func(rbxuri.Request) {
		if WebViewOverlayVisible() {
			t.Error("Hybrid StartGame ran before the child was hidden")
		}
		sequence = append(sequence, "start")
	})
	t.Cleanup(func() {
		SetWebViewJavascriptSignal(nil)
		SetWebViewUserClosed(nil)
		SetWebViewStartGame(nil)
	})
	HandleHybridExecuteRoblox(`{"moduleID":"Game","functionName":"launchGame","params":{"request":{"requestType":"RequestGame","placeId":1818}}}`)
	if got := strings.Join(sequence, ","); got != "signal,start" {
		t.Fatalf("Hybrid callbacks = %q, want signal,start without browser close", got)
	}
}

func TestWebViewInvalidJoinKeepsPresentation(t *testing.T) {
	setTestWebViewPresentation(t)
	closed, started := false, false
	SetWebViewUserClosed(func() { closed = true })
	SetWebViewStartGame(func(rbxuri.Request) { started = true })
	t.Cleanup(func() {
		SetWebViewUserClosed(nil)
		SetWebViewStartGame(nil)
	})
	HandleWebViewPolicyURI("roblox://experiences/start?placeId=invalid")
	HandleHybridExecuteRoblox(`{"moduleID":"Game","functionName":"launchGame","params":{"request":{"requestType":"Unsupported","placeId":1818}}}`)
	if !WebViewOverlayVisible() || closed || started {
		t.Fatal("invalid join must preserve the browser without close/start callbacks")
	}
}

func setTestWebViewPresentation(t *testing.T) {
	t.Helper()
	webViewOverlay.Lock()
	visible, started, failed := webViewOverlay.visible, webViewOverlay.started, webViewOverlay.failed
	webViewOverlay.visible, webViewOverlay.started, webViewOverlay.failed = true, false, false
	webViewOverlay.Unlock()
	t.Cleanup(func() {
		webViewOverlay.Lock()
		webViewOverlay.visible, webViewOverlay.started, webViewOverlay.failed = visible, started, failed
		webViewOverlay.Unlock()
	})
}

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

// This owns two short-lived synthetic host windows. It pins the lifecycle
// that matters after a browser-originated join: the child is unmapped before
// StartGame, and destroying that first host cannot leave an old WebKit child
// above a replacement host's Home surface.
func TestWebViewJoinHandoffAndHostRecreation(t *testing.T) {
	if os.Getenv("TIPSY_WEBVIEW_HOST_TEST") != "1" {
		t.Skip("set TIPSY_WEBVIEW_HOST_TEST=1 on an X11 display")
	}
	CloseWebViewOverlay()
	SetWebViewStartGame(nil)
	SetWebViewUserClosed(nil)
	t.Cleanup(func() {
		SetWebViewStartGame(nil)
		SetWebViewUserClosed(nil)
		CloseWebViewOverlay()
	})
	first, err := Open("Tipsy WebView handoff test", 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	if err := ShowWebViewOverlay(WebViewOpen{
		URL: "data:text/html,<title>Handoff test</title><p>Local only</p>", Theme: "Dark", Title: "Handoff test",
	}); err != nil {
		first.Close()
		t.Fatal(err)
	}
	if testWebViewCursor(0) == 0 {
		first.Close()
		t.Fatal("first synthetic WebView child did not map")
	}
	started := false
	closed := 0
	SetWebViewUserClosed(func() {
		if testWebViewMapped() {
			t.Error("handleWindowClose ran while the child was still mapped")
		}
		closed++
		HandleWebViewPolicyURI("bs:command:close")
	})
	SetWebViewStartGame(func(rbxuri.Request) {
		if testWebViewMapped() {
			t.Error("StartGame ran while the WebView child was still mapped")
		}
		if closed != 1 {
			t.Errorf("StartGame saw %d close notifications, want 1", closed)
		}
		started = true
	})
	if testWebViewPolicy("roblox://experiences/start?placeId=1818") != 1 || !started {
		first.Close()
		t.Fatal("synthetic join did not reach StartGame")
	}
	if testWebViewPolicy("roblox://experiences/start?placeId=1818") != 0 ||
		testWebViewPolicy("bs:command:close") != 0 || closed != 1 {
		first.Close()
		t.Fatal("hidden child accepted a stale join/close callback")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if testWebViewCursor(-1) != 1 {
		t.Fatal("destroyed host retained a mapped WebView child")
	}

	second, err := Open("Tipsy WebView replacement host", 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := ShowWebViewOverlay(WebViewOpen{
		URL: "data:text/html,<title>Replacement test</title><p>Local only</p>", Theme: "Dark", Title: "Replacement test",
	}); err != nil {
		t.Fatal(err)
	}
	if testWebViewCursor(0) == 0 {
		t.Fatal("replacement host did not receive one mapped WebView child")
	}
	started = false
	if testWebViewPolicy(`{"moduleID":"Game","functionName":"launchGame","params":{"request":{"requestType":"RequestGame","placeId":1818}}}`) != 1 || !started || closed != 1 {
		t.Fatal("Hybrid handoff changed the distinct hide-only contract")
	}
	if testWebViewCursor(-1) != 1 {
		t.Fatal("replacement WebView child did not unmap")
	}
	if err := ShowWebViewOverlay(WebViewOpen{
		URL: "data:text/html,<title>Reopen test</title><p>Local only</p>", Theme: "Dark", Title: "Reopen test",
	}); err != nil {
		t.Fatal(err)
	}
	if testWebViewPolicy("bs:command:close") != 1 || closed != 2 {
		t.Fatal("new presentation did not receive exactly one close notification")
	}
}
