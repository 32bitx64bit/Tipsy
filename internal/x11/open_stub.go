// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package x11

// Open reports that native X11 is unavailable on this build.
func Open(title string, width, height int) (*Window, error) {
	return OpenOnDisplay(title, width, height, DisplayPointer)
}

// OpenOnDisplay reports that native X11 is unavailable on this build.
func OpenOnDisplay(title string, width, height int, display string) (*Window, error) {
	_ = title
	_ = display
	if width < 1 || height < 1 {
		return nil, ErrInvalidSize
	}
	return nil, ErrUnavailable
}

// ListOutputs reports that native X11 is unavailable on this build.
func ListOutputs() ([]Output, error) {
	return nil, ErrUnavailable
}

func setFullscreenLocked(w *Window, enabled bool) error {
	_ = w
	_ = enabled
	return ErrUnavailable
}

func dismissLocked(w *Window) error {
	w.dismissed = true
	return nil
}

func setPointerLockLocked(w *Window, locked, center bool) (bool, error) {
	_ = w
	_ = locked
	_ = center
	return false, ErrUnavailable
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

// InputReady is never selectable on builds without native X11.
func (w *Window) InputReady() <-chan struct{} {
	return nil
}

// RefreshReady is never selectable on builds without native X11.
func (w *Window) RefreshReady() <-chan struct{} {
	return nil
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
	clearActiveWindow(w)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.display = 0
	w.xid = 0
	w.cursor = 0
	w.pointerCaptured = false
	w.dismissed = true
	return nil
}

// RefreshVersion is unavailable without native X11.
func (w *Window) RefreshVersion() uint64 { return 0 }

// WakeEventPump is a no-op without native X11.
func WakeEventPump() {}

func testLastPumpRawSamples() int { return 0 }

func testLastPumpWarps() int { return 0 }

func testCoalesceRawPump(n int) int {
	_ = n
	return -1
}
