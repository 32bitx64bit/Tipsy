// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11/x11probe"
)

func focusStartupMeasurementScrollWindow(t *testing.T, w *Window) {
	t.Helper()
	requireProbe(t)
	if err := x11probe.SetFocus(w.XID()); err != nil {
		t.Fatalf("set owned focus: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := w.Pump(); err != nil && err != ErrClosed {
			t.Fatalf("pump focused window: %v", err)
		}
		if w.Focused() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned window never received FocusIn")
}

func nextStartupMeasurementScroll(t *testing.T, c *inputCollector, w *Window) InputEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case event := <-c.ch:
			if event.Kind == InputScroll {
				return event
			}
			if event.Kind != InputFocus && event.Kind != InputResize &&
				!(event.Kind == InputPointer && event.PointerAction == PointerMove && !event.Relative) {
				t.Fatalf("unexpected input before fixed startup scroll: %+v", event)
			}
		default:
		}
		if err := w.Pump(); err != nil && err != ErrClosed {
			t.Fatalf("pump startup scroll: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for fixed startup scroll")
	return InputEvent{}
}

func TestStartupMeasurementVerticalScrollTestDriverFailsClosed(t *testing.T) {
	resetStartupMeasurementForTest(t)
	w := &Window{display: 101, xid: 102, focused: true}

	// A MapNotify for another owned window never licenses this driver.
	testStartupMeasurementMap(101, 103)
	if got := w.NewStartupMeasurementVerticalScrollTestDriver(); got != nil {
		t.Fatal("foreign MapNotify enabled a startup-scroll driver")
	}

	// The same real fact is necessary but never sufficient without focus and
	// without an active Roblox pointer capture.
	testStartupMeasurementMap(101, 102)
	w.focused = false
	if got := w.NewStartupMeasurementVerticalScrollTestDriver(); got != nil {
		t.Fatal("unfocused window enabled a startup-scroll driver")
	}
	w.focused = true
	w.pointerCaptured = true
	if got := w.NewStartupMeasurementVerticalScrollTestDriver(); got != nil {
		t.Fatal("captured window enabled a startup-scroll driver")
	}
	w.pointerCaptured = false

	driver := w.NewStartupMeasurementVerticalScrollTestDriver()
	if driver == nil {
		t.Fatal("exact mapped, focused, uncaptured window did not create driver")
	}
	driver.Close()
	if driver.Dispatch() {
		t.Fatal("closed driver dispatched input")
	}
	if got := w.NewStartupMeasurementVerticalScrollTestDriver(); got != nil {
		t.Fatal("closed driver made a second startup-scroll capability available")
	}
}

func TestStartupMeasurementVerticalScrollTestDriverDispatchesOneFixedOwnedScroll(t *testing.T) {
	// Input injection is restricted to a private Xvfb server. This exercises
	// the actual XSendEvent -> Tipsy event-pump -> InputScroll contract, not a
	// callback or a test ring write.
	startXvfb(t)
	w := openInputWindow(t)
	c := collectInput(t)
	focusStartupMeasurementScrollWindow(t, w)
	c.clearAndSettle(t, w)

	driver := w.NewStartupMeasurementVerticalScrollTestDriver()
	if driver == nil {
		t.Fatal("exact mapped, focused, uncaptured X11 window did not create driver")
	}
	if !driver.Dispatch() {
		t.Fatal("fixed startup scroll did not dispatch")
	}
	if driver.Dispatch() {
		t.Fatal("one-shot startup-scroll driver dispatched a second input")
	}

	event := nextStartupMeasurementScroll(t, c, w)
	if event.ScrollX != 0 || event.ScrollY != -1 {
		t.Fatalf("fixed startup scroll = (%v, %v), want (0, -1)", event.ScrollX, event.ScrollY)
	}
	if wantX, wantY := float32(32), float32(32); event.X != wantX || event.Y != wantY {
		t.Fatalf("fixed startup scroll coordinates = (%v, %v), want (%v, %v)", event.X, event.Y, wantX, wantY)
	}

	// Continue pumping briefly: exactly one ButtonPress must create exactly
	// one InputScroll; no release or second detent is synthesized.
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := w.Pump(); err != nil && err != ErrClosed {
			t.Fatalf("pump after startup scroll: %v", err)
		}
		select {
		case extra := <-c.ch:
			if extra.Kind == InputScroll {
				t.Fatalf("one fixed dispatch delivered extra scroll: %+v", extra)
			}
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartupMeasurementVerticalScrollDispatchImmediatelyPrecedesDrawableArm(t *testing.T) {
	// This is an ordering check only. It does not claim that a wheel caused a
	// visible redraw; a later XDamage observation remains a separate edge.
	startXvfb(t)
	w := openInputWindow(t)
	focusStartupMeasurementScrollWindow(t, w)
	cancel, availability := w.OnStartupMeasurementEdges(StartupMeasurementEdges{
		PostScrollDrawableUpdate: func() {},
	})
	defer cancel()
	if !availability.PostScrollDrawableUpdate {
		t.Skip("XDamage unavailable on private X11 test server")
	}
	driver := w.NewStartupMeasurementVerticalScrollTestDriver()
	if driver == nil {
		t.Fatal("predeclared X11 window did not create startup-scroll driver")
	}
	if !driver.Dispatch() {
		t.Fatal("fixed startup scroll did not dispatch")
	}
	// No Pump or input callback is permitted between the completed send and
	// this declaration. The Runtime owner performs this same immediate call.
	if !w.DeclareStartupMeasurementVerticalScrollDispatch() {
		t.Fatal("immediate post-dispatch drawable arm failed")
	}
	if w.DeclareStartupMeasurementVerticalScrollDispatch() {
		t.Fatal("drawable arm accepted a second declaration")
	}
}
