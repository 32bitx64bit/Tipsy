// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package graphics

/*
#cgo pkg-config: x11 xrandr egl glesv2
#cgo LDFLAGS: -lX11 -lEGL -lGLESv2 -pthread
#include "egl.h"
*/
import "C"

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"time"

	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

const (
	eglSuccess = 0x3000
	eglVendor  = 0x3053
	eglVersion = 0x3054
)

func platformDisplayRefreshRates(xdisplay, xid uintptr) (float64, []float32) {
	if xdisplay == 0 || xid == 0 {
		return 0, nil
	}
	var nativeCurrent C.double
	var nativeRates [64]C.double
	nativeRateCount := int(C.tipsy_x11_refresh_rates(
		C.uintptr_t(xdisplay), C.ulong(xid), &nativeCurrent, &nativeRates[0], C.int(len(nativeRates))))
	// Replies can move input/RandR events from the connection socket into
	// Xlib's queue. Wake the sole event reader even if the socket is now empty.
	x11.WakeEventPump()
	current := float64(nativeCurrent)
	uniqueRates := make(map[int64]float64, nativeRateCount+1)
	for i := 0; i < nativeRateCount; i++ {
		hz := float64(nativeRates[i])
		if hz > 0 {
			uniqueRates[int64(math.Round(hz*1000))] = hz
		}
	}
	if current > 0 {
		uniqueRates[int64(math.Round(current*1000))] = current
	}
	rates := make([]float64, 0, len(uniqueRates))
	for _, hz := range uniqueRates {
		rates = append(rates, hz)
	}
	sort.Float64s(rates)
	supported := make([]float32, 0, len(rates))
	for _, hz := range rates {
		supported = append(supported, float32(hz))
	}
	return current, supported
}

// BindEGL creates an OpenGL ES 2 context and window surface on x.
func BindEGL(x *x11.Window) (*EGL, error) {
	if x == nil || x.Display() == 0 || x.XID() == 0 {
		return nil, ErrNoWindow
	}
	runtime.LockOSThread()

	var dpy, surf, ctx C.uintptr_t
	rc := C.tipsy_egl_bind(C.uintptr_t(x.Display()), C.ulong(x.XID()), &dpy, &surf, &ctx)
	if rc != eglSuccess || dpy == 0 || surf == 0 || ctx == 0 {
		return nil, fmt.Errorf("graphics: BindEGL failed: %s", eglErrorName(int(rc)))
	}

	refreshHz, supportedHz := platformDisplayRefreshRates(x.Display(), x.XID())
	e := &EGL{
		display:    uintptr(dpy),
		surface:    uintptr(surf),
		context:    uintptr(ctx),
		x11Display: x.Display(),
		x11XID:     x.XID(),
	}
	path := C.GoString(C.tipsy_egl_bind_path())
	vendor := C.GoString(C.tipsy_egl_query(dpy, eglVendor))
	version := C.GoString(C.tipsy_egl_query(dpy, eglVersion))
	logging.Logger(logging.CatGraphics).Info("bound EGL on X11",
		"path", path, "vendor", vendor, "version", version,
		"refreshHz", refreshHz, "supportedRefreshRates", supportedHz)
	return e, nil
}

// Swap presents the current back buffer on the calling thread (EGL owner).
// This is for Tipsy's own pre-client frames only: once the Roblox RenderJob
// presents, it is the sole presenter on the XID and must not be fought.
func (e *EGL) Swap() error {
	if e == nil {
		return ErrClosed
	}
	runtime.LockOSThread()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 || e.surface == 0 || e.context == 0 {
		return ErrClosed
	}
	if rc := C.tipsy_egl_make_current(C.uintptr_t(e.display), C.uintptr_t(e.surface), C.uintptr_t(e.context)); rc != eglSuccess {
		return fmt.Errorf("graphics: MakeCurrent: %s", eglErrorName(int(rc)))
	}
	rc := C.tipsy_egl_swap(C.uintptr_t(e.display), C.uintptr_t(e.surface))
	if rc != eglSuccess {
		return fmt.Errorf("graphics: Swap: %s", eglErrorName(int(rc)))
	}
	return nil
}

// ReleaseCurrent unbinds the context so another thread can MakeCurrent.
func (e *EGL) ReleaseCurrent() error {
	if e == nil {
		return ErrClosed
	}
	runtime.LockOSThread()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 {
		return ErrClosed
	}
	if rc := C.tipsy_egl_release_current(C.uintptr_t(e.display)); rc != eglSuccess {
		return fmt.Errorf("graphics: ReleaseCurrent: %s", eglErrorName(int(rc)))
	}
	return nil
}

