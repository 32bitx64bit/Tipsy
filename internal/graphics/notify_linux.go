// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package graphics

/*
#include <stdint.h> // for uintptr_t (declarations only; see //export rule)
*/
import "C"

import "runtime/cgo"

// GoEGLHandoff is invoked once by the C sentinel swap thread when it finishes.
// A verified guest swap normally retires it immediately. A host without the
// Android guest-swap bridge can instead finish through the strictly bounded
// readback fallback. The callback is an event, never a Go polling source.
//
// It must not block the C pthread, so the wake is a non-blocking token on the
// instance's capacity-1 coalesced channel. The uintptr is a cgo.Handle for
// that channel; StopSwapThread deletes the handle only after pthread_join, and
// the C thread can only call this before returning, so Value can never see a
// deleted handle.
//
//export GoEGLHandoff
func GoEGLHandoff(handle C.uintptr_t) {
	if handle == 0 {
		return
	}
	wake, ok := cgo.Handle(handle).Value().(chan struct{})
	if !ok || wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// GoEGLGuestSurfaceCreated registers the exact host EGLSurface paired with an
// X11 window and returns an opaque, monotonically changing generation. The
// Android compatibility shim must retain that generation and include it on
// successful swaps and destruction; returning zero means no active graphics
// sentinel owns this window.
//
//export GoEGLGuestSurfaceCreated
func GoEGLGuestSurfaceCreated(window, display, surface C.uintptr_t) C.uint64_t {
	return C.uint64_t(eglGuestSwaps.surfaceCreated(uintptr(window), uintptr(display), uintptr(surface)))
}

// GoEGLGuestSwap accepts only a successful Android EGL swap whose complete
// window/surface/generation identity belongs to the active sentinel. It wakes
// the C thread through its mutex/condition, rather than sampling window pixels
// or accessing Roblox memory.
//
//export GoEGLGuestSwap
func GoEGLGuestSwap(window, display, surface C.uintptr_t, generation C.uint64_t) C.int {
	if eglGuestSwaps.guestSwap(uintptr(window), uintptr(display), uintptr(surface), uint64(generation)) {
		return 1
	}
	return 0
}

// GoEGLGuestSurfaceDestroyed invalidates a surface mapping before the Android
// shim forwards EGL destruction. A stale later swap is then rejected even if
// the host reuses its numeric surface address.
//
//export GoEGLGuestSurfaceDestroyed
func GoEGLGuestSurfaceDestroyed(window, display, surface C.uintptr_t, generation C.uint64_t) {
	eglGuestSwaps.surfaceDestroyed(uintptr(window), uintptr(display), uintptr(surface), uint64(generation))
}
