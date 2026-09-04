// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package x11

// Open reports that native X11 is unavailable on this build.
func Open(title string, width, height int) (*Window, error) {
	_ = title
	if width < 1 || height < 1 {
		return nil, ErrInvalidSize
	}
	return nil, ErrUnavailable
}

// Pump reports that native X11 is unavailable on this build.
func (w *Window) Pump() error {
	if w == nil || w.closed {
		return ErrClosed
	}
	return ErrUnavailable
}

// StartBackgroundPump reports that native X11 is unavailable on this build.
func (w *Window) StartBackgroundPump() error {
	if w == nil || w.closed {
		return ErrClosed
	}
	return ErrUnavailable
}

// StopBackgroundPump is a no-op on builds without native X11.
func (w *Window) StopBackgroundPump() error {
	if w == nil {
		return nil
	}
	return nil
}

// Close is a no-op on builds without native X11.
func (w *Window) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.display = 0
	w.xid = 0
	w.cursor = 0
	return nil
}