// StartSwapThread presents one sentinel frame on a C pthread, then yields
// the window: it never presents again and retires permanently on the first
// client-presented frame, so the Roblox RenderJob is the sole presenter.
func (e *EGL) StartSwapThread() error {
	if e == nil {
		return ErrClosed
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 || e.surface == 0 || e.context == 0 {
		return ErrClosed
	}
	if e.swap != 0 {
		return nil
	}
	p := C.tipsy_egl_swap_thread_start(C.uintptr_t(e.x11Display), C.ulong(e.x11XID),
		C.uintptr_t(e.display), C.uintptr_t(e.surface), C.uintptr_t(e.context))
	if p == 0 {
		return fmt.Errorf("graphics: swap thread")
	}
	e.swap = uintptr(p)
	go e.watchSwapHandoff()
	return nil
}

// watchSwapHandoff logs the single presenter handoff when the C thread
// detects a client-presented frame. It exits once the thread is stopped or
// the handoff is observed.
func (e *EGL) watchSwapHandoff() {
	for {
		time.Sleep(250 * time.Millisecond)
		e.mu.Lock()
		if e.swap == 0 {
			e.mu.Unlock()
			return
		}
		retired, probes, failed := e.swapHandoffStatsLocked()
		e.mu.Unlock()
		if retired {
			logging.Logger(logging.CatGraphics).Info("client presenter detected on window; Tipsy swap thread retired",
				"probes", probes, "probeFailures", failed)
			return
		}
	}
}

// SwapHandedOff reports whether the swap thread detected a client-presented
// frame on the window and permanently stopped presenting. After handoff the
// Roblox RenderJob is the only presenter on the XID.
func (e *EGL) SwapHandedOff() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	retired, _, _ := e.swapHandoffStatsLocked()
	return retired
}

func (e *EGL) swapHandoffStatsLocked() (retired bool, probes uint64, failed uint64) {
	if e.swap == 0 {
		return false, 0, 0
	}
	var cRetired C.int
	var cProbes, cFailed C.ulong
	C.tipsy_egl_swap_thread_state(C.uintptr_t(e.swap), &cRetired, &cProbes, &cFailed)
	return cRetired != 0, uint64(cProbes), uint64(cFailed)
}

// StopSwapThread joins the C present thread and releases its current context.
func (e *EGL) StopSwapThread() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopSwapThreadLocked()
}

func (e *EGL) stopSwapThreadLocked() error {
	if e.swap == 0 {
		return nil
	}
	fail := C.tipsy_egl_swap_thread_stop(C.uintptr_t(e.swap))
	e.swap = 0
	if fail != 0 && fail != eglSuccess {
		return fmt.Errorf("graphics: swap thread: %s", eglErrorName(int(fail)))
	}
	return nil
}

// Close releases the EGL surface, context, and display.
func (e *EGL) Close() error {
	if e == nil {
		return nil
	}
	runtime.LockOSThread()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 {
		return nil
	}
	_ = e.stopSwapThreadLocked()
	C.tipsy_egl_close(C.uintptr_t(e.display), C.uintptr_t(e.surface), C.uintptr_t(e.context))
	logging.Logger(logging.CatGraphics).Info("closed EGL")
	e.display = 0
	e.surface = 0
	e.context = 0
	return nil
}

func eglErrorName(code int) string {
	switch code {
	case 0x3000:
		return "EGL_SUCCESS"
	case 0x3001:
		return "EGL_NOT_INITIALIZED"
	case 0x3002:
		return "EGL_BAD_ACCESS"
	case 0x3003:
		return "EGL_BAD_ALLOC"
	case 0x3004:
		return "EGL_BAD_ATTRIBUTE"
	case 0x3005:
		return "EGL_BAD_CONFIG"
	case 0x3006:
		return "EGL_BAD_CONTEXT"
	case 0x3007:
		return "EGL_BAD_CURRENT_SURFACE"
	case 0x3008:
		return "EGL_BAD_DISPLAY"
	case 0x3009:
		return "EGL_BAD_MATCH"
	case 0x300A:
		return "EGL_BAD_NATIVE_PIXMAP"
	case 0x300B:
		return "EGL_BAD_NATIVE_WINDOW"
	case 0x300C:
		return "EGL_BAD_PARAMETER"
	case 0x300D:
		return "EGL_BAD_SURFACE"
	case 0x300E:
		return "EGL_CONTEXT_LOST"
	default:
		return fmt.Sprintf("EGL error 0x%x", code)
	}
}
