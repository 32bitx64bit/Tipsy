// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

// StartupMeasurementVerticalScrollTestDriver is the narrow, one-shot
// test-only interaction interface for a separately opt-in startup measurement
// session. It has no caller-provided coordinates, count, keys, text, or
// arbitrary action: Dispatch sends exactly one downward vertical wheel detent
// to the owned Window, or it sends nothing.
//
// The Runtime test owner must call its recorder declaration immediately after
// a true Dispatch return, before yielding to the input pump, so the XDamage
// one-shot arm is established before the queued wheel reaches Roblox.
type StartupMeasurementVerticalScrollTestDriver interface {
	Dispatch() bool
	Close()
}

type startupMeasurementVerticalScrollTestDriver struct {
	window     *Window
	generation uint64
}

// NewStartupMeasurementVerticalScrollTestDriver returns the sole sealed
// interaction capability for an opt-in startup-measurement test session. It
// performs no input dispatch itself. A nil return is fail-closed: the caller
// must leave the scroll/update metric unavailable rather than infer a scroll
// from focus, mapping, a timer, a redraw, or ordinary input.
//
// A driver is available only after that exact owned Window has produced its
// real MapNotify and is currently focused and not pointer-captured. Dispatch
// repeats those focus/capture checks at the instant it sends the event. Normal
// launches never call this method.
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

// Dispatch sends one fixed Button5 press: one downward vertical core-X11
// wheel detent at the owned client's center. There is deliberately no button
// release because X11 wheel releases do not represent another scroll step.
// It returns true only after the native X11 send/synchronize path completed;
// a false result sends no event and permanently consumes this test driver.
//
// After a true return, the startup-measurement Runtime owner must immediately
// call its existing declaration helper in the same control flow, before any
// Pump or input callback can run. That declaration clears old XDamage before
// arming one later drawable update; Dispatch does not arm or observe damage.
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
	// A failed precondition or native send consumes this deliberately sealed
	// one-shot capability. Retrying through an arbitrary loop would turn the
	// test boundary into general input automation.
	w.startupMeasurementScrollTestDriverActive = false
	if !startupMeasurementDispatchVerticalScroll(w) {
		return false
	}
	w.startupMeasurementScrollTestDriverDispatched = true
	return true
}

// Close invalidates an unused test driver without sending input. It is safe
// to defer from the Runtime test arm's teardown and cannot make another
// driver available for this Window.
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
