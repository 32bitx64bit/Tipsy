// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: x11 xrandr xi
#cgo LDFLAGS: -lX11 -pthread
#cgo CFLAGS: -D_GNU_SOURCE
#include "x11.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

const maxListedOutputs = 32

// TIPSYInputRingLen mirrors the C input ring capacity. The ring is
// process-wide; Tipsy owns one Roblox window per process.
const TIPSYInputRingLen = 256

// inputTextCap is the committed-UTF-8 cap, matching TIPSY_INPUT_TEXT_BYTES.
const inputTextCap = 256

// inputDrainScratch is the once-per-window C drain buffer. Event slots are
// ~40 B; IME text lives in a parallel array and is wiped after decode.
type inputDrainScratch struct {
	evs   [TIPSYInputRingLen]C.struct_tipsy_input_ev
	texts [TIPSYInputRingLen][inputTextCap]byte
}

func (w *Window) inputDrainScratch() *inputDrainScratch {
	if s, ok := w.inputScratch.(*inputDrainScratch); ok && s != nil {
		return s
	}
	s := new(inputDrainScratch)
	w.inputScratch = s
	return s
}

func wipeInputScratch(w *Window) {
	if w == nil {
		return
	}
	if s, ok := w.inputScratch.(*inputDrainScratch); ok && s != nil {
		for i := range s.texts {
			clear(s.texts[i][:])
		}
	}
}

func testClearInputRing() {
	C.tipsy_x11_input_test_clear()
}

func testPushPointer(action int32, button int32, x, y float32) {
	C.tipsy_x11_input_test_push(C.TIPSY_INPUT_POINTER, C.int(action), C.long(button), 0, C.float(x), C.float(y))
}

func testPushText(s string) {
	if s == "" {
		return
	}
	p := C.CString(s)
	defer C.free(unsafe.Pointer(p))
	C.tipsy_x11_input_test_push_text(p, C.int(len(s)))
}

func testInputTextSlotsClean() bool {
	return C.tipsy_x11_input_test_text_slots_clean() != 0
}

func testLastPumpRawSamples() int {
	return int(C.tipsy_x11_test_last_pump_raw_samples())
}

func testLastPumpWarps() int {
	return int(C.tipsy_x11_test_last_pump_warps())
}

func testCoalesceRawPump(n int) int {
	return int(C.tipsy_x11_test_coalesce_raw_pump(C.int(n)))
}

func inputABISizes() (evBytes, textBytes int) {
	return int(C.tipsy_x11_input_ev_size()), int(C.TIPSY_INPUT_TEXT_BYTES)
}

// ListOutputs reports connected XRandR outputs on the current DISPLAY.
func ListOutputs() ([]Output, error) {
	var raw [maxListedOutputs]C.tipsy_xrr_output
	n := int(C.tipsy_x11_list_outputs(&raw[0], C.int(len(raw))))
	if n < 0 {
		return nil, fmt.Errorf("%w (DISPLAY=%q)", ErrNoDisplay, os.Getenv("DISPLAY"))
	}
	out := make([]Output, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Output{
			Name:    C.GoString(&raw[i].name[0]),
			X:       int(raw[i].x),
			Y:       int(raw[i].y),
			Width:   int(raw[i].width),
			Height:  int(raw[i].height),
			Primary: raw[i].primary != 0,
		})
	}
	return out, nil
}

// Open creates a mapped InputOutput window on the native X11 display.
// Placement is left to the window manager (typically the pointer) unless
// OpenOnDisplay is used with a target monitor.
func Open(title string, width, height int) (*Window, error) {
	return OpenOnDisplay(title, width, height, DisplayPointer)
}

