// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo pkg-config: x11
#include "display_x11.h"
*/
import "C"

// X11DisplayPhysicalSizeMM returns the X server's reported physical
// screen size in millimeters for the display pointer (Xlib Display*),
// or 0,0 when the server does not expose one. dpy comes from the x11
// package Window the launcher already opened.
func X11DisplayPhysicalSizeMM(dpy uintptr) (int, int) {
	if dpy == 0 {
		return 0, 0
	}
	var w, h C.int
	C.tipsy_x11_physical_mm(C.uintptr_t(dpy), 0, &w, &h)
	return int(w), int(h)
}
