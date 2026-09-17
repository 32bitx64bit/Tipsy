// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: pangocairo pangoft2 cairo
#cgo LDFLAGS: -lm
#include "focused_overlay.h"
#include "focused_text_foreground.h"
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
// RbxKeyboard EditText. It retains only a text/caret alpha foreground for the
// host's pre-present compositor, never text or X11/root background pixels.
type FocusedTextOverlay struct {
	mu     sync.Mutex
	window *Window
	native uintptr
	closed bool
}

// NewFocusedTextOverlay binds a transient foreground rasterizer to w. It has
// no X11 child/window and works with desktop compositing disabled.
func NewFocusedTextOverlay(w *Window) (*FocusedTextOverlay, error) {
	if w == nil {
		return nil, ErrClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, ErrClosed
	}
	ptr := uintptr(C.tipsy_focused_overlay_new())
	if ptr == 0 {
		return nil, fmt.Errorf("x11: focused text surface allocation")
	}
	return &FocusedTextOverlay{window: w, native: ptr}, nil
}

// Update replaces s's dirty text-only premultiplied-alpha foreground. Text
// crosses into one zeroed C allocation for this call and is never logged or
// retained; graphics consumes the leased foreground before its next update.
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
	if o.window.closed {
		return ErrClosed
	}
	paint := prepareFocusedTextPaint(s, o.window.width, o.window.height)
	if !paint.visible {
		if C.tipsy_focused_overlay_update(C.uintptr_t(o.native), 0,
			0,
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
		C.uint64_t(paint.version),
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

// Close unpublishes, wipes, and destroys the foreground before the parent
// closes.
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
		if o.native != 0 {
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
	textOriginX         int
	lineBoxTop          int
	lineBoxHeight       int
	baselineY           int
}

// queryForTest returns content-free aggregate render health.
func (o *FocusedTextOverlay) queryForTest() focusedTextOverlayTestState {
	return o.queryOverlayState()
}

// Diagnostics measures content-free aggregate foreground health on demand. It
// reads only the owned CPU alpha foreground and performs no X11 operation.
func (o *FocusedTextOverlay) Diagnostics() FocusedTextOverlayDiagnostics {
	return o.queryOverlayState().FocusedTextOverlayDiagnostics
}

func (o *FocusedTextOverlay) queryOverlayState() focusedTextOverlayTestState {
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
	if o.window.closed {
		return state
	}
	var metrics C.struct_tipsy_focused_overlay_metrics
	shown := C.tipsy_focused_overlay_query(C.uintptr_t(o.native), &metrics)
	state.x, state.y = int(metrics.x), int(metrics.y)
	state.width, state.height = int(metrics.width), int(metrics.height)
	state.textOriginX = int(metrics.text_origin_x)
	state.lineBoxTop = int(metrics.line_box_top)
	state.lineBoxHeight = int(metrics.line_box_height)
	state.baselineY = int(metrics.baseline_y)
	painted := uint64(metrics.glyph_pixels) + uint64(metrics.caret_pixels)
	requested := uint32(metrics.requested_argb)
	state.FocusedTextOverlayDiagnostics = FocusedTextOverlayDiagnostics{
		Mapped:            shown != 0,
		UsesARGB:          true,
		TextColorARGB:     requested,
		TextAlpha:         uint8(requested >> 24),
		GlyphMaskPixels:   uint64(metrics.glyph_pixels),
		CaretMaskPixels:   uint64(metrics.caret_pixels),
		PaintedPixels:     painted,
		BrightGlyphPixels: uint64(metrics.bright_pixels),
		AntialiasedPixels: uint64(metrics.antialias_pixels),
	}
	return state
}

func (o *FocusedTextOverlay) foregroundAlphaForTest(x, y int) (uint64, bool) {
	if o == nil || o.native == 0 {
		return 0, false
	}
	var alpha C.ulong
	rc := C.tipsy_focused_overlay_test_foreground_alpha(C.uintptr_t(o.native),
		C.int(x), C.int(y), &alpha)
	return uint64(alpha), rc == 0
}

// focusedTextForegroundLeaseForTest exercises the exact C lease consumed by
// the host compositor without exposing pixels, text, or a general renderer.
type focusedTextForegroundLeaseForTest struct {
	x, y, width, height, stride int
	generation                  uint64
	lease                       uintptr
}

func acquireFocusedTextForegroundForTest() (focusedTextForegroundLeaseForTest, bool) {
	var frame C.struct_tipsy_focused_text_frame
	if C.tipsy_focused_text_frame_acquire(&frame) == 0 {
		return focusedTextForegroundLeaseForTest{}, false
	}
	return focusedTextForegroundLeaseForTest{
		x:          int(frame.x),
		y:          int(frame.y),
		width:      int(frame.width),
		height:     int(frame.height),
		stride:     int(frame.stride),
		generation: uint64(frame.generation),
		lease:      uintptr(frame.lease),
	}, true
}

func (f focusedTextForegroundLeaseForTest) release() {
	if f.lease != 0 {
		C.tipsy_focused_text_frame_release(C.uintptr_t(f.lease))
	}
}

func focusedTextOverlayLiveForTest() bool {
	return C.tipsy_focused_text_overlay_live() != 0
}