// OpenOnDisplay creates a mapped InputOutput window. display is "primary"
// (default), "pointer" for window-manager mouse placement, or an output name.
func OpenOnDisplay(title string, width, height int, display string) (*Window, error) {
	if width < 1 || height < 1 {
		return nil, ErrInvalidSize
	}
	robloxWindow := title == "Roblox" || title == RobloxWindowTitle
	if title == "Roblox" {
		title = RobloxWindowTitle
	}
	minWidth, minHeight := 1, 1
	if robloxWindow {
		minWidth, minHeight = RobloxMinimumWidth, RobloxMinimumHeight
		if width < minWidth {
			width = minWidth
		}
		if height < minHeight {
			height = minHeight
		}
	}
	placeX, placeY, usePosition := 0, 0, 0
	outputs, err := ListOutputs()
	if err == nil {
		x, y, force := ResolvePlacement(display, width, height, outputs)
		if force {
			placeX, placeY, usePosition = x, y, 1
		}
	}
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))
	icon32, err := windowIconARGB()
	if err != nil {
		return nil, err
	}
	icon := make([]C.ulong, len(icon32))
	for i, value := range icon32 {
		icon[i] = C.ulong(value)
	}
	var iconPtr *C.ulong
	if len(icon) != 0 {
		iconPtr = &icon[0]
	}

	var dpy C.uintptr_t
	var xid, del C.ulong
	var randrEventBase C.int
	rc := C.tipsy_x11_open(ctitle, C.int(width), C.int(height),
		C.int(minWidth), C.int(minHeight),
		C.int(placeX), C.int(placeY), C.int(usePosition),
		iconPtr, C.int(len(icon)), &dpy, &xid, &del, &randrEventBase)
	if rc != 0 || dpy == 0 || xid == 0 {
		return nil, fmt.Errorf("%w (DISPLAY=%q)", ErrNoDisplay, os.Getenv("DISPLAY"))
	}

	w := &Window{
		display:        uintptr(dpy),
		xid:            uintptr(xid),
		wmDelete:       uintptr(del),
		randrEventBase: int(randrEventBase),
		width:          width,
		height:         height,
	}
	setActiveWindow(w)
	// Roblox renders its own cursor. This transparent cursor is scoped to the
	// client window: leaving it returns to the host cursor automatically.
	w.cursor = uintptr(C.tipsy_x11_hide_cursor(C.uintptr_t(w.display), C.ulong(w.xid)))
	if w.cursor == 0 {
		logging.Logger(logging.CatX11).Info("X11 cursor hide unavailable")
	}
	_ = w.Pump()
	logging.Logger(logging.CatX11).Info("opened X11 window",
		"title", title, "width", w.width, "height", w.height, "xid", w.xid,
		"display", normalizeDisplay(display), "placeX", placeX, "placeY", placeY,
		"forced", usePosition != 0)
	return w, nil
}

// setFullscreenLocked sends the EWMH state request while the Window mutex is
// held. The WM applies it asynchronously and reports the resulting geometry
// through ConfigureNotify.
func setFullscreenLocked(w *Window, enabled bool) error {
	value := C.int(0)
	if enabled {
		value = 1
	}
	if C.tipsy_x11_request_fullscreen(C.uintptr_t(w.display), C.ulong(w.xid), value) != 0 {
		return ErrFullscreen
	}
	return nil
}

func dismissLocked(w *Window) error {
	if C.tipsy_x11_unmap(C.uintptr_t(w.display), C.ulong(w.xid)) != 0 {
		return ErrClosed
	}
	w.dismissed = true
	logging.Logger(logging.CatX11).Info("dismissed X11 window", "xid", w.xid)
	return nil
}

// setCursorVisibleLocked swaps the transparent Roblox cursor for the
// inherited host cursor and back. Showing undefines and frees the owned
// transparent cursor; hiding defines a fresh one. Both are window-scoped.
func setCursorVisibleLocked(w *Window, visible bool) {
	if visible {
		if w.cursor != 0 {
			C.tipsy_x11_restore_cursor(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.cursor))
			w.cursor = 0
			logging.Logger(logging.CatX11).Info("X11 cursor shown", "xid", w.xid)
		}
		return
	}
	if w.cursor == 0 {
		w.cursor = uintptr(C.tipsy_x11_hide_cursor(C.uintptr_t(w.display), C.ulong(w.xid)))
		if w.cursor == 0 {
			logging.Logger(logging.CatX11).Info("X11 cursor hide unavailable")
		}
	}
}

func setPointerLockLocked(w *Window, locked bool, anchor pointerAnchor) (bool, error) {
	value := C.int(0)
	if locked {
		value = 1
	}
	var anchorX, anchorY, status C.int
	rc := int(C.tipsy_x11_set_pointer_lock(C.uintptr_t(w.display), C.ulong(w.xid),
		value, C.int(anchor), &anchorX, &anchorY, &status))
	switch rc {
	case 1:
		w.pointerCaptured = true
		w.pointerAnchorX = int(anchorX)
		w.pointerAnchorY = int(anchorY)
		logging.Logger(logging.CatX11).Info("X11 pointer lock acquired",
			"xid", w.xid, "anchorX", w.pointerAnchorX, "anchorY", w.pointerAnchorY,
			"center", anchor != anchorCursor, "sticky", anchor == anchorCenterSticky)
		return true, nil
	case 2:
		w.pointerCaptured = false
		logging.Logger(logging.CatX11).Info("X11 pointer lock released", "xid", w.xid)
		return true, nil
	case -1:
		logging.Logger(logging.CatX11).Error("X11 pointer lock rejected",
			"xid", w.xid, "grabStatus", int(status))
		return false, fmt.Errorf("%w (status=%d)", ErrPointerGrab, int(status))
	case -2:
		return false, ErrClosed
	default:
		return false, nil
	}
}

