// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"errors"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11/x11probe"
)

// These tests pin the grab-mode focus policy and the corroborated-ungrab
// rule. A keyboard grab activated by another client makes the X server emit
// FocusOut/FocusIn edges with mode NotifyGrab/NotifyUngrab on the owned
// window; treating those as real focus let a background window claim focus
// and re-acquire the desktop pointer grab. The real-grab test runs in both
// default and official_release builds (no synthetic opt-in needed); the
// forged-mode matrix additionally exercises the development opt-in.

func TestFocusModeActionPolicy(t *testing.T) {
	cases := []struct {
		mode int
		want int
	}{
		{focusModeNormal, focusModeApply},
		{focusModeGrab, focusModeIgnore},
		{focusModeWhileGrabbed, focusModeIgnore},
		{focusModeUngrab, focusModeCorroborate},
		{99, focusModeIgnore}, // unknown modes fail closed
	}
	for _, tc := range cases {
		if got := testFocusModeAction(tc.mode); got != tc.want {
			t.Fatalf("focus mode %d action = %d, want %d", tc.mode, got, tc.want)
		}
	}
}

// waitFocused pumps until the window's observed focus state matches want.
func waitFocused(t *testing.T, w *Window, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Focused() == want {
			return
		}
		if err := w.Pump(); err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Pump: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Focused() = %v, want %v", w.Focused(), want)
}

