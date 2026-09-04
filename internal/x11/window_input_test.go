// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11/x11probe"
)

// Keysym constants (X11 keysymdef values).
const (
	xkEscape = 0xff1b
	xkLeft   = 0xff51
	xkA      = 0x61 // physical key plus committed text
	button1  = 1
	button3  = 3
	maskBtn1 = 0x100
)

type inputCollector struct {
	ch   chan InputEvent
	stop func()
}

func collectInput(t *testing.T) *inputCollector {
	t.Helper()
	c := &inputCollector{ch: make(chan InputEvent, 64)}
	c.stop = OnInput(func(ev InputEvent) {
		select {
		case c.ch <- ev:
		default:
		}
	})
	t.Cleanup(c.stop)
	return c
}

// clear discards queued events (e.g. the focus gained by Open's own
// XSetInputFocus) so assertions see only probe-driven events.
func (c *inputCollector) clear() {
	for {
		select {
		case <-c.ch:
		default:
			return
		}
	}
}

// clearAndSettle pumps a few rounds and discards everything queued so
// far (events still in flight from prior windows/probes).
func (c *inputCollector) clearAndSettle(t *testing.T, w *Window) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = w.Pump()
		time.Sleep(10 * time.Millisecond)
	}
	c.clear()
}

func (c *inputCollector) next(t *testing.T, w *Window) InputEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-c.ch:
			return ev
		default:
		}
		// Synthetic events cross the server asynchronously; keep the
		// pump running (as the runtime ticker does) until one lands.
		_ = w.Pump()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for input event")
	return InputEvent{}
}