// Pump drains the input ring into Go subscribers. While StartBackgroundPump
// is running, the C thread is the only XPending/XNextEvent reader; Pump
// does not call tipsy_x11_pump. Without a background pump (unit tests), Pump
// still consumes X events itself so tests stay single-consumer.
func (w *Window) Pump() error {
	if w == nil {
		return ErrClosed
	}
	w.mu.Lock()
	if w.closed || w.display == 0 {
		w.mu.Unlock()
		return ErrClosed
	}
	C.tipsy_x11_wake_ack()
	if C.tipsy_x11_io_error() != 0 {
		w.closed = true
		w.mu.Unlock()
		return ErrClosed
	}

	var closed C.int
	if w.pump == 0 {
		cw := C.int(w.width)
		ch := C.int(w.height)
		rc := C.tipsy_x11_pump(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.wmDelete), C.int(w.randrEventBase), &cw, &ch, &closed)
		w.width = int(cw)
		w.height = int(ch)
		if rc != 0 {
			w.closed = true
			w.mu.Unlock()
			return ErrClosed
		}
	}
	evs, closeRequested := w.drainInputLocked()
	if closed != 0 || closeRequested {
		w.closed = true
		_ = dismissLocked(w)
		w.mu.Unlock()
		notifyInput(evs)
		clear(evs)
		return ErrClosed
	}
	w.mu.Unlock()
	notifyInput(evs)
	clear(evs)
	return nil
}

// drainInputLocked moves captured events from the C ring into Go events
// and applies focus state. Called with w.mu held. Reuses window scratch so
// the ~40 B slots do not escape to a new 80 KiB heap allocation per wake.
func (w *Window) drainInputLocked() ([]InputEvent, bool) {
	s := w.inputDrainScratch()
	n := int(C.tipsy_x11_input_drain(&s.evs[0], (*C.char)(unsafe.Pointer(&s.texts[0][0])), C.int(len(s.evs))))
	if n == 0 {
		return nil, false
	}
	if cap(w.inputEvents) < n {
		w.inputEvents = make([]InputEvent, n)
	}
	evs := w.inputEvents[:0]
	closeRequested := false
	for i := 0; i < n; i++ {
		r := &s.evs[i]
		switch r.kind {
		case C.TIPSY_INPUT_FOCUS:
			gained := r.a != 0
			w.focused = gained
			evs = append(evs, InputEvent{Kind: InputFocus, FocusGained: gained, FocusAltHeld: r.b != 0})
			logging.Logger(logging.CatX11).Info("window focus", "gained", gained)
		case C.TIPSY_INPUT_KEY:
			if r.b == 0 {
				// Keep the actual X11 edge for diagnostics/subscribers, but
				// account for the absence of an Android physical key code.
				// The direct JNI route rejects KeyCode 0 rather than inventing
				// text or a guessed mapping.
				inputMu.Lock()
				inputDrops++
				inputMu.Unlock()
			}
			evs = append(evs, InputEvent{Kind: InputKey, KeyPressed: r.a != 0, KeyCode: int32(r.b), ScanCode: int32(r.c), RepeatCount: int32(r.repeat_count)})
		case C.TIPSY_INPUT_POINTER:
			action, relative := decodePointerRingAction(int32(r.a))
			ev := InputEvent{Kind: InputPointer, PointerAction: action, Button: int32(r.b), X: float32(r.x), Y: float32(r.y), Relative: relative}
			if relative {
				ev.Button = 0
				ev.DeltaX = float32(r.dx)
				ev.DeltaY = float32(r.dy)
			}
			evs = append(evs, ev)
		case C.TIPSY_INPUT_SCROLL:
			evs = append(evs, InputEvent{Kind: InputScroll, X: float32(r.x), Y: float32(r.y), ScrollX: float32(r.a), ScrollY: float32(r.b)})
		case C.TIPSY_INPUT_RESIZE:
			width, height := int(r.b), int(r.c)
			if width <= 0 || height <= 0 {
				continue
			}
			w.width, w.height = width, height
			if w.pointerCaptured {
				w.pointerAnchorX = width / 2
				w.pointerAnchorY = height / 2
			}
			evs = append(evs, InputEvent{Kind: InputResize, Width: width, Height: height})
		case C.TIPSY_INPUT_TEXT:
			ntext := int(r.text_len)
			if ntext <= 0 || ntext > inputTextCap {
				clear(s.texts[i][:])
				continue
			}
			text := C.GoStringN((*C.char)(unsafe.Pointer(&s.texts[i][0])), C.int(ntext))
			clear(s.texts[i][:])
			if text == "" {
				continue
			}
			// Text can contain credentials. Preserve it for the subscribed
			// editor adapter, but never log it here.
			evs = append(evs, InputEvent{Kind: InputText, Text: text})
		case C.TIPSY_INPUT_CLOSE:
			// The C ring is process-global because Tipsy hosts one Roblox
			// window. Still match the XID so a late close from an already
			// destroyed test window cannot close a later one.
			if uintptr(r.b) == w.xid {
				closeRequested = true
			}
		case C.TIPSY_INPUT_CAPTURE:
			if uintptr(r.c) != w.xid {
				continue
			}
			ev := InputEvent{Kind: InputPointerCapture, X: float32(r.x), Y: float32(r.y)}
			switch r.a {
			case 1:
				ev.Captured = true
				w.pointerCaptured = true
				w.pointerAnchorX, w.pointerAnchorY = int(r.x), int(r.y)
			case 2:
				ev.CaptureFailed = true
				ev.CaptureStatus = int32(r.b)
			default:
				w.pointerCaptured = false
			}
			evs = append(evs, ev)
		}
	}
	return evs, closeRequested
}

