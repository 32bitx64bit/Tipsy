// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo && official_release

package x11

// Official releases drop all synthetic XSendEvent input and enforce the
// WebView join origin/gesture gate unconditionally. The development test
// seams are compiled out; these no-op stubs only keep test code buildable.

// setSyntheticTestEvents never enables synthetic input in official builds.
func setSyntheticTestEvents(bool) bool { return false }

// syntheticTestEventsEnabled is always false in official builds.
func syntheticTestEventsEnabled() bool { return false }

// testWebViewSetJoinGateBypass reports that the bypass seam does not exist.
func testWebViewSetJoinGateBypass(bool) bool { return false }
