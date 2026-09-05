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
	mu          sync.Mutex
	display     uintptr   // EGLDisplay
	surface     uintptr   // EGLSurface
	context     uintptr   // EGLContext
	x11Display  uintptr   // Xlib Display* (observation-only probes)
	x11XID      uintptr   // X11 Window
	refreshHz   float64   // active XRandR CRTC refresh at bind time; 0 = unknown
	supportedHz []float32 // active XRandR output modes; immutable after bind
	swap        uintptr   // C tipsy_swap* background thread, or 0
}

// RefreshRateHz reports the XRandR refresh rate of the CRTC currently
// containing the client window. Zero means the X server did not expose a
// usable mode rate. It is an observation for presentation policy, not an FPS
// measurement.
func (e *EGL) RefreshRateHz() float64 {
	current, _ := e.refreshRateSnapshot()
	return current
}

// SupportedRefreshRatesHz reports the positive, deduplicated XRandR mode
// rates supported by the output currently containing the client window.
// The returned slice is a copy. An empty result means the X server did not
// expose a usable mode list.
func (e *EGL) SupportedRefreshRatesHz() []float32 {
	_, supported := e.refreshRateSnapshot()
	return supported
}

func (e *EGL) refreshRateSnapshot() (float64, []float32) {
	if e == nil {
		return 0, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// Window managers may place or move the mapped window after EGL bind.
	// Refresh from XRandR at each policy/JNI publication point so the result
	// follows the CRTC currently containing the window, then retain the last
	// usable snapshot as a fallback for a transient XRandR query failure.
	if e.x11Display != 0 && e.x11XID != 0 {
		if current, supported := platformDisplayRefreshRates(e.x11Display, e.x11XID); current > 0 {
			e.refreshHz = current
			e.supportedHz = append(e.supportedHz[:0], supported...)
		}
	}
	return e.refreshHz, append([]float32(nil), e.supportedHz...)
}
