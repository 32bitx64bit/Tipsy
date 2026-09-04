// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// NativeHandleProvider is implemented by X11 windows (internal/x11) so
// eglCreateWindowSurface can unwrap an ANativeWindow without importing x11.
type NativeHandleProvider interface {
	NativeHandle() uintptr
}

// Window is Tipsy's ANativeWindow. Graphics/X11 may set NativeHandle to an
// X11 Window (EGLNativeWindowType).
type Window struct {
	ptr unsafe.Pointer
}

// NewWindow allocates an ANativeWindow with the given size and optional X11 handle.
func NewWindow(width, height int, native NativeHandleProvider) *Window {
	var h uintptr
	if native != nil {
		h = native.NativeHandle()
	}
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	p := C.tipsy_ANativeWindow_new(C.int32_t(width), C.int32_t(height), C.uintptr_t(h))
	return &Window{ptr: unsafe.Pointer(p)}
}

// NativeHandle implements NativeHandleProvider (X11 Window / EGL native window).
func (w *Window) NativeHandle() uintptr {
	if w == nil || w.ptr == nil {
		return 0
	}
	return uintptr(C.tipsy_ANativeWindow_get_handle(w.ptr))
}

// SetNativeHandle stores an X11 Window (or other EGLNativeWindowType) on this ANativeWindow.
func (w *Window) SetNativeHandle(h uintptr) {
	if w == nil || w.ptr == nil {
		return
	}
	C.tipsy_ANativeWindow_set_handle(w.ptr, C.uintptr_t(h))
}

// Ptr returns the ANativeWindow* for NDK callers.
func (w *Window) Ptr() uintptr {
	if w == nil {
		return 0
	}
	return uintptr(w.ptr)
}

// Size returns the current ANativeWindow buffer dimensions in pixels.
func (w *Window) Size() (int, int) {
	if w == nil || w.ptr == nil {
		return 0, 0
	}
	return int(C.tipsy_ANativeWindow_getWidth(w.ptr)), int(C.tipsy_ANativeWindow_getHeight(w.ptr))
}

// Format returns the current ANativeWindow buffer pixel format
// (WINDOW_FORMAT_* in android_bridge.h; RGBA_8888 = 1 for new windows).
func (w *Window) Format() int {
	if w == nil || w.ptr == nil {
		return 0
	}
	return int(C.tipsy_ANativeWindow_getFormat(w.ptr))
}

// Resize updates the ANativeWindow buffer geometry to width×height through
// ANativeWindow_setBuffersGeometry, preserving the current pixel format.
// Non-positive dimensions are rejected without touching the window, matching
// the Android contract that buffer geometry must stay positive.
func (w *Window) Resize(width, height int) error {
	if w == nil || w.ptr == nil {
		return errors.New("android: ANativeWindow is not allocated")
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("android: invalid buffer geometry %dx%d", width, height)
	}
	format := C.tipsy_ANativeWindow_getFormat(w.ptr)
	if rc := C.tipsy_ANativeWindow_setBuffersGeometry(w.ptr, C.int32_t(width), C.int32_t(height), format); rc != 0 {
		return fmt.Errorf("android: ANativeWindow_setBuffersGeometry(%d,%d) failed: %d", width, height, int(rc))
	}
	return nil
}

// BindDefaultWindow makes ANativeWindow_fromSurface return this X11-backed window.
func BindDefaultWindow(w *Window) {
	if w == nil || w.ptr == nil {
		return
	}
	C.tipsy_set_default_window(w.ptr)
}

func CMalloc(n int) unsafe.Pointer {
	if n <= 0 {
		return C.malloc(1)
	}
	return C.malloc(C.size_t(n))
}

func newAsset(buf unsafe.Pointer, len int64, owned int, fd int) unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_AAsset_from_buffer(buf, C.int64_t(len), C.int(owned), C.int(fd)))
}
