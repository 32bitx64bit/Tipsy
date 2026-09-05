// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package x11

// FocusedTextOverlay is unavailable without Linux X11/cgo support.
type FocusedTextOverlay struct{}

func NewFocusedTextOverlay(w *Window) (*FocusedTextOverlay, error) {
	_ = w
	return nil, ErrUnavailable
}

func (o *FocusedTextOverlay) Update(s FocusedTextSnapshot) error {
	_ = o
	_ = s
	return ErrUnavailable
}

func (o *FocusedTextOverlay) Close() error { return nil }

func (o *FocusedTextOverlay) Diagnostics() FocusedTextOverlayDiagnostics {
	return FocusedTextOverlayDiagnostics{}
}
