// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo && !official_release

package x11

/*
#cgo CFLAGS: -DTIPSY_X11_ALLOW_SYNTHETIC_TEST_EVENTS=1 -DTIPSY_WEBVIEW_ALLOW_TEST_SEAMS=1
#include "x11.h"
#include "webview_overlay.h"
*/
import "C"

// This file compiles the development-only native test seams. The macros are
// part of the package's merged cgo CFLAGS, so x11.c and webview_overlay.c see
// them too. official_release builds exclude this file entirely: the X11
// synthetic-event opt-in and the WebView join-gate bypass then do not exist.

// setSyntheticTestEvents enables or disables the development-only opt-in
// that lets tipsy_x11_pump accept synthetic XSendEvent input, and returns the
// resulting state. Production code never calls it; only tests do, and the
// pump drops forged input again when it is off.
func setSyntheticTestEvents(enabled bool) bool {
	v := C.int(0)
	if enabled {
		v = 1
	}
	return C.tipsy_x11_set_allow_synthetic_test_events(v) != 0
}

// syntheticTestEventsEnabled reports whether the synthetic-event opt-in is
// currently active.
func syntheticTestEventsEnabled() bool {
	return C.tipsy_x11_allow_synthetic_test_events() != 0
}

// testWebViewSetJoinGateBypass arms the development-only bypass of the
// WebView join origin/gesture gate so host tests can drive join handoffs
// from local data: pages. It reports whether the seam exists; official
// builds compile it out and always return false.
func testWebViewSetJoinGateBypass(bypass bool) bool {
	v := C.int(0)
	if bypass {
		v = 1
	}
	C.tipsy_webview_overlay_test_set_join_gate_bypass(v)
	return true
}
