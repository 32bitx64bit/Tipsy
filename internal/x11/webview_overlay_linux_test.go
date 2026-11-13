// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

// Exercise the registered close publisher rather than an injected hide
// function, without starting GTK or Roblox.
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

// TestWebViewJoinGateRequiresOfficialOriginAndRecentInput exercises the real
// C join policy against explicit facts: a verified https Roblox origin plus
// real user input within the gesture window. It needs no display or page.
func TestWebViewJoinGateRequiresOfficialOriginAndRecentInput(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		age  float64 // seconds since last real input; negative means none
		want int
	}{
		{"official fresh input", "https://www.roblox.com/games/start?placeId=1818", 0.2, 1},
		{"apex origin", "https://roblox.com/", 1, 1},
		{"subdomain", "https://experiences.roblox.com/", 4, 1},
		{"inside window", "https://www.roblox.com/", 4.9, 1},
		{"stale input", "https://www.roblox.com/", 6, 0},
		{"no input", "https://www.roblox.com/", -1, 0},
		{"plain http", "http://www.roblox.com/", 0.2, 0},
		{"hostile host", "https://evil.com/", 0.2, 0},
		{"suffix trick", "https://roblox.com.evil.com/", 0.2, 0},
		{"lookalike host", "https://evilroblox.com/", 0.2, 0},
		{"deep suffix trick", "https://www.roblox.com.evil.com/", 0.2, 0},
		{"data page", "data:text/html,<p>local</p>", 0.2, 0},
		{"userinfo", "https://user:pass@www.roblox.com/", 0.2, 0},
		{"empty uri", "", 0.2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testWebViewJoinGate(tc.uri, tc.age); got != tc.want {
				t.Fatalf("gate(%q, age=%v) = %d, want %d", tc.uri, tc.age, got, tc.want)
			}
		})
	}
}

// This owns a separate host window and uses only synthetic local content;
// it validates real GTK cursor transitions.
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

// This owns two short-lived synthetic host windows and pins the post-join
// lifecycle: the child is unmapped before StartGame, and destroying the first
// host cannot leave an old WebKit child above a replacement host.
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
	// With the gate armed, a local data: page has no verified Roblox origin
	// and must not hand off a join, even for the app's own roblox: scheme.
	if testWebViewPolicy("roblox://experiences/start?placeId=1818") != 0 {
		first.Close()
		t.Fatal("data: page join was accepted without a verified origin")
	}
	// The remaining lifecycle assertions drive joins from local data: pages,
	// which requires the development-only gate bypass. Official builds
	// compile it out and skip this host test.
	if !testWebViewSetJoinGateBypass(true) {
		first.Close()
		t.Skip("webview join test seam is compiled out (official_release)")
	}
	t.Cleanup(func() { testWebViewSetJoinGateBypass(false) })
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

func waitTestWebViewMapped(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if testWebViewMapped() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("WebView child did not map")
}

func waitTestWebViewDocument(t *testing.T, prefix string) {
	t.Helper()
	waitTestWebViewURI(t, func(uri string) bool {
		return strings.HasPrefix(uri, prefix)
	}, prefix)
}

func waitTestWebViewURI(t *testing.T, match func(string) bool, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if uri, loading := testWebViewDocument(); !loading && match(uri) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	uri, loading := testWebViewDocument()
	t.Fatalf("document %q loading=%t never became %s", uri, loading, want)
}

// TestWebViewPostMessageJoinRequiresOfficialOriginAndGesture drives the real
// script-message-received::tipsyJoin path with window.webkit.messageHandlers
// .tipsyJoin.postMessage on a live WebKitGTK view. It pins the
// WebKitJavascriptResult decoding: without it no message reaches the gate.
func TestWebViewPostMessageJoinRequiresOfficialOriginAndGesture(t *testing.T) {
	if os.Getenv("TIPSY_WEBVIEW_HOST_TEST") != "1" {
		t.Skip("set TIPSY_WEBVIEW_HOST_TEST=1 on an X11 display")
	}
	CloseWebViewOverlay()
	SetWebViewStartGame(nil)
	SetWebViewUserClosed(nil)
	started := make(chan rbxuri.Request, 4)
	SetWebViewStartGame(func(req rbxuri.Request) { started <- req })
	w, err := Open("Tipsy WebView postMessage test", 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		SetWebViewStartGame(nil)
		SetWebViewUserClosed(nil)
		CloseWebViewOverlay()
		w.Close()
	})

	show := func(html string) {
		t.Helper()
		url := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
		if err := ShowWebViewOverlay(WebViewOpen{
			URL: url, Theme: "Dark", Title: "PostMessage test",
		}); err != nil {
			t.Fatal(err)
		}
		waitTestWebViewMapped(t)
	}
	expectRejected := func(step string) {
		t.Helper()
		select {
		case req := <-started:
			t.Fatalf("%s: join %+v reached StartGame", step, req)
		case <-time.After(750 * time.Millisecond):
		}
	}
	expectJoin := func(step string) {
		t.Helper()
		select {
		case req := <-started:
			if req.PlaceID != 1818 {
				t.Fatalf("%s: StartGame got place %d, want 1818", step, req.PlaceID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: postMessage never reached StartGame", step)
		}
	}

	// 1. A genuine https://*.roblox.com document whose load handler posts a
	// join has no user input yet. The origin check passes; the gesture check
	// must reject the message.
	show("<p>origin probe</p>")
	if !testWebViewLoadRobloxPage() {
		t.Fatal("could not load the Roblox-origin document")
	}
	waitTestWebViewDocument(t, "https://www.roblox.com/")
	expectRejected("official origin without user input")
	if !WebViewOverlayVisible() {
		t.Fatal("rejected join hid the overlay")
	}

	// 2. A real key press on the view licenses the join window. Reloading the
	// first-party page makes its load handler post the same join again, now
	// with fresh input.
	if !testWebViewSendKey() {
		t.Fatal("could not deliver real key input to the view")
	}
	if !testWebViewLoadRobloxPage() {
		t.Fatal("could not reload the Roblox-origin document")
	}
	expectJoin("official origin with recent user input")
	if WebViewOverlayVisible() {
		t.Fatal("join handoff did not hide the overlay")
	}

	// 3. A non-Roblox document must fail the origin check even with fresh
	// real input. Its own page script posts the same join shortly after load
	// and then marks the document, so the rejection is not a missing post.
	show(`<script>setTimeout(function(){window.webkit.messageHandlers.tipsyJoin.postMessage("roblox://experiences/start?placeId=1818");location.hash="posted"},500)</script>`)
	if !testWebViewSendKey() {
		t.Fatal("could not deliver real key input to the view")
	}
	waitTestWebViewURI(t, func(uri string) bool {
		return strings.HasPrefix(uri, "data:") && strings.Contains(uri, "posted")
	}, "the posted data: document")
	expectRejected("non-Roblox origin with recent user input")
	if !WebViewOverlayVisible() {
		t.Fatal("rejected join hid the overlay")
	}
}