// pumpFor keeps the pump running for a bounded window without asserting on
// delivered events.
func pumpFor(t *testing.T, w *Window, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := w.Pump(); err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Pump: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGrabModeFocusEdgesIgnored drives REAL keyboard-grab focus edges from a
// second connection on a private Xvfb. The second connection also observes
// the events, proving the server emitted the grab edges the pump must filter
// (rather than the edges never arriving).
func TestGrabModeFocusEdgesIgnored(t *testing.T) {
	startXvfb(t)
	w := openInputWindow(t)
	c := collectInput(t)
	if !testFocusGrabOpen() {
		t.Skip("second display connection unavailable")
	}
	t.Cleanup(testFocusGrabClose)
	waitFocused(t, w, true)
	c.clearAndSettle(t, w)

	if !testFocusGrabWatch(w.XID()) {
		t.Fatal("watch focus on second connection")
	}
	if status := testFocusGrabKeyboard(); status != 0 { // GrabSuccess
		t.Skipf("keyboard grab unavailable on test display: status %d", status)
	}
	if !testFocusGrabWaitFocus(w.XID(), false, focusModeGrab, 2000) {
		t.Fatal("server did not emit FocusOut NotifyGrab for the owned window")
	}
	// The grab edge must not drop real keyboard focus or deliver an event.
	pumpQuiet(t, w, c, 200*time.Millisecond)
	if !w.Focused() {
		t.Fatal("FocusOut NotifyGrab dropped real keyboard focus")
	}

	testFocusUngrabKeyboard()
	if !testFocusGrabWaitFocus(w.XID(), true, focusModeUngrab, 2000) {
		t.Fatal("server did not emit FocusIn NotifyUngrab for the owned window")
	}
	// Focus really reverted to the owned window, so the corroborated
	// NotifyUngrab edge keeps (re-asserts) focus.
	pumpFor(t, w, 200*time.Millisecond)
	c.clear()
	if !w.Focused() {
		t.Fatal("corroborated FocusIn NotifyUngrab lost real keyboard focus")
	}

	// Ordinary NotifyNormal focus transitions must still work afterwards.
	if !testFocusGrabSetFocus(testFocusGrabWindow()) {
		t.Fatal("set focus to grab window")
	}
	waitFocused(t, w, false)
	if !testFocusGrabSetFocus(w.XID()) {
		t.Fatal("set focus back to owned window")
	}
	waitFocused(t, w, true)
}

// TestUncorroboratedGrabFocusEdgesIgnored forges focus edges in every grab
// mode while the server's real focus is elsewhere (and back), pinning the
// corroboration rule. Requires the development synthetic opt-in; skipped in
// official_release builds where forged input is compiled out.
func TestUncorroboratedGrabFocusEdgesIgnored(t *testing.T) {
	startXvfb(t)
	w := openInputWindow(t)
	c := collectInput(t)
	if !setSyntheticTestEvents(true) {
		t.Skip("synthetic test opt-in is compiled out (official_release)")
	}
	t.Cleanup(func() { setSyntheticTestEvents(false) })
	if !testFocusGrabOpen() {
		t.Skip("second display connection unavailable")
	}
	t.Cleanup(testFocusGrabClose)
	waitFocused(t, w, true)
	c.clearAndSettle(t, w)

	// Move real focus away; no grab-mode FocusIn may claim it back.
	if !testFocusGrabSetFocus(testFocusGrabWindow()) {
		t.Fatal("set focus to grab window")
	}
	waitFocused(t, w, false)
	c.clear()
	for _, mode := range []int{focusModeUngrab, focusModeGrab, focusModeWhileGrabbed} {
		if !testFocusGrabSendFocus(w.XID(), true, mode, focusDetailNonlinear) {
			t.Fatalf("send FocusIn mode %d", mode)
		}
		pumpQuiet(t, w, c, 150*time.Millisecond)
		if w.Focused() {
			t.Fatalf("uncorroborated FocusIn mode %d claimed focus", mode)
		}
	}

	// Real focus returns; a corroborated NotifyUngrab FocusIn now applies.
	if !testFocusGrabSetFocus(w.XID()) {
		t.Fatal("set focus back to owned window")
	}
	waitFocused(t, w, true)
	c.clear()
	if !testFocusGrabSendFocus(w.XID(), true, focusModeUngrab, focusDetailNonlinear) {
		t.Fatal("send corroborated FocusIn NotifyUngrab")
	}
	ev := c.next(t, w)
	if ev.Kind != InputFocus || !ev.FocusGained {
		t.Fatalf("corroborated ungrab edge = %+v, want focus gained", ev)
	}
	if !w.Focused() {
		t.Fatal("corroborated FocusIn NotifyUngrab lost focus")
	}

	// While really focused, no grab-mode FocusOut may drop focus. This
	// includes NotifyUngrab: focus is corroborated on the owned window.
	for _, mode := range []int{focusModeGrab, focusModeWhileGrabbed, focusModeUngrab} {
		if !testFocusGrabSendFocus(w.XID(), false, mode, focusDetailNonlinear) {
			t.Fatalf("send FocusOut mode %d", mode)
		}
		pumpQuiet(t, w, c, 150*time.Millisecond)
		if !w.Focused() {
			t.Fatalf("FocusOut mode %d dropped real focus", mode)
		}
	}

	// Unfocused again: a NotifyUngrab FocusOut is corroborated (focus is
	// elsewhere) and applies as a real loss edge.
	if !testFocusGrabSetFocus(testFocusGrabWindow()) {
		t.Fatal("set focus to grab window")
	}
	waitFocused(t, w, false)
	c.clear()
	if !testFocusGrabSendFocus(w.XID(), false, focusModeUngrab, focusDetailNonlinear) {
		t.Fatal("send unfocused FocusOut NotifyUngrab")
	}
	ev = c.next(t, w)
	if ev.Kind != InputFocus || ev.FocusGained {
		t.Fatalf("unfocused ungrab edge = %+v, want focus lost", ev)
	}
	if w.Focused() {
		t.Fatal("unfocused FocusOut NotifyUngrab claimed focus")
	}
}

// TestBoundedDismissUnderWedgedEventMutex pins the emergency secondary-Display
// unmap: with tipsy_event_mu wedged, Dismiss must return within the bounded
// teardown window and still force the server to drop the active pointer grab
// by unmapping the grab window on a fresh connection.
func TestBoundedDismissUnderWedgedEventMutex(t *testing.T) {
	startXvfb(t)
	other := openInputWindow(t)
	requireProbeConnection(t)
	drainPump(other, t)
	deadline := time.Now().Add(2 * time.Second)
	for !x11probe.Viewable(other.XID()) && time.Now().Before(deadline) {
		drainPump(other, t)
		time.Sleep(25 * time.Millisecond)
	}
	if !x11probe.Viewable(other.XID()) {
		t.Skip("second window not viewable on test display")
	}

	// Open w last: SetPointerLock acts on the most recently opened window,
	// and Open takes focus and pumps w's FocusIn itself. Only w is pumped
	// from here: the C keyboard-focus flag is process-wide and draining the
	// second window's FocusOut would clear it.
	w := openInputWindow(t)
	pumpFor(t, w, 100*time.Millisecond)
	waitFocused(t, w, true)
	var locked bool
	var lockErr error
	for attempt := 0; attempt < 40; attempt++ {
		locked, lockErr = SetPointerLock(true)
		if lockErr == nil && locked {
			break
		}
		drainPump(w, t)
		time.Sleep(25 * time.Millisecond)
	}
	if lockErr != nil || !locked {
		t.Skipf("pointer lock unavailable on test display: %v", lockErr)
	}
	if err := x11probe.GrabPointer(other.XID()); err == nil {
		x11probe.UngrabPointer()
		t.Fatal("competing grab succeeded while w was grabbed")
	}

	// Wedge the event mutex longer than the teardown bound.
	if !testTeardownHoldEventMu(1500) {
		t.Fatal("hold event mutex")
	}
	start := time.Now()
	if err := w.Dismiss(); err != nil {
		t.Fatalf("Dismiss under wedged event mutex: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond {
		t.Fatalf("Dismiss returned in %s; expected the bounded timedlock wait", elapsed)
	}
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("Dismiss blocked %s on the wedged event mutex", elapsed)
	}
	if x11probe.Viewable(w.XID()) {
		t.Fatal("emergency dismiss left the grab window viewable")
	}
	// Unmapping the grab window must have terminated the active grab.
	if err := x11probe.GrabPointer(other.XID()); err != nil {
		t.Fatalf("pointer grab survived emergency dismiss: %v", err)
	}
	x11probe.UngrabPointer()
}

// TestBoundedPumpStopUnderWedgedEventMutex pins the timedjoin abandon path: a
// pump thread blocked on the wedged event mutex must not block shutdown, the
// abandoned thread must record its Display so close cannot free it underneath,
// and it must clean both up once the wedge clears.
func TestBoundedPumpStopUnderWedgedEventMutex(t *testing.T) {
	startXvfb(t)
	w := openInputWindow(t)
	dpy := w.Display()
	drainPump(w, t)

	// Wedge the mutex first so the new pump thread blocks on its first
	// tipsy_wait_x11 event-lock acquisition.
	if !testTeardownHoldEventMu(1500) {
		t.Fatal("hold event mutex")
	}
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the pump reach the event lock
	start := time.Now()
	if err := w.StopBackgroundPump(); err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("StopBackgroundPump: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond {
		t.Fatalf("StopBackgroundPump returned in %s; expected the bounded timedjoin wait", elapsed)
	}
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("StopBackgroundPump blocked %s on the wedged pump thread", elapsed)
	}
	if !testDpyHasAbandonedPump(dpy) {
		t.Fatal("abandoned pump did not record its Display")
	}
	// After the hold expires the abandoned pump thread must exit, free itself,
	// and relinquish the Display record; the window then closes normally.
	deadline := time.Now().Add(2 * time.Second)
	for testDpyHasAbandonedPump(dpy) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if testDpyHasAbandonedPump(dpy) {
		t.Fatal("abandoned pump thread did not release its Display record")
	}
	testResetCloseDisplayCalls()
	if err := w.Close(); err != nil {
		t.Fatalf("Close after abandoned pump: %v", err)
	}
	if got := testCloseDisplayCalls(); got != 1 {
		t.Fatalf("close after pump exit closed the Display %d times, want 1", got)
	}
}

// TestAbandonedPumpDisplayCloseSkipped pins the close guard directly: while a
// Display still has an abandoned pump, Close must release the grab window on a
// fresh connection and leave XCloseDisplay alone (the pump thread or process
// exit owns the connection). Recording the Display stands in for the wedged
// thread that cannot be kept alive deterministically past the join deadline.
func TestAbandonedPumpDisplayCloseSkipped(t *testing.T) {
	startXvfb(t)
	w := openInputWindow(t)
	dpy := w.Display()
	testRecordAbandonedDpy(dpy)
	t.Cleanup(func() { testClearAbandonedDpy(dpy) })
	if !testDpyHasAbandonedPump(dpy) {
		t.Fatal("test seam did not record the Display")
	}
	testResetCloseDisplayCalls()
	if err := w.Close(); err != nil {
		t.Fatalf("Close with abandoned pump: %v", err)
	}
	if got := testCloseDisplayCalls(); got != 0 {
		t.Fatalf("Close called XCloseDisplay %d times on a Display with an abandoned pump", got)
	}
}
