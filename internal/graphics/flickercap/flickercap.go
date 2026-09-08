// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package flickercap is a diagnostics-only helper that captures frames of an
// already-mapped window for flicker analysis. It reads pixels of the named
// target window only (never the desktop) and is not linked into the launch
// path.
package flickercap

/*
#cgo pkg-config: x11
#include "flickercap.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Target is a handle to one mapped window on the default display.
type Target struct {
	dpy    C.uintptr_t
	win    C.ulong
	Width  int
	Height int
}

// Find locates the mapped Roblox window (WM_CLASS "roblox" or WM_NAME
// "Roblox") on DISPLAY.
func Find() (*Target, error) {
	var dpy C.uintptr_t
	var win C.ulong
	var w, h C.int
	if rc := C.tipsy_fcap_find(&dpy, &win, &w, &h); rc != 0 {
		return nil, fmt.Errorf("flickercap: Roblox window not found (rc=%d)", int(rc))
	}
	return &Target{dpy: dpy, win: win, Width: int(w), Height: int(h)}, nil
}

// Attach connects to an already-mapped window by XID through a fresh X
// connection.
func Attach(xid uintptr) (*Target, error) {
	var dpy C.uintptr_t
	var w, h C.int
	if rc := C.tipsy_fcap_attach(&dpy, C.ulong(xid), &w, &h); rc != 0 {
		return nil, fmt.Errorf("flickercap: cannot attach to xid %d (rc=%d)", uint64(xid), int(rc))
	}
	return &Target{dpy: dpy, win: C.ulong(xid), Width: int(w), Height: int(h)}, nil
}

// PaintWhite fills the window white through the capture connection. Used by
// tests to simulate a foreign presenter's frames.
func (t *Target) PaintWhite() error {
	if t == nil || t.dpy == 0 {
		return fmt.Errorf("flickercap: closed target")
	}
	if rc := C.tipsy_fcap_paint_white(t.dpy, t.win, C.int(t.Width), C.int(t.Height)); rc != 0 {
		return fmt.Errorf("flickercap: paint failed (rc=%d)", int(rc))
	}
	return nil
}

// Close releases the capture display connection.
func (t *Target) Close() {
	if t == nil || t.dpy == 0 {
		return
	}
	C.tipsy_fcap_close(t.dpy)
	t.dpy = 0
}

// Grid samples a gw x gh RGB grid of the window's current content into dst
// (len dst >= gw*gh*4) and returns it.
func (t *Target) Grid(gw, gh int, dst []byte) ([]byte, error) {
	if t == nil || t.dpy == 0 {
		return nil, fmt.Errorf("flickercap: closed target")
	}
	if len(dst) < gw*gh*4 {
		return nil, fmt.Errorf("flickercap: dst too small")
	}
	rc := C.tipsy_fcap_grid(t.dpy, t.win, C.int(t.Width), C.int(t.Height),
		C.int(gw), C.int(gh), (*C.uchar)(unsafe.Pointer(&dst[0])))
	if rc != 0 {
		return nil, fmt.Errorf("flickercap: XGetImage failed (rc=%d)", int(rc))
	}
	return dst, nil
}

// Full grabs the whole window as tightly packed RGBX bytes.
func (t *Target) Full() ([]byte, error) {
	if t == nil || t.dpy == 0 {
		return nil, fmt.Errorf("flickercap: closed target")
	}
	var buf *C.uchar
	if rc := C.tipsy_fcap_full(t.dpy, t.win, C.int(t.Width), C.int(t.Height), &buf); rc != 0 {
		return nil, fmt.Errorf("flickercap: full grab failed (rc=%d)", int(rc))
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(buf)), t.Width*t.Height*4)
	out := make([]byte, len(raw))
	copy(out, raw)
	C.tipsy_fcap_free(buf)
	return out, nil
}
