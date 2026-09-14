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
	x11Display uintptr // Xlib Display* (bounded fallback probes only)
	x11XID     uintptr // X11 Window
	swap       uintptr // C tipsy_swap* background thread, or 0
	// swapGuest identifies this sentinel's one accepted Android EGL surface.
	// It is registered before the C thread can expose a sentinel and removed
	// before that thread is joined and freed. The Android ABI bridge carries
	// its generation back on every guest-swap signal, so a recycled XID or EGL
	// handle cannot retire a later surface.
	swapGuest        *guestSwapRegistration
	swapGuestSurface guestSwapIdentity
	// Handoff wake plumbing (linux+cgo; see bind_linux.go, notify_linux.go).
	// swapWake is the capacity-1 coalesced channel the C thread resolves via
	// swapHandle; swapStop is closed after the C thread is joined; swapDone is
	// closed by the watcher goroutine when it returns.
	swapWake   chan struct{}
	swapStop   chan struct{}
	swapDone   chan struct{}
	swapHandle uintptr // cgo.Handle for swapWake, or 0
}

// swapHandoffSource says why the sentinel thread stopped. guest-swap is the
// normal path. bounded-readback-fallback is retained only for a host where the
// Android EGL compatibility boundary cannot provide the explicit signal.
type swapHandoffSource uint8

const (
	swapHandoffNone swapHandoffSource = iota
	swapHandoffGuestSwap
	swapHandoffReadbackFallback
	swapHandoffFallbackExhausted
)

func (s swapHandoffSource) String() string {
	switch s {
	case swapHandoffGuestSwap:
		return "guest-swap"
	case swapHandoffReadbackFallback:
		return "bounded-readback-fallback"
	case swapHandoffFallbackExhausted:
		return "fallback-exhausted"
	default:
		return "none"
	}
}

// swapHandoffStats is intentionally content-free. It provides the exact
// event source and bounded fallback interval/count for diagnostics and tests;
// it does not attempt to infer displayed FPS or frame time.
type swapHandoffStats struct {
	Retired  bool
	Finished bool
	Source   swapHandoffSource
	Probes   uint64
	Failed   uint64
}
