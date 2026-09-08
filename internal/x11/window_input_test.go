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
	xkF11    = 0xffc8
	xkA      = 0x61 // physical key plus committed text
	xkSlash  = 0x2f // chat-open physical key plus committed text
	button1  = 1
	button3  = 3
	button4  = 4
	button5  = 5
	button6  = 6
	button7  = 7
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

func (c *inputCollector) nextPointerEdge(t *testing.T, w *Window, action int32, button int32) InputEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ev := c.next(t, w)
		if ev.Kind == InputPointer && ev.PointerAction == action && ev.Button == button {
			return ev
		}
		// A real focus/raise and pointer warp can precede the requested edge.
		// Those events are intentionally outside this assertion.
		if ev.Kind != InputFocus && ev.Kind != InputResize &&
			!(ev.Kind == InputPointer && ev.PointerAction == PointerMove && !ev.Relative) {
			t.Fatalf("unexpected event before pointer edge: %+v", ev)
		}
	}
	t.Fatal("timed out waiting for pointer edge")
	return InputEvent{}
}

func openInputWindow(t *testing.T) *Window {
	return openInputWindowSize(t, 64, 64)
}

func openInputWindowSize(t *testing.T, width, height int) *Window {
	t.Helper()
	ensureDisplay(t)
	w, err := Open("Tipsy input test", width, height)
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

func requirePointerGrabAvailable(t *testing.T, w *Window) {
	t.Helper()
	if err := x11probe.GrabPointer(w.XID()); err != nil {
		t.Skipf("desktop has an active pointer grab: %v", err)
	}
	x11probe.UngrabPointer()
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

func TestSlashDeliveredAsPhysicalKeyAndText(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)
	dropsBefore := InputDroppedKeys()

	if err := x11probe.Key(w.XID(), xkSlash, true); err != nil {
		t.Fatalf("probe slash: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkSlash, false); err != nil {
		t.Fatalf("probe slash release: %v", err)
	}
	drainPump(w, t)

	ev := c.next(t, w)
	if ev.Kind != InputKey || !ev.KeyPressed || ev.KeyCode != 76 || ev.ScanCode <= 8 {
		t.Fatalf("event = %+v, want physical slash (Android 76, raw X11 code > 8)", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputText || ev.Text != "/" {
		t.Fatalf("event = kind=%d text=%q, want committed slash", ev.Kind, ev.Text)
	}
	ev = c.next(t, w)
	if ev.Kind != InputKey || ev.KeyPressed || ev.KeyCode != 76 || ev.ScanCode <= 8 {
		t.Fatalf("event = %+v, want physical slash release", ev)
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

func TestWheelButtonsDeliverOneDetentPerPress(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)

	for _, step := range []struct {
		button      uint64
		wantX       float32
		wantY       float32
		wantScrollX float32
		wantScrollY float32
	}{
		{button: button4, wantX: 11, wantY: 12, wantScrollY: 1},
		{button: button5, wantX: 13, wantY: 14, wantScrollY: -1},
		{button: button6, wantX: 15, wantY: 16, wantScrollX: -1},
		{button: button7, wantX: 17, wantY: 18, wantScrollX: 1},
	} {
		if err := x11probe.Button(w.XID(), int(step.wantX), int(step.wantY), step.button, true); err != nil {
			t.Fatalf("wheel button %d press: %v", step.button, err)
		}
		if err := x11probe.Button(w.XID(), int(step.wantX), int(step.wantY), step.button, false); err != nil {
			t.Fatalf("wheel button %d release: %v", step.button, err)
		}
		drainPump(w, t)
		ev := c.next(t, w)
		if ev.Kind != InputScroll || ev.X != step.wantX || ev.Y != step.wantY ||
			ev.ScrollX != step.wantScrollX || ev.ScrollY != step.wantScrollY {
			t.Fatalf("wheel button %d event = %+v", step.button, ev)
		}
		select {
		case extra := <-c.ch:
			t.Fatalf("wheel button %d release created an extra detent: %+v", step.button, extra)
		case <-time.After(40 * time.Millisecond):
		}
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

func TestBackgroundPumpResizeWakesAndUpdatesSize(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	t.Cleanup(func() { _ = w.StopBackgroundPump() })
	settleBackgroundInput(t, w, c)

	if err := x11probe.Resize(w.XID(), 96, 80); err != nil {
		t.Fatalf("probe resize: %v", err)
	}
	if err := x11probe.Motion(w.XID(), 70, 60, 0); err != nil {
		t.Fatalf("probe motion: %v", err)
	}
	select {
	case <-w.InputReady():
	case <-time.After(2 * time.Second):
		t.Fatal("InputReady did not wake on resize")
	}
	if err := w.Pump(); err != nil && err != ErrClosed {
		t.Fatalf("Pump: %v", err)
	}
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

func waitPointerPosition(t *testing.T, w *Window, wantX, wantY int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = w.Pump()
		x, y, err := x11probe.PointerPosition(w.XID())
		if err == nil && x == wantX && y == wantY {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	x, y, err := x11probe.PointerPosition(w.XID())
	t.Fatalf("pointer position = (%d,%d), err=%v; want (%d,%d)", x, y, err, wantX, wantY)
}

func TestPointerLockKeepsPressAnchorAndDeliversRelativeMotion(t *testing.T) {
	w := openInputWindowSize(t, 320, 180)
	c := collectInput(t)
	requireProbe(t)
	requirePointerGrabAvailable(t, w)
	c.clearAndSettle(t, w)

	const anchorX, anchorY = 20, 22
	c.clearAndSettle(t, w)
	if err := x11probe.Button(w.XID(), anchorX, anchorY, button3, true); err != nil {
		t.Fatalf("secondary down: %v", err)
	}
	ev := c.nextPointerEdge(t, w, PointerDown, 3)
	if ev.Kind != InputPointer || ev.PointerAction != PointerDown || ev.Button != 3 || ev.X != anchorX || ev.Y != anchorY {
		t.Fatalf("secondary down = %+v, want anchor (%d,%d)", ev, anchorX, anchorY)
	}
	changed, err := SetPointerLock(true)
	if err != nil || !changed {
		t.Fatalf("SetPointerLock(true) = changed %t, err %v", changed, err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointerCapture || !ev.Captured || ev.X != anchorX || ev.Y != anchorY {
		t.Fatalf("capture event = %+v, want acquired at anchor", ev)
	}
	if captured, x, y := w.PointerCapture(); !captured || x != anchorX || y != anchorY {
		t.Fatalf("PointerCapture = %t (%d,%d), want true (%d,%d)", captured, x, y, anchorX, anchorY)
	}

	// Repeated movement can reach an edge on every cycle, yet each cycle is
	// recentered and reports a fresh displacement from the same anchor.
	for i := 0; i < 3; i++ {
		if err := x11probe.WarpPointer(w.XID(), 319, 179); err != nil {
			t.Fatalf("captured motion %d: %v", i, err)
		}
		ev = c.next(t, w)
		if ev.Kind != InputPointer || ev.PointerAction != PointerMove || !ev.Relative ||
			ev.X != anchorX || ev.Y != anchorY || ev.DeltaX != 299 || ev.DeltaY != 157 {
			t.Fatalf("captured motion %d = %+v, want anchored relative (299,157)", i, ev)
		}
		waitPointerPosition(t, w, anchorX, anchorY)
	}

	// A resize smaller than the original press position clamps the stable
	// anchor and recenters before the next input edge.
	if err := x11probe.Resize(w.XID(), 16, 12); err != nil {
		t.Fatalf("resize while captured: %v", err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputResize || ev.Width != 16 || ev.Height != 12 {
		t.Fatalf("resize event = %+v, want 16x12", ev)
	}
	const releaseX, releaseY = 15, 11
	if captured, x, y := w.PointerCapture(); !captured || x != releaseX || y != releaseY {
		t.Fatalf("PointerCapture after resize = %t (%d,%d), want true (%d,%d)", captured, x, y, releaseX, releaseY)
	}
	waitPointerPosition(t, w, releaseX, releaseY)

	if err := x11probe.Button(w.XID(), releaseX, releaseY, button3, false); err != nil {
		t.Fatalf("secondary up: %v", err)
	}
	ev = c.nextPointerEdge(t, w, PointerUp, 3)
	if ev.Kind != InputPointer || ev.PointerAction != PointerUp || ev.Button != 3 || ev.X != releaseX || ev.Y != releaseY {
		t.Fatalf("secondary up = %+v, want release at clamped anchor", ev)
	}
	changed, err = SetPointerLock(false)
	if err != nil || !changed {
		t.Fatalf("SetPointerLock(false) = changed %t, err %v", changed, err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointerCapture || ev.Captured || ev.CaptureFailed {
		t.Fatalf("capture release = %+v", ev)
	}
	waitPointerPosition(t, w, releaseX, releaseY)

	if err := x11probe.WarpPointer(w.XID(), releaseX-5, releaseY-3); err != nil {
		t.Fatalf("post-release motion: %v", err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || ev.Relative ||
		ev.X != releaseX-5 || ev.Y != releaseY-3 {
		t.Fatalf("post-release motion = %+v, want normal absolute motion", ev)
	}

	// Returning to the release anchor is a second *physical* post-release
	// movement, not the stale recenter generated while capture was active. A
	// stale suppression token here used to discard this event, making the
	// desktop pointer and Roblox's absolute-mouse state disagree until a later
	// movement appeared to jump.
	if err := x11probe.WarpPointer(w.XID(), releaseX, releaseY); err != nil {
		t.Fatalf("post-release return to anchor: %v", err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || ev.Relative ||
		ev.X != releaseX || ev.Y != releaseY {
		t.Fatalf("second post-release motion = %+v, want normal anchor motion", ev)
	}
}

func TestPointerLockPreservesEachQueuedRelativeSample(t *testing.T) {
	w := openInputWindowSize(t, 320, 180)
	c := collectInput(t)
	requireProbe(t)
	requirePointerGrabAvailable(t, w)
	c.clearAndSettle(t, w)

	const anchorX, anchorY = 80, 60
	if err := x11probe.Button(w.XID(), anchorX, anchorY, button3, true); err != nil {
		t.Fatalf("secondary down: %v", err)
	}
	_ = c.nextPointerEdge(t, w, PointerDown, 3)
	if changed, err := SetPointerLock(true); err != nil || !changed {
		t.Fatalf("SetPointerLock(true) = changed %t, err %v", changed, err)
	}
	ev := c.next(t, w)
	if ev.Kind != InputPointerCapture || !ev.Captured {
		t.Fatalf("capture event = %+v", ev)
	}

	// Queue three genuine relative source samples before Tipsy's X reader runs.
	// The old core path collapsed this burst to one final-coordinate delta;
	// XI2's float-valuator path must retain three ordered camera moves.
	for i := 0; i < 3; i++ {
		if err := x11probe.RelativeMotion(1, 0); err != nil {
			t.Fatalf("relative motion %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		ev = c.next(t, w)
		if ev.Kind != InputPointer || ev.PointerAction != PointerMove || !ev.Relative ||
			ev.X != anchorX || ev.Y != anchorY || ev.DeltaX == 0 || ev.DeltaY != 0 {
			t.Fatalf("relative sample %d = %+v, want separate horizontal captured move", i, ev)
		}
	}
	select {
	case extra := <-c.ch:
		if extra.Kind == InputPointer && extra.Relative {
			t.Fatalf("unexpected fourth captured sample: %+v", extra)
		}
	default:
	}
	waitPointerPosition(t, w, anchorX, anchorY)
}

func TestPointerLockFallsBackToCoreMotionWhenRawStreamIsQuiet(t *testing.T) {
	w := openInputWindowSize(t, 320, 180)
	c := collectInput(t)
	requireProbe(t)
	requirePointerGrabAvailable(t, w)
	c.clearAndSettle(t, w)

	const anchorX, anchorY = 20, 22
	if err := x11probe.Button(w.XID(), anchorX, anchorY, button3, true); err != nil {
		t.Fatalf("secondary down: %v", err)
	}
	_ = c.nextPointerEdge(t, w, PointerDown, 3)
	if changed, err := SetPointerLock(true); err != nil || !changed {
		t.Fatalf("SetPointerLock(true) = changed %t, err %v", changed, err)
	}
	ev := c.next(t, w)
	if ev.Kind != InputPointerCapture || !ev.Captured {
		t.Fatalf("capture event = %+v", ev)
	}

	// XSendEvent creates only the core stream. RawMotion remains selected for
	// a real hardware source, but a server that accepts that selection and then
	// supplies no raw master events must still move the camera through the
	// proven relative/recenter fallback.
	if err := x11probe.Motion(w.XID(), 80, anchorY, 0); err != nil {
		t.Fatalf("quiet-raw core motion: %v", err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || !ev.Relative ||
		ev.X != anchorX || ev.Y != anchorY || ev.DeltaX != 60 || ev.DeltaY != 0 {
		t.Fatalf("quiet-raw fallback event = %+v, want anchored relative (60,0)", ev)
	}
	waitPointerPosition(t, w, anchorX, anchorY)
}

func TestPointerLockQueuedRecenterDoesNotCancelDelta(t *testing.T) {
	w := openInputWindowSize(t, 320, 180)
	c := collectInput(t)
	requireProbe(t)
	requirePointerGrabAvailable(t, w)
	c.clearAndSettle(t, w)

	const anchorX, anchorY = 20, 22
	if err := x11probe.Button(w.XID(), anchorX, anchorY, button3, true); err != nil {
		t.Fatalf("secondary down: %v", err)
	}
	_ = c.nextPointerEdge(t, w, PointerDown, 3)
	if changed, err := SetPointerLock(true); err != nil || !changed {
		t.Fatalf("SetPointerLock(true) = changed %t, err %v", changed, err)
	}
	ev := c.next(t, w)
	if ev.Kind != InputPointerCapture || !ev.Captured {
		t.Fatalf("capture event = %+v", ev)
	}

	// Physical look and a queued recenter must not coalesce to a zero delta.
	// Production used the warp's anchor coordinate as the latest sample, so
	// nativePassMouseMove never saw camera travel.
	if err := x11probe.WarpPointer(w.XID(), 319, 179); err != nil {
		t.Fatalf("queued physical look: %v", err)
	}
	if err := x11probe.WarpPointer(w.XID(), anchorX, anchorY); err != nil {
		t.Fatalf("queued recenter: %v", err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerMove || !ev.Relative ||
		ev.X != anchorX || ev.Y != anchorY || ev.DeltaX != 299 || ev.DeltaY != 157 {
		t.Fatalf("queued-recenter captured motion = %+v, want relative (299,157)", ev)
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = w.Pump()
		select {
		case snap := <-c.ch:
			if snap.Kind == InputPointer && snap.Relative && snap.DeltaX == -299 && snap.DeltaY == -157 {
				t.Fatal("recenter snap-back was delivered as camera motion")
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestPointerLockFocusLossReleasesOnce(t *testing.T) {
	w := openInputWindowSize(t, 320, 180)
	c := collectInput(t)
	requireProbe(t)
	requirePointerGrabAvailable(t, w)
	c.clearAndSettle(t, w)

	c.clearAndSettle(t, w)
	if err := x11probe.Button(w.XID(), 12, 14, button3, true); err != nil {
		t.Fatal(err)
	}
	_ = c.nextPointerEdge(t, w, PointerDown, 3)
	if changed, err := SetPointerLock(true); err != nil || !changed {
		t.Fatalf("capture: changed=%t err=%v", changed, err)
	}
	_ = c.next(t, w)
	if err := x11probe.Focus(w.XID(), false); err != nil {
		t.Fatal(err)
	}

	ev := c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerUp || ev.Button != 3 {
		t.Fatalf("focus cancellation first event = %+v, want one secondary up", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointerCapture || ev.Captured || ev.CaptureFailed {
		t.Fatalf("focus cancellation capture event = %+v", ev)
	}
	ev = c.next(t, w)
	if ev.Kind != InputFocus || ev.FocusGained {
		t.Fatalf("focus cancellation focus event = %+v", ev)
	}
	if captured, _, _ := w.PointerCapture(); captured {
		t.Fatal("pointer remained captured after FocusOut")
	}
	if err := x11probe.GrabPointer(w.XID()); err != nil {
		t.Fatalf("probe could not grab after FocusOut release: %v", err)
	}
	x11probe.UngrabPointer()

	// A late physical release after cancellation is stale and must not create
	// a second engine-visible button-up edge.
	if err := x11probe.Button(w.XID(), 12, 14, button3, false); err != nil {
		t.Fatal(err)
	}
	_ = w.Pump()
	select {
	case extra := <-c.ch:
		t.Fatalf("stale release delivered after focus cancellation: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPointerLockGrabFailureKeepsButtonEdges(t *testing.T) {
	w := openInputWindow(t)
	c := collectInput(t)
	requireProbe(t)
	c.clearAndSettle(t, w)

	ownedGrab := x11probe.GrabPointer(w.XID()) == nil
	if ownedGrab {
		defer x11probe.UngrabPointer()
	} else {
		t.Log("using ambient desktop pointer grab as competing client")
	}
	if err := x11probe.Button(w.XID(), 18, 19, button3, true); err != nil {
		t.Fatal(err)
	}
	ev := c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerDown {
		t.Fatalf("down before failed grab = %+v", ev)
	}
	if changed, err := SetPointerLock(true); changed || err == nil {
		t.Fatalf("rejected SetPointerLock = changed %t, err %v", changed, err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointerCapture || !ev.CaptureFailed {
		t.Fatalf("grab failure event = %+v", ev)
	}
	if err := x11probe.Button(w.XID(), 18, 19, button3, false); err != nil {
		t.Fatal(err)
	}
	ev = c.next(t, w)
	if ev.Kind != InputPointer || ev.PointerAction != PointerUp || ev.Button != 3 {
		t.Fatalf("up after failed grab = %+v", ev)
	}
}
