// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import (
	"errors"
	"sync"
)

var (
	// ErrUnavailable is returned when this build has no native EGL (non-Linux or CGO disabled).
	ErrUnavailable = errors.New("graphics: native EGL unavailable")
	// ErrNoWindow is returned when BindEGL is given a nil or closed X11 window.
	ErrNoWindow = errors.New("graphics: missing X11 window")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("graphics: EGL surface closed")
)

// EGL is an ES2 context + window surface bound to a native X11 window.
type EGL struct {
	mu         sync.Mutex
	display    uintptr // EGLDisplay
	surface    uintptr // EGLSurface
	context    uintptr // EGLContext
	x11Display uintptr // Xlib Display* (observation-only probes)
	x11XID     uintptr // X11 Window
	swap       uintptr // C tipsy_swap* background thread, or 0
	// Handoff wake plumbing (linux+cgo; see bind_linux.go, notify_linux.go).
	// swapWake is the capacity-1 coalesced channel the C thread resolves via
	// swapHandle; swapStop is closed after the C thread is joined; swapDone is
	// closed by the watcher goroutine when it returns.
	swapWake   chan struct{}
	swapStop   chan struct{}
	swapDone   chan struct{}
	swapHandle uintptr // cgo.Handle for swapWake, or 0
}
