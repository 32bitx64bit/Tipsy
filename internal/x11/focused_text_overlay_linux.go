// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: x11 xext pangocairo pangoft2 cairo-xlib
#cgo LDFLAGS: -lm
#include "focused_overlay.h"
#include <stdlib.h>
#include <string.h>
*/
import "C"

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"
)

// FocusedTextOverlay is the platform View-equivalent for the APK's focused
// transparent RbxKeyboard EditText. It retains X11 pixmaps, never text.
type FocusedTextOverlay struct {
	mu     sync.Mutex
	window *Window
	native uintptr
	closed bool
}

// NewFocusedTextOverlay binds a transient text surface to w. The child is
// created lazily after genuine focused geometry arrives.
func NewFocusedTextOverlay(w *Window) (*FocusedTextOverlay, error) {
	if w == nil {
		return nil, ErrClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 || w.xid == 0 {
		return nil, ErrClosed
	}
	ptr := uintptr(C.tipsy_focused_overlay_new(
		C.uintptr_t(w.display), C.ulong(w.xid)))
	if ptr == 0 {
		return nil, fmt.Errorf("x11: focused text surface allocation")
	}
	return &FocusedTextOverlay{window: w, native: ptr}, nil
}

// Update composes s over a captured copy of the Roblox surface. Text crosses
// into one zeroed C allocation for this call and is never logged or retained.
func (o *FocusedTextOverlay) Update(s FocusedTextSnapshot) error {
	if o == nil {
		return ErrUnavailable
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.native == 0 || o.window == nil {
		return ErrClosed
	}
	o.window.mu.Lock()
	defer o.window.mu.Unlock()
	if o.window.closed || o.window.display == 0 {
		return ErrClosed
	}
	paint := prepareFocusedTextPaint(s, o.window.width, o.window.height)
	if !paint.visible {
		if C.tipsy_focused_overlay_update(C.uintptr_t(o.native), 0,
			0, C.int(o.window.width), C.int(o.window.height),
			0, 0, 0, 0, 0, 0, nil, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 0, nil, 0, 0) != 0 {
			return fmt.Errorf("x11: hide focused text surface")
		}
		return nil
	}
	textBytes := []byte(paint.text)
	defer wipeFocusedTextBytes(textBytes)
	buf := C.malloc(C.size_t(len(textBytes) + 1))
	if buf == nil {
		return fmt.Errorf("x11: focused text scratch allocation")
	}
	defer func() {
		C.memset(buf, 0, C.size_t(len(textBytes)+1))
		C.free(buf)
	}()
	if len(textBytes) > 0 {
		C.memcpy(buf, unsafe.Pointer(&textBytes[0]), C.size_t(len(textBytes)))
	}
	*(*byte)(unsafe.Add(buf, len(textBytes))) = 0
	if strings.IndexByte(paint.fontFile, 0) >= 0 {
		return fmt.Errorf("x11: invalid focused text font path")
	}
	fontFile := C.CString(paint.fontFile)
	defer C.free(unsafe.Pointer(fontFile))
	rc := C.tipsy_focused_overlay_update(C.uintptr_t(o.native), 1,
		C.uint64_t(paint.version), C.int(o.window.width), C.int(o.window.height),
		C.int(paint.x), C.int(paint.y), C.int(paint.width), C.int(paint.height),
		C.double(paint.fontSize), C.int(paint.font), fontFile,
		C.uint32_t(paint.argb), C.double(paint.letterSpacing),
		C.int(paint.paddingLeft), C.int(paint.paddingTop),
		C.int(paint.paddingRight), C.int(paint.paddingBottom),
		boolCInt(paint.includeFontPadding), C.int(paint.xAlignment),
		C.int(paint.yAlignment), boolCInt(paint.multiline),
		boolCInt(paint.wrapped), boolCInt(paint.editable),
		boolCInt(paint.cursorVisible), (*C.uchar)(buf),
		C.int(len(textBytes)), C.int(paint.cursorByte))
	if rc != 0 {
		return fmt.Errorf("x11: paint focused text surface")
	}
	return nil
}

func wipeFocusedTextBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func boolCInt(v bool) C.int {
	if v {
		return 1
	}
	return 0
}

// Close wipes and destroys captured surfaces before the parent closes.
func (o *FocusedTextOverlay) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	if o.window != nil {
		o.window.mu.Lock()
		if o.native != 0 && o.window.display != 0 {
			C.tipsy_focused_overlay_free(C.uintptr_t(o.native))
		}
		o.window.mu.Unlock()
	}
	o.native = 0
	o.closed = true
	return nil
}

type focusedTextOverlayTestState struct {
	FocusedTextOverlayDiagnostics
	x, y, width, height int
	backgroundPreserved bool
	antialiasedPixels   uint64
	backgroundPixels    uint64
	inputShapePixels    uint64
	textOriginX         int
	lineBoxTop          int
	lineBoxHeight       int
	baselineY           int
}

// queryForTest returns aggregate render health, measuring pixels on demand
// so existing focused-text tests pass without TIPSY_OVERLAY_PIXEL_DIAG.
func (o *FocusedTextOverlay) queryForTest() focusedTextOverlayTestState {
	return o.queryOverlayState(true)
}

// Diagnostics exposes content-free surface health for live validation.
// Pixel counts are populated only when paint ran with TIPSY_OVERLAY_PIXEL_DIAG=1;
// this path does not perform XGetImage readbacks.
func (o *FocusedTextOverlay) Diagnostics() FocusedTextOverlayDiagnostics {
	return o.queryOverlayState(false).FocusedTextOverlayDiagnostics
}

func (o *FocusedTextOverlay) queryOverlayState(measure bool) focusedTextOverlayTestState {
	var state focusedTextOverlayTestState
	if o == nil {
		return state
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.native == 0 || o.window == nil {
		return state
	}
	o.window.mu.Lock()
	defer o.window.mu.Unlock()
	if o.window.closed || o.window.display == 0 {
		return state
	}
	if measure {
		_ = C.tipsy_focused_overlay_measure_for_test(C.uintptr_t(o.native))
	}
	var cx, cy, cw, ch, origin, top, boxHeight, baseline C.int
	var painted, glyphs, caret, antialiased, bright, background, inputShape C.ulong
	var preserved C.int
	var requested C.uint32_t
	shown := C.tipsy_focused_overlay_query(C.uintptr_t(o.native),
		&cx, &cy, &cw, &ch, &painted, &preserved, &requested,
		&glyphs, &caret, &antialiased, &bright, &background,
		&origin, &top, &boxHeight, &baseline, &inputShape)
	state.x, state.y = int(cx), int(cy)
	state.width, state.height = int(cw), int(ch)
	state.backgroundPreserved = preserved != 0
	state.antialiasedPixels = uint64(antialiased)
	state.backgroundPixels = uint64(background)
	state.inputShapePixels = uint64(inputShape)
	state.textOriginX = int(origin)
	state.lineBoxTop = int(top)
	state.lineBoxHeight = int(boxHeight)
	state.baselineY = int(baseline)
	state.FocusedTextOverlayDiagnostics = FocusedTextOverlayDiagnostics{
		Mapped:            shown != 0,
		TextColorARGB:     uint32(requested),
		TextAlpha:         uint8(uint32(requested) >> 24),
		GlyphMaskPixels:   uint64(glyphs),
		CaretMaskPixels:   uint64(caret),
		PaintedPixels:     uint64(painted),
		BrightGlyphPixels: uint64(bright),
		AntialiasedPixels: uint64(antialiased),
		BackgroundPixels:  uint64(background),
	}
	return state
}

func (o *FocusedTextOverlay) fillParentAndSampleForTest(x, y, width, height int) (uint64, bool) {
	if o == nil || o.native == 0 {
		return 0, false
	}
	var pixel C.ulong
	rc := C.tipsy_focused_overlay_test_fill_parent(C.uintptr_t(o.native),
		C.int(x), C.int(y), C.int(width), C.int(height), &pixel)
	return uint64(pixel), rc == 0
}

func (o *FocusedTextOverlay) addVisibleUnderlayForTest(x, y, width, height int) (uint64, bool) {
	if o == nil || o.native == 0 {
		return 0, false
	}
	var pixel C.ulong
	rc := C.tipsy_focused_overlay_test_add_visible_underlay(C.uintptr_t(o.native),
		C.int(x), C.int(y), C.int(width), C.int(height), &pixel)
	return uint64(pixel), rc == 0
}

func (o *FocusedTextOverlay) changeVisibleUnderlayForTest() (uint64, bool) {
	if o == nil || o.native == 0 {
		return 0, false
	}
	var pixel C.ulong
	rc := C.tipsy_focused_overlay_test_change_visible_underlay(
		C.uintptr_t(o.native), &pixel)
	return uint64(pixel), rc == 0
}

func (o *FocusedTextOverlay) sampleRootForTest(parentX, parentY int) (uint64, bool) {
	if o == nil || o.native == 0 {
		return 0, false
	}
	var pixel C.ulong
	rc := C.tipsy_focused_overlay_test_root_pixel(C.uintptr_t(o.native),
		C.int(parentX), C.int(parentY), &pixel)
	return uint64(pixel), rc == 0
}

func (o *FocusedTextOverlay) exposeForTest() bool {
	return o != nil && o.native != 0 &&
		C.tipsy_focused_overlay_test_expose(C.uintptr_t(o.native)) == 0
}

func newUnfocusedWindowForFocusedOverlayTest(width, height int) (*Window, uintptr, func(), error) {
	var display C.uintptr_t
	var xid, focus C.ulong
	if C.tipsy_focused_overlay_test_window_open(C.int(width), C.int(height),
		&display, &xid, &focus) != 0 {
		return nil, 0, nil, ErrNoDisplay
	}
	w := &Window{display: uintptr(display), xid: uintptr(xid), width: width, height: height}
	cleanup := func() {
		w.mu.Lock()
		C.tipsy_focused_overlay_test_window_close(
			C.uintptr_t(w.display), C.ulong(w.xid))
		w.display, w.xid, w.closed = 0, 0, true
		w.mu.Unlock()
	}
	return w, uintptr(focus), cleanup, nil
}

func focusedOverlayTestFocus(w *Window) uintptr {
	if w == nil || w.display == 0 {
		return 0
	}
	return uintptr(C.tipsy_focused_overlay_test_focus(C.uintptr_t(w.display)))
}
