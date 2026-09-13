// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: webkit2gtk-4.1 gtk+-3.0 x11
#include "webview_overlay.h"
#include <stdlib.h>
#include <string.h>
*/
import "C"

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// WebViewCookie is a scoped cookie for the in-window WebKit overlay.
// Callers must never log Name or Value.
type WebViewCookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	HostOnly bool
	Secure   bool
	HTTPOnly bool
	Expires  time.Time
}

// WebViewOpen is one OpenWindow request. URL is not logged.
type WebViewOpen struct {
	URL        string
	WindowType string
	Theme      string
	Title      string
	HideHeader bool
	Cookies    []WebViewCookie
}

var webViewOverlay struct {
	sync.Mutex
	started bool
	failed  bool
	visible bool
	assets  string
}

// SetWebViewAssetsDir supplies the active, validated APK asset directory.
// Cursor artwork is loaded from that installation, never bundled with Tipsy.
func SetWebViewAssetsDir(dir string) {
	webViewOverlay.Lock()
	webViewOverlay.assets = dir
	webViewOverlay.Unlock()
}

// ShowWebViewOverlay maps a WebKitGTK child over the active Roblox X11
// window. Missing GTK/WebKit is a single honest failure; the JNI protocol
// stays registered.
func ShowWebViewOverlay(p WebViewOpen) error {
	if stringsBlank(p.URL) {
		return fmt.Errorf("x11: webview open missing url")
	}
	activeWindow.Lock()
	w := activeWindow.w
	activeWindow.Unlock()
	if w == nil {
		return ErrClosed
	}
	w.mu.Lock()
	parent := w.xid
	width, height := w.width, w.height
	closed := w.closed
	w.mu.Unlock()
	if closed || parent == 0 {
		return ErrClosed
	}
	webViewOverlay.Lock()
	if webViewOverlay.failed {
		webViewOverlay.Unlock()
		return fmt.Errorf("x11: webkit overlay unavailable")
	}
	assets := webViewOverlay.assets
	webViewOverlay.Unlock()
	_, _ = SetPointerLock(false)
	// The GTK child owns its cursor. Changing the parent here leaks the host
	// cursor into Roblox after Back and overrides a valid LeftAlt policy.

	urlC := C.CString(p.URL)
	defer C.free(unsafe.Pointer(urlC))
	themeC := C.CString(WebViewWebsiteTheme(p.Theme))
	defer C.free(unsafe.Pointer(themeC))
	titleC := C.CString(p.Title)
	defer C.free(unsafe.Pointer(titleC))
	assetsC := C.CString(assets)
	defer C.free(unsafe.Pointer(assetsC))
	n := len(p.Cookies)
	var names, values, domains, paths **C.char
	var secure, httpOnly *C.int
	if n > 0 {
		nameA := make([]*C.char, n)
		valueA := make([]*C.char, n)
		domainA := make([]*C.char, n)
		pathA := make([]*C.char, n)
		secA := make([]C.int, n)
		httpA := make([]C.int, n)
		for i, c := range p.Cookies {
			nameA[i] = C.CString(c.Name)
			valueA[i] = C.CString(c.Value)
			domainA[i] = C.CString(SoupCookieDomain(c.Domain, c.HostOnly))
			path := c.Path
			if path == "" {
				path = "/"
			}
			pathA[i] = C.CString(path)
			if c.Secure {
				secA[i] = 1
			}
			if c.HTTPOnly {
				httpA[i] = 1
			}
		}
		defer func() {
			for i := range nameA {
				C.free(unsafe.Pointer(nameA[i]))
				if valueA[i] != nil {
					C.memset(unsafe.Pointer(valueA[i]), 0, C.size_t(len(p.Cookies[i].Value)+1))
					C.free(unsafe.Pointer(valueA[i]))
				}
				C.free(unsafe.Pointer(domainA[i]))
				C.free(unsafe.Pointer(pathA[i]))
			}
		}()
		names = (**C.char)(unsafe.Pointer(&nameA[0]))
		values = (**C.char)(unsafe.Pointer(&valueA[0]))
		domains = (**C.char)(unsafe.Pointer(&domainA[0]))
		paths = (**C.char)(unsafe.Pointer(&pathA[0]))
		secure = &secA[0]
		httpOnly = &httpA[0]
	}
	// Publish presentation state while the open is queued. A fast local page
	// may call back from GTK immediately after queuing; its dismissal must
	// observe this presentation rather than race the visible assignment.
	webViewOverlay.Lock()
	rc := C.tipsy_webview_overlay_open(C.ulong(parent), C.int(width), C.int(height),
		urlC, themeC, titleC, assetsC, names, values, domains, paths, secure, httpOnly, C.int(n))
	if rc != 0 {
		webViewOverlay.failed = true
		webViewOverlay.Unlock()
		logging.Logger(logging.CatX11).Error("webkit overlay unavailable")
		return fmt.Errorf("x11: webkit overlay unavailable")
	}
	webViewOverlay.started = true
	webViewOverlay.visible = true
	webViewOverlay.Unlock()
	return nil
}

func stringsBlank(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			return false
		}
	}
	return true
}

// HideWebViewOverlay unmaps the child so Roblox input is restored.
func HideWebViewOverlay() {
	hideWebViewOverlay()
}

// hideWebViewOverlay consumes a presentation before any close publication.
// Programmatic hides still suppress the user-close event.
func hideWebViewOverlay() bool {
	webViewOverlay.Lock()
	wasVisible := webViewOverlay.visible
	webViewOverlay.visible = false
	started := webViewOverlay.started
	failed := webViewOverlay.failed
	webViewOverlay.Unlock()
	if !started || failed {
		return wasVisible
	}
	C.tipsy_webview_overlay_hide()
	return wasVisible
}

// CloseWebViewOverlay destroys the child widgets. Safe to call from the
// parent window Close path.
func CloseWebViewOverlay() {
	webViewOverlay.Lock()
	webViewOverlay.visible = false
	started := webViewOverlay.started
	failed := webViewOverlay.failed
	webViewOverlay.Unlock()
	if !started || failed {
		return
	}
	C.tipsy_webview_overlay_close()
}

// WebViewOverlayVisible reports whether the child is mapped.
func WebViewOverlayVisible() bool {
	webViewOverlay.Lock()
	defer webViewOverlay.Unlock()
	return webViewOverlay.visible
}

func testWebViewCursor(kind int) int {
	return int(C.tipsy_webview_overlay_test_cursor(C.int(kind)))
}

func testWebViewMapped() bool {
	return C.tipsy_webview_overlay_visible() != 0
}

func testWebViewPolicy(uri string) int {
	uriC := C.CString(uri)
	defer C.free(unsafe.Pointer(uriC))
	return int(C.tipsy_webview_overlay_test_policy(uriC))
}

//export tipsy_go_webview_policy
func tipsy_go_webview_policy(uri *C.char) C.int {
	if uri == nil {
		return 0
	}
	if HandleWebViewPolicyURI(C.GoString(uri)) {
		return 1
	}
	return 0
}

//export tipsy_go_webview_closed
func tipsy_go_webview_closed() {
	dismissWebViewOverlay()
}
