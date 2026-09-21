// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

// StartupMeasurementVerticalScrollTestDriver is the one-shot, test-only
// interaction interface for an opt-in startup-measurement session. Dispatch
// sends exactly one fixed wheel detent to the owned Window, or nothing; the
// Runtime test owner must call its recorder declaration immediately after a
// true return, before yielding to the input pump.
type StartupMeasurementVerticalScrollTestDriver interface {
	Dispatch() bool
	Close()
}

type startupMeasurementVerticalScrollTestDriver struct {
	window     *Window
	generation uint64
}

// NewStartupMeasurementVerticalScrollTestDriver returns the sole sealed
// interaction capability for an opt-in startup-measurement test session; it
// performs no input dispatch itself. A nil return is fail-closed: the caller
// must leave the metric unavailable rather than infer a scroll from focus,
// mapping, a timer, a redraw, or ordinary input.
//
// It requires a mapped, focused, non-pointer-captured Window; Dispatch
// repeats those checks at the instant it sends.
func (w *Window) NewStartupMeasurementVerticalScrollTestDriver() StartupMeasurementVerticalScrollTestDriver {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 || w.xid == 0 || !w.focused ||
		w.pointerCaptured || w.startupMeasurementScrollTestDriverGeneration != 0 ||
		!startupMeasurementMapObserved(w) {
		return nil
	}
	w.startupMeasurementScrollTestDriverGeneration++
	w.startupMeasurementScrollTestDriverActive = true
	return &startupMeasurementVerticalScrollTestDriver{
		window:     w,
		generation: w.startupMeasurementScrollTestDriverGeneration,
	}
}

// Dispatch sends one fixed Button5 press: one downward vertical wheel detent
// at the owned client's center. There is no button release because X11 wheel
// releases do not represent another scroll step. It returns true only after
// the native send/synchronize path completed; a false result sends no event
// and permanently consumes this driver.
//
// After a true return, the Runtime owner must immediately call its declaration
// helper in the same control flow, before any Pump or input callback can run.
func (d *startupMeasurementVerticalScrollTestDriver) Dispatch() bool {
	if d == nil || d.window == nil || d.generation == 0 {
		return false
	}
	w := d.window
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 || w.xid == 0 ||
		!w.startupMeasurementScrollTestDriverActive ||
		w.startupMeasurementScrollTestDriverGeneration != d.generation ||
		w.startupMeasurementScrollTestDriverDispatched || !w.focused || w.pointerCaptured {
		return false
	}
	// A failed precondition or native send consumes this sealed one-shot
	// capability.
	w.startupMeasurementScrollTestDriverActive = false
	if !startupMeasurementDispatchVerticalScroll(w) {
		return false
	}
	w.startupMeasurementScrollTestDriverDispatched = true
	return true
}

// Close invalidates an unused test driver without sending input. It cannot
// make another driver available for this Window.
func (d *startupMeasurementVerticalScrollTestDriver) Close() {
	if d == nil || d.window == nil || d.generation == 0 {
		return
	}
	w := d.window
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startupMeasurementScrollTestDriverGeneration == d.generation {
		w.startupMeasurementScrollTestDriverActive = false
	}
}

func (w *Window) clearStartupMeasurementScrollTestDriverLocked() {
	if w != nil {
		w.startupMeasurementScrollTestDriverActive = false
	}
}
