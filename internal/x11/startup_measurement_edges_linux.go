// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: x11 xrandr xi xdamage
#cgo LDFLAGS: -lX11 -pthread
#cgo CFLAGS: -D_GNU_SOURCE
#include "x11.h"
*/
import "C"

func startupMeasurementSetMapObserver(w *Window, enabled bool) {
	v := C.int(0)
	if enabled {
		v = 1
	}
	C.tipsy_x11_startup_measurement_set_map_observer(C.uintptr_t(w.display), C.ulong(w.xid), v)
}

func startupMeasurementMapObserved(w *Window) bool {
	return C.tipsy_x11_startup_measurement_map_observed(C.uintptr_t(w.display), C.ulong(w.xid)) != 0
}

func startupMeasurementEnableDrawableObservation(w *Window) bool {
	return C.tipsy_x11_startup_measurement_enable_drawable_observation(C.uintptr_t(w.display), C.ulong(w.xid)) != 0
}

func startupMeasurementDisableDrawableObservation(w *Window) {
	C.tipsy_x11_startup_measurement_disable_drawable_observation(C.uintptr_t(w.display), C.ulong(w.xid))
}

func startupMeasurementArmDrawableObservation(w *Window) bool {
	return C.tipsy_x11_startup_measurement_arm_drawable_observation(C.uintptr_t(w.display), C.ulong(w.xid)) != 0
}

func startupMeasurementTakeDrawableObservation(w *Window) bool {
	return C.tipsy_x11_startup_measurement_take_drawable_observation(C.uintptr_t(w.display), C.ulong(w.xid)) != 0
}

// startupMeasurementDispatchVerticalScroll is the native half of the sealed
// test driver. It has no input parameters beyond Window's private owned
// handle; the C path always sends its one fixed Button5 wheel press.
func startupMeasurementDispatchVerticalScroll(w *Window) bool {
	return C.tipsy_x11_startup_measurement_dispatch_vertical_scroll(C.uintptr_t(w.display), C.ulong(w.xid)) != 0
}

// The helpers below mirror existing C-side X11 test seams. They set only the
// content-free private facts used by the public subscription logic and never
// touch a display, render, or inject input.
func testStartupMeasurementClear() {
	C.tipsy_x11_startup_measurement_test_clear()
}

func testStartupMeasurementMap(dpy, xid uintptr) {
	C.tipsy_x11_startup_measurement_test_observe_map(C.uintptr_t(dpy), C.ulong(xid))
}

func testStartupMeasurementDrawable(dpy, xid uintptr) {
	C.tipsy_x11_startup_measurement_test_observe_drawable(C.uintptr_t(dpy), C.ulong(xid))
}