// StartBackgroundPump starts the exclusive C X-event reader so V2Start can
// block on C Main while Go only drains the input ring. Do not Swap/EGL here.
func (w *Window) StartBackgroundPump() error {
	if w == nil {
		return ErrClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 {
		return ErrClosed
	}
	if w.pump != 0 {
		return nil
	}
	p := C.tipsy_x11_pump_thread_start(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.wmDelete), C.int(w.randrEventBase), C.int(w.width), C.int(w.height))
	if p == 0 {
		return fmt.Errorf("x11: background pump thread")
	}
	w.pump = uintptr(p)
	return nil
}

// StopBackgroundPump joins the C pump thread started by StartBackgroundPump.
func (w *Window) StopBackgroundPump() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopBackgroundPumpLocked()
}

func (w *Window) stopBackgroundPumpLocked() error {
	if w.pump == 0 {
		return nil
	}
	cw, ch := C.int(w.width), C.int(w.height)
	var closed C.int
	C.tipsy_x11_pump_thread_stop(C.uintptr_t(w.pump), &cw, &ch, &closed)
	w.pump = 0
	w.width = int(cw)
	w.height = int(ch)
	if closed != 0 {
		w.closed = true
		return ErrClosed
	}
	return nil
}

// Close destroys the window and closes the X display connection.
func (w *Window) Close() error {
	if w == nil {
		return nil
	}
	clearActiveWindow(w)
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.stopBackgroundPumpLocked()
	if w.display == 0 {
		w.closed = true
		wipeInputScratch(w)
		return nil
	}
	if w.cursor != 0 {
		C.tipsy_x11_restore_cursor(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.cursor))
		w.cursor = 0
	}
	C.tipsy_x11_close(C.uintptr_t(w.display), C.ulong(w.xid))
	logging.Logger(logging.CatX11).Info("closed X11 window", "xid", w.xid)
	w.display = 0
	w.xid = 0
	w.closed = true
	w.dismissed = true
	w.pointerCaptured = false
	wipeInputScratch(w)
	return nil
}

// RefreshVersion changes after the event reader observes a client move/resize,
// reparent/map, or RandR display-configuration event. Reading it performs no
// X-server query. Call Pump first when no background event reader is running.
// A zero version means the window is closed.
func (w *Window) RefreshVersion() uint64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 {
		return 0
	}
	return uint64(C.tipsy_x11_refresh_version())
}

// WakeEventPump wakes the event reader after another shared-display Xlib
// caller may have buffered events while waiting for a server reply.
func WakeEventPump() {
	C.tipsy_nudge_pump()
}
