// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package x11probe sends synthetic X11 input events to a window for tests.
// It exists because cgo is forbidden in x11 package _test.go files (the
// same constraint that created internal/graphics/flickercap). XSendEvent
// marks events send_event=true; the x11 decoder accepts both synthetic and
// real events. Test and diagnostics use only.
package x11probe

/*
#cgo pkg-config: x11 xrandr xtst
#include "probe.h"
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"
)

// ErrProbe is returned when the probe connection is unavailable.
var ErrProbe = errors.New("x11probe: unavailable")

func mustOpen() error {
	if C.probe_open() != 0 {
		return ErrProbe
	}
	return nil
}

// Open connects the probe's private X display.
func Open() error { return mustOpen() }

// Close disconnects the probe display.
func Close() { C.probe_close() }

// Focus sends a synthetic FocusIn/FocusOut for xid.
func Focus(xid uintptr, gained bool) error {
	if err := mustOpen(); err != nil {
		return err
	}
	g := C.int(0)
	if gained {
		g = 1
	}
	if C.probe_focus(C.ulong(xid), g) != 0 {
		return ErrProbe
	}
	return nil
}

// SetFocus performs a real XSetInputFocus from the probe connection.
func SetFocus(xid uintptr) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_set_focus(C.ulong(xid)) != 0 {
		return ErrProbe
	}
	return nil
}

// QueryFocus returns the server's current input-focus window.
func QueryFocus() uintptr {
	if err := mustOpen(); err != nil {
		return 0
	}
	return uintptr(C.probe_query_focus())
}

// Key sends a synthetic key press/release for keysym.
func Key(xid uintptr, keysym uint64, pressed bool) error {
	return KeyAt(xid, keysym, pressed, 0)
}

// KeyAt sends an event with an explicit server timestamp for legacy repeat tests.
func KeyAt(xid uintptr, keysym uint64, pressed bool, timestamp uint32) error {
	if err := mustOpen(); err != nil {
		return err
	}
	p := C.int(0)
	if pressed {
		p = 1
	}
	rc := C.probe_key(C.ulong(xid), C.ulong(keysym), p, C.ulong(timestamp))
	if rc == -2 {
		return errors.New("x11probe: keysym not in server keymap")
	}
	if rc != 0 {
		return ErrProbe
	}
	return nil
}

// Button sends a synthetic button press/release at (x, y).
func Button(xid uintptr, x, y int, button uint64, pressed bool) error {
	if err := mustOpen(); err != nil {
		return err
	}
	p := C.int(0)
	if pressed {
		p = 1
	}
	if C.probe_button(C.ulong(xid), C.int(x), C.int(y), C.uint(button), p) != 0 {
		return ErrProbe
	}
	return nil
}

// Motion sends a synthetic motion event with the given button state
// (e.g. 0x100 = Button1Mask) so the decoder treats it as drag motion.
func Motion(xid uintptr, x, y int, state uint64) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_motion(C.ulong(xid), C.int(x), C.int(y), C.uint(state)) != 0 {
		return ErrProbe
	}
	return nil
}

// WarpPointer moves the real server pointer to window-relative coordinates.
// Pointer-lock tests use it to exercise grab, relative delta, and recentering
// behavior without reading or generating user input.
func WarpPointer(xid uintptr, x, y int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_warp_pointer(C.ulong(xid), C.int(x), C.int(y)) != 0 {
		return ErrProbe
	}
	return nil
}

// RelativeMotion emits one real relative pointer movement through XTEST.
// It is limited to X11 integration tests and never inspects user input.
func RelativeMotion(dx, dy int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_relative_motion(C.int(dx), C.int(dy)) != 0 {
		return ErrProbe
	}
	return nil
}

// PointerPosition returns the real server pointer in window coordinates.
func PointerPosition(xid uintptr) (x, y int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var px, py C.int
	if C.probe_query_pointer(C.ulong(xid), &px, &py) != 0 {
		return 0, 0, ErrProbe
	}
	return int(px), int(py), nil
}

// WindowRootOrigin returns the window's top-left corner in root coordinates.
func WindowRootOrigin(xid uintptr) (x, y int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var px, py C.int
	if C.probe_window_root_origin(C.ulong(xid), &px, &py) != 0 {
		return 0, 0, ErrProbe
	}
	return int(px), int(py), nil
}

// GrabPointer acquires a competing grab from the probe connection. Tests use
// it to verify that Tipsy reports grab rejection without swallowing edges.
func GrabPointer(xid uintptr) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_grab_pointer(C.ulong(xid)) != 0 {
		return ErrProbe
	}
	return nil
}

// UngrabPointer releases a probe-owned grab.
func UngrabPointer() { C.probe_ungrab_pointer() }

// Resize asks X11 to resize the client window. Tests use the resulting real
// ConfigureNotify to assert resize/pointer ordering in the capture bridge.
func Resize(xid uintptr, width, height int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if width <= 0 || height <= 0 || C.probe_resize(C.ulong(xid), C.uint(width), C.uint(height)) != 0 {
		return ErrProbe
	}
	return nil
}

// WindowSize reports the current server-side client geometry in X11 pixels.
func WindowSize(xid uintptr) (width, height int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var w, h C.int
	if C.probe_window_size(C.ulong(xid), &w, &h) != 0 {
		return 0, 0, ErrProbe
	}
	return int(w), int(h), nil
}

// WindowMinimumSize reports the WM_NORMAL_HINTS minimum client geometry.
func WindowMinimumSize(xid uintptr) (width, height int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var w, h C.int
	if C.probe_window_min_size(C.ulong(xid), &w, &h) != 0 {
		return 0, 0, ErrProbe
	}
	return int(w), int(h), nil
}

// WMDelete sends the standards-defined ICCCM request used by a window
// manager's title-bar close button.
func WMDelete(xid uintptr) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_wm_delete(C.ulong(xid)) != 0 {
		return ErrProbe
	}
	return nil
}

// Viewable reports whether the server currently considers xid mapped and
// viewable.
func Viewable(xid uintptr) bool {
	if err := mustOpen(); err != nil {
		return false
	}
	return C.probe_is_viewable(C.ulong(xid)) != 0
}

// WindowTitle returns the modern EWMH UTF-8 title for xid.
func WindowTitle(xid uintptr) (string, error) {
	if err := mustOpen(); err != nil {
		return "", err
	}
	var buf [1024]C.char
	n := C.probe_get_utf8_title(C.ulong(xid), &buf[0], C.int(len(buf)))
	if n < 0 {
		return "", ErrProbe
	}
	return C.GoStringN((*C.char)(unsafe.Pointer(&buf[0])), n), nil
}

// WindowIconInfo returns the first EWMH icon dimensions and total 32-bit
// property item count.
func WindowIconInfo(xid uintptr) (width, height, items uint64, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, 0, err
	}
	var w, h, n C.ulong
	if C.probe_get_icon_info(C.ulong(xid), &w, &h, &n) != 0 {
		return 0, 0, 0, ErrProbe
	}
	return uint64(w), uint64(h), uint64(n), nil
}

// WindowManagerPresent reports whether the root window advertises an EWMH WM.
func WindowManagerPresent() bool {
	if err := mustOpen(); err != nil {
		return false
	}
	return C.probe_window_manager_present() != 0
}

// Fullscreen reports whether _NET_WM_STATE currently contains FULLSCREEN.
func Fullscreen(xid uintptr) bool {
	if err := mustOpen(); err != nil {
		return false
	}
	return C.probe_has_fullscreen(C.ulong(xid)) != 0
}

// WaitFullscreen waits for the asynchronous EWMH state transition.
func WaitFullscreen(xid uintptr, enabled bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if Fullscreen(xid) == enabled {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return Fullscreen(xid) == enabled
}

// Move asks X11 to move the client window and emit ConfigureNotify.
func Move(xid uintptr, x, y int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_move(C.ulong(xid), C.int(x), C.int(y)) != 0 {
		return ErrProbe
	}
	return nil
}

// RandROutputProperty emits a real RandR output-property notification. Only
// call on a private test display: this briefly creates and deletes a test
// property on an output, without changing the output mode or connection.
func RandROutputProperty() error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_randr_output_property() != 0 {
		return ErrProbe
	}
	return nil
}

// DetectableRepeat changes only the given client connection. Its pump must be stopped.
func DetectableRepeat(display uintptr, enabled bool) error {
	flag := C.int(0)
	if enabled {
		flag = 1
	}
	if C.probe_detectable_repeat(C.uintptr_t(display), flag) != 0 {
		return ErrProbe
	}
	return nil
}

// RepeatRate changes the server's repeat settings. Use only on a private test server.
func RepeatRate(delay, interval uint32) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_repeat_rate(C.uint(delay), C.uint(interval)) != 0 {
		return ErrProbe
	}
	return nil
}

// RealKey injects a physical transition through XTest; the server generates repeats.
// Use only on a private test server and always release a pressed key.
func RealKey(keysym uint64, pressed bool) error {
	if err := mustOpen(); err != nil {
		return err
	}
	flag := C.int(0)
	if pressed {
		flag = 1
	}
	if C.probe_real_key(C.ulong(keysym), flag) != 0 {
		return ErrProbe
	}
	return nil
}

// Sync waits for the server to process all preceding probe requests.
func Sync() { C.probe_sync() }