func openInputWindow(t *testing.T) *Window {
	t.Helper()
	ensureDisplay(t)
	w, err := Open("Tipsy input test", 64, 64)
	if err != nil {
		if err == ErrUnavailable || err == ErrNoDisplay {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func drainPump(w *Window, t *testing.T) {
	t.Helper()
	if err := w.Pump(); err != nil && err != ErrClosed {
		t.Fatalf("Pump: %v", err)
	}
}

func requireProbe(t *testing.T) {
	t.Helper()
	if err := x11probe.Open(); err != nil {
		t.Skipf("probe display unavailable: %v", err)
	}
	t.Cleanup(x11probe.Close)
}

func TestFocusEventsDelivered(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)

	// Drain any focus state from Open's own XSetInputFocus, settle
	// in-flight events, then start from a clean slate.
	drainPump(w, t)
	c.clearAndSettle(t, w)

	if err := x11probe.Focus(w.XID(), true); err != nil {
		t.Fatalf("probe focus: %v", err)
	}
	drainPump(w, t)
	ev := c.next(t, w)
	if ev.Kind != InputFocus || !ev.FocusGained {
		t.Fatalf("event = %+v, want focus gained", ev)
	}
	if !w.Focused() {
		t.Fatal("Focused() = false after FocusIn")
	}

	if err := x11probe.Focus(w.XID(), false); err != nil {
		t.Fatalf("probe focus out: %v", err)
	}
	drainPump(w, t)
	ev = c.next(t, w)
	if ev.Kind != InputFocus || ev.FocusGained {
		t.Fatalf("event = %+v, want focus lost", ev)
	}
	if w.Focused() {
		t.Fatal("Focused() = true after FocusOut")
	}
}

func TestNonTextKeyEvents(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)

	if err := x11probe.Key(w.XID(), xkEscape, true); err != nil {
		t.Fatalf("probe key: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkEscape, false); err != nil {
		t.Fatalf("probe key release: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkLeft, true); err != nil {
		t.Fatalf("probe key left: %v", err)
	}
	drainPump(w, t)

	ev := c.next(t, w)
	if ev.Kind != InputKey || !ev.KeyPressed || ev.KeyCode != 111 {
		t.Fatalf("event = %+v, want Escape press keycode 111", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputKey || ev.KeyPressed || ev.KeyCode != 111 {
		t.Fatalf("event = %+v, want Escape release keycode 111", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputKey || !ev.KeyPressed || ev.KeyCode != 21 {
		t.Fatalf("event = %+v, want Left press keycode 21", ev)
	}
}

func TestPrintableKeyDeliveredAsPhysicalKey(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)
	dropsBefore := InputDroppedKeys()

	if err := x11probe.Key(w.XID(), xkA, true); err != nil {
		t.Fatalf("probe key a: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkA, false); err != nil {
		t.Fatalf("probe key a release: %v", err)
	}
	drainPump(w, t)

	// X11's letter A preserves the real physical edge for Roblox's direct
	// key path, then separately reports the input method's committed UTF-8.
	// This separation keeps layout/compose handling out of keycode mapping.
	ev := c.next(t, w)
	if ev.Kind != InputKey || !ev.KeyPressed || ev.KeyCode != 29 || ev.ScanCode <= 8 {
		t.Fatalf("event = %+v, want physical A (Android 29, raw X11 code > 8)", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputText || ev.Text != "a" {
		t.Fatalf("event = kind=%d text=%q, want committed text %q", ev.Kind, ev.Text, "a")
	}
	ev = c.next(t, w)
	if ev.Kind != InputKey || ev.KeyPressed || ev.KeyCode != 29 || ev.ScanCode <= 8 {
		t.Fatalf("event = %+v, want physical A release without duplicate text", ev)
	}
	if got := InputDroppedKeys(); got != dropsBefore {
		t.Fatalf("InputDroppedKeys = %d, want unchanged %d", got, dropsBefore)
	}
}

func TestPointerButtonAndContinuousMotion(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)

	// PointerMotionMask must deliver ordinary unpressed motion. The direct
	// Roblox mouse listener consumes this stream to keep its cursor position
	// current before any button is pressed.
	if err := x11probe.Motion(w.XID(), 8, 9, 0); err != nil {
		t.Fatalf("probe hover: %v", err)
	}
	drainPump(w, t)
	ev := c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || ev.X != 8 || ev.Y != 9 {
		t.Fatalf("event = %+v, want unpressed move to (8,9)", ev)
	}

	if err := x11probe.Button(w.XID(), 10, 20, button1, true); err != nil {
		t.Fatalf("probe button: %v", err)
	}
	if err := x11probe.Motion(w.XID(), 30, 40, maskBtn1); err != nil {
		t.Fatalf("probe motion: %v", err)
	}
	if err := x11probe.Button(w.XID(), 30, 40, button1, false); err != nil {
		t.Fatalf("probe release: %v", err)
	}
	drainPump(w, t)

	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerDown || ev.Button != 1 || ev.X != 10 || ev.Y != 20 {
		t.Fatalf("event = %+v, want down at (10,20)", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || ev.X != 30 || ev.Y != 40 {
		t.Fatalf("event = %+v, want move to (30,40)", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerUp || ev.Button != 1 || ev.X != 30 || ev.Y != 40 {
		t.Fatalf("event = %+v, want up at (30,40)", ev)
	}
	// Only the intentional unpressed move, button edge, drag, and release
	// arrive; the pointer stream does not fabricate extra events.
	select {
	case ev := <-c.ch:
		t.Fatalf("unexpected extra event: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPointerMotionCoalescesToLatestPosition(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)

	// A slow engine consumer may have several raw moves pending. The X11
	// ring retains the newest real position rather than adding stale cursor
	// lag or growing without bound.
	if err := x11probe.Motion(w.XID(), 4, 5, 0); err != nil {
		t.Fatalf("first probe motion: %v", err)
	}
	if err := x11probe.Motion(w.XID(), 17, 23, 0); err != nil {
		t.Fatalf("second probe motion: %v", err)
	}
	drainPump(w, t)
	ev := c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || ev.X != 17 || ev.Y != 23 {
		t.Fatalf("coalesced move = %+v, want latest position (17,23)", ev)
	}
	select {
	case ev := <-c.ch:
		t.Fatalf("stale coalesced move delivered: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestResizePrecedesFollowingPointerDelivery(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)

	if err := x11probe.Resize(w.XID(), 96, 80); err != nil {
		t.Fatalf("probe resize: %v", err)
	}
	if err := x11probe.Motion(w.XID(), 70, 60, 0); err != nil {
		t.Fatalf("probe motion: %v", err)
	}
	drainPump(w, t)

	ev := c.next(t, w)
	if ev.Kind != InputResize || ev.Width != 96 || ev.Height != 80 {
		t.Fatalf("first event = %+v, want resize 96x80", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || ev.X != 70 || ev.Y != 60 {
		t.Fatalf("second event = %+v, want pointer move after resize", ev)
	}
	if width, height := w.Size(); width != 96 || height != 80 {
		t.Fatalf("Size() = %dx%d, want resize dimensions", width, height)
	}
}

func TestNotifyInputDeliversResizeToEverySubscriberBeforePointer(t *testing.T) {
	var order []string
	stopA := OnInput(func(ev InputEvent) {
		switch ev.Kind {
		case InputResize:
			order = append(order, "A resize")
		case InputPointer:
			order = append(order, "A pointer")
		}
	})
	defer stopA()
	stopB := OnInput(func(ev InputEvent) {
		switch ev.Kind {
		case InputResize:
			order = append(order, "B resize")
		case InputPointer:
			order = append(order, "B pointer")
		}
	})
	defer stopB()

	notifyInput([]InputEvent{
		{Kind: InputResize, Width: 1600, Height: 900},
		{Kind: InputPointer, PointerAction: PointerMove, X: 700, Y: 500},
	})
	want := []string{"A resize", "B resize", "A pointer", "B pointer"}
	if len(order) != len(want) {
		t.Fatalf("subscriber order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("subscriber order = %v, want %v", order, want)
		}
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	w := openInputWindow(t)
	requireProbe(t)
	drainPump(w, t)

	got := make(chan InputEvent, 8)
	cancel := OnInput(func(ev InputEvent) { got <- ev })
	drainPump(w, t)
	cancel()

	if err := x11probe.Focus(w.XID(), true); err != nil {
		t.Fatalf("probe focus: %v", err)
	}
	drainPump(w, t)
	select {
	case ev := <-got:
		t.Fatalf("delivered after cancel: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRightButtonDelivered(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)

	if err := x11probe.Button(w.XID(), 5, 6, button3, true); err != nil {
		t.Fatalf("probe button3: %v", err)
	}
	if err := x11probe.Button(w.XID(), 5, 6, button3, false); err != nil {
		t.Fatalf("probe button3 release: %v", err)
	}
	drainPump(w, t)

	ev := c.next(t, w)
	if ev.PointerAction != PointerDown || ev.Button != 3 {
		t.Fatalf("event = %+v, want button3 down", ev)
	}
	ev = c.next(t, w)
	if ev.PointerAction != PointerUp || ev.Button != 3 {
		t.Fatalf("event = %+v, want button3 up", ev)
	}
}
