// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package x11

import "time"

// WebViewCookie is unused without Linux WebKitGTK.
type WebViewCookie struct {
	Name, Value, Domain, Path  string
	HostOnly, Secure, HTTPOnly bool
	Expires                    time.Time
}

// WebViewOpen is unused without Linux WebKitGTK.
type WebViewOpen struct {
	URL        string
	WindowType string
	Theme      string
	Title      string
	HideHeader bool
	Cookies    []WebViewCookie
}

func ShowWebViewOverlay(p WebViewOpen) error {
	_ = p
	return ErrUnavailable
}

func SetWebViewAssetsDir(dir string) {}

func HideWebViewOverlay() {}

func hideWebViewOverlay() bool { return false }

func CloseWebViewOverlay() {}

func WebViewOverlayVisible() bool { return false }
