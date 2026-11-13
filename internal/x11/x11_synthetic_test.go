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

// These tests pin the default synthetic-event policy on a private bare Xvfb
// server: forged XSendEvent input, focus, and state events are dropped, while
// the ICCCM WM protocol channel (WM_DELETE_WINDOW, a properly framed
// WM_TAKE_FOCUS) and server-verified ConfigureNotify geometry still work.
// The development opt-in stays OFF here; official_release builds compile it
// out entirely and run the same assertions.

func requireSyntheticOptInOff(t *testing.T) {
	t.Helper()
	if setSyntheticTestEvents(false) {
		t.Fatal("synthetic test opt-in must be off for the default-policy tests")
	}
	if syntheticTestEventsEnabled() {
		t.Fatal("synthetic test opt-in reported enabled while off")
	}
}

// pumpQuiet pumps for a bounded window and fails on any delivered input.
func pumpQuiet(t *testing.T, w *Window, c *inputCollector, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := w.Pump(); err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Pump: %v", err)
		}
		select {
		case ev := <-c.ch:
			t.Fatalf("synthetic event was delivered with the opt-in off: %+v", ev)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSyntheticInputAndFocusDroppedByDefault(t *testing.T) {
	startXvfb(t)
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbeConnection(t)
	requireSyntheticOptInOff(t)
	drainPump(w, t)
	c.clearAndSettle(t, w)

	// Forged focus loss, keys, buttons, wheel, and motion must all die in
	// the pump before any input ring entry is created.
	if err := x11probe.Focus(w.XID(), false); err != nil {
		t.Fatalf("probe focus: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkA, true); err != nil {
		t.Fatalf("probe key down: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkA, false); err != nil {
		t.Fatalf("probe key up: %v", err)
	}
	if err := x11probe.Button(w.XID(), 10, 12, button1, true); err != nil {
		t.Fatalf("probe button down: %v", err)
	}
	if err := x11probe.Button(w.XID(), 10, 12, button1, false); err != nil {
		t.Fatalf("probe button up: %v", err)
	}
	if err := x11probe.Button(w.XID(), 10, 12, button5, true); err != nil {
		t.Fatalf("probe wheel: %v", err)
	}
	if err := x11probe.Motion(w.XID(), 20, 22, 0); err != nil {
		t.Fatalf("probe motion: %v", err)
	}
	x11probe.Sync()
	pumpQuiet(t, w, c, 300*time.Millisecond)
	if !w.Focused() {
		t.Fatal("synthetic FocusOut changed the owned window's focus state")
	}

	// The ICCCM close protocol is a ClientMessage and must still work.
	if err := x11probe.WMDelete(w.XID()); err != nil {
		t.Fatalf("WM_DELETE_WINDOW: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := w.Pump(); errors.Is(err, ErrClosed) {
			return
		} else if err != nil {
			t.Fatalf("Pump after WM_DELETE_WINDOW: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("WM_DELETE_WINDOW was dropped with the synthetic opt-in off")
}

func TestForgedWMTakeFocusDroppedAndProperOfferHonored(t *testing.T) {
	startXvfb(t)
	w := openInputWindow(t)
	other := openInputWindow(t)
	requireProbeConnection(t)
	requireSyntheticOptInOff(t)
	drainPump(w, t)
	drainPump(other, t)

	// Open takes focus immediately, so the second window owns it now.
	if got := x11probe.QueryFocus(); got != other.XID() {
		t.Fatalf("focus before forged offer = %#x, want other window %#x", got, other.XID())
	}

	// A ClientMessage carrying WM_TAKE_FOCUS outside WM_PROTOCOLS is forged
	// and must not grant focus.
	if err := x11probe.ForgedWMTakeFocus(w.XID()); err != nil {
		t.Fatalf("forged take-focus: %v", err)
	}
	x11probe.Sync()
	drainPump(w, t)
	if got := x11probe.QueryFocus(); got == w.XID() {
		t.Fatal("forged WM_TAKE_FOCUS granted input focus")
	}

	// The properly framed ICCCM offer still claims focus.
	if err := x11probe.WMTakeFocus(w.XID()); err != nil {
		t.Fatalf("WM_TAKE_FOCUS: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		drainPump(w, t)
		if x11probe.QueryFocus() == w.XID() && w.Focused() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("proper WM_PROTOCOLS WM_TAKE_FOCUS did not grant focus")
}

func TestSyntheticConfigureNotifyGeometryVerified(t *testing.T) {
	startXvfb(t)
	const winW, winH = 64, 64
	w := openInputWindowSize(t, winW, winH)
	c := collectInput(t)
	requireProbeConnection(t)
	requireSyntheticOptInOff(t)
	drainPump(w, t)
	c.clearAndSettle(t, w)

	// A forged resize claiming geometry the server does not report must not
	// reach the input stream or the window's size copy.
	if err := x11probe.ConfigureNotify(w.XID(), 0, 0, 200, 150); err != nil {
		t.Fatalf("forged configure: %v", err)
	}
	x11probe.Sync()
	pumpQuiet(t, w, c, 200*time.Millisecond)
	if width, height := w.Size(); width != winW || height != winH {
		t.Fatalf("forged ConfigureNotify resized the window to %dx%d", width, height)
	}

	// A synthetic ConfigureNotify whose geometry matches the server's
	// current attributes is accepted. On a bare Xvfb server the parent is
	// the root, so root coordinates are the parent-relative event x/y.
	x, y, err := x11probe.WindowRootOrigin(w.XID())
	if err != nil {
		t.Fatalf("window origin: %v", err)
	}
	if err := x11probe.ConfigureNotify(w.XID(), x, y, winW, winH); err != nil {
		t.Fatalf("matching configure: %v", err)
	}
	ev := c.next(t, w)
	if ev.Kind != InputResize || ev.Width != winW || ev.Height != winH {
		t.Fatalf("verified synthetic configure = %+v, want %dx%d resize", ev, winW, winH)
	}
}

func TestSyntheticOptInRestoresTestInput(t *testing.T) {
	startXvfb(t)
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbeConnection(t)
	if !setSyntheticTestEvents(true) {
		t.Skip("synthetic test opt-in is compiled out (official_release)")
	}
	t.Cleanup(func() { setSyntheticTestEvents(false) })
	drainPump(w, t)
	c.clearAndSettle(t, w)

	if err := x11probe.Key(w.XID(), xkEscape, true); err != nil {
		t.Fatalf("probe key: %v", err)
	}
	ev := c.next(t, w)
	if ev.Kind != InputKey || !ev.KeyPressed {
		t.Fatalf("opted-in synthetic key = %+v, want key press", ev)
	}
}
