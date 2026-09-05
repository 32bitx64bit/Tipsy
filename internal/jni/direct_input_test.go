// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"testing"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

func selectPointerPath(t *testing.T, value string) {
	t.Helper()
	t.Setenv("TIPSY_INPUT_PATH", value)
	ResetPointerInputPath()
	t.Cleanup(ResetPointerInputPath)
}

func selectKeyboardPath(t *testing.T, value string) {
	t.Helper()
	t.Setenv("TIPSY_KEY_INPUT_PATH", value)
	ResetKeyboardInputPath()
	t.Cleanup(ResetKeyboardInputPath)
}

func wireRecordingDirectTarget(t *testing.T, env, class uintptr) {
	t.Helper()
	testDirectRecReset()
	if !SetRobloxDirectInputTarget(env, class, testDirectRecordButtonFn(), testDirectRecordMoveFn(), testDirectRecordWheelFn(), testDirectRecordMouseLockedFn()) {
		t.Fatal("recording direct target did not wire")
	}
	t.Cleanup(ClearRobloxDirectInputTarget)
}

func wireRecordingDirectKeyTarget(t *testing.T, env, class uintptr) {
	t.Helper()
	testDirectRecReset()
	if !SetRobloxDirectKeyTarget(env, class, testDirectRecordKeyFn()) {
		t.Fatal("recording direct keyboard target did not wire")
	}
	t.Cleanup(ClearRobloxDirectKeyTarget)
}

func stubPointerLock(t *testing.T, fn func(bool) (bool, error)) {
	t.Helper()
	old := pointerLockSetter
	pointerLockSetter = fn
	rmbPointerFallback.Store(false)
	t.Cleanup(func() {
		pointerLockSetter = old
		rmbPointerFallback.Store(false)
	})
}

// TestDirectMouseButtonABI pins the exact public-static-native JNI ABI proven
// from classes2.dex: nativePassMouseButton(FFZI)V receives
// (JNIEnv*, jclass, x, y, pressed, getActionButton()-1).
func TestDirectMouseButtonABI(t *testing.T) {
	const env, class = uintptr(0x1234), uintptr(0x5678)
	wireRecordingDirectTarget(t, env, class)
	before := RobloxDirectInputStats()

	if !DispatchRobloxDirectPointer(motionActionDown, 631.25, 383.5, 1) {
		t.Fatal("direct primary DOWN was not delivered")
	}
	if got := testDirectRecButtonID(0); got != env {
		t.Fatalf("recorded env = %#x, want %#x", got, env)
	}
	if got := testDirectRecButtonID(1); got != class {
		t.Fatalf("recorded class = %#x, want %#x", got, class)
	}
	if x, y := testDirectRecButtonFloat(0), testDirectRecButtonFloat(1); x != 631.25 || y != 383.5 {
		t.Fatalf("DOWN coordinates = (%v,%v), want (631.25,383.5)", x, y)
	}
	if !testDirectRecButtonPressed() {
		t.Fatal("DOWN did not carry pressed=true")
	}
	if got := testDirectRecButtonIndex(); got != 0 {
		t.Fatalf("primary button index = %d, want 0", got)
	}

	if !DispatchRobloxDirectPointer(motionActionUp, 632, 384, 1) {
		t.Fatal("direct primary UP was not delivered")
	}
	if testDirectRecButtonPressed() {
		t.Fatal("UP did not carry pressed=false")
	}
	if x, y := testDirectRecButtonFloat(0), testDirectRecButtonFloat(1); x != 632 || y != 384 {
		t.Fatalf("UP coordinates = (%v,%v), want the real UP coordinates (632,384)", x, y)
	}
	if got := RobloxDirectInputStats().ButtonDelivered - before.ButtonDelivered; got != 2 {
		t.Fatalf("direct button delivery delta = %d, want 2", got)
	}

	if !DispatchRobloxDirectPointer(motionActionDown, 10, 20, 3) {
		t.Fatal("direct secondary DOWN was not delivered")
	}
	if got := testDirectRecButtonIndex(); got != 1 {
		t.Fatalf("secondary button index = %d, want 1", got)
	}
}

// TestDirectMouseMoveABI pins nativePassMouseMove(FFFF)V as
// (absolute x, absolute y, delta x, delta y). A real button coordinate seeds
// the following drag delta; the direct method receives no invented time slot.
func TestDirectMouseMoveABI(t *testing.T) {
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	if !DispatchRobloxDirectPointer(motionActionDown, 100, 200, 1) {
		t.Fatal("direct DOWN was not delivered")
	}
	before := RobloxDirectInputStats()
	if !DispatchRobloxDirectPointer(motionActionMove, 112.5, 196.25, 0) {
		t.Fatal("direct MOVE was not delivered")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 112.5 || y != 196.25 {
		t.Fatalf("absolute move = (%v,%v), want (112.5,196.25)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 12.5 || dy != -3.75 {
		t.Fatalf("move delta = (%v,%v), want (12.5,-3.75)", dx, dy)
	}
	if got := RobloxDirectInputStats().MoveDelivered - before.MoveDelivered; got != 1 {
		t.Fatalf("direct move delivery delta = %d, want 1", got)
	}
}

func TestDirectCapturedMouseMoveAccumulatesLogicalAndExplicitDelta(t *testing.T) {
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	if !DispatchRobloxDirectPointer(motionActionDown, 20, 22, 3) {
		t.Fatal("secondary DOWN was not delivered")
	}
	if !DispatchRobloxDirectPointerDelta(20, 22, 43, -28) {
		t.Fatal("captured move was not delivered")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 63 || y != -6 {
		t.Fatalf("captured absolute = (%v,%v), want accumulated logical (63,-6)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 43 || dy != -28 {
		t.Fatalf("captured delta = (%v,%v), want (43,-28)", dx, dy)
	}
	if !DispatchRobloxDirectPointerDelta(20, 22, -5, 4) {
		t.Fatal("second captured move was not delivered")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 58 || y != -2 {
		t.Fatalf("second captured absolute = (%v,%v), want (58,-2)", x, y)
	}
	if !DispatchRobloxDirectPointer(motionActionUp, 20, 22, 3) {
		t.Fatal("secondary UP was not delivered")
	}
	if !DispatchRobloxDirectPointer(motionActionMove, 25, 19, 0) {
		t.Fatal("first post-release move was not delivered")
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 5 || dy != -3 {
		t.Fatalf("first post-release delta = (%v,%v), want (5,-3)", dx, dy)
	}
	if !DispatchRobloxDirectPointer(motionActionMove, 20, 22, 0) {
		t.Fatal("second post-release move was not delivered")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 20 || y != 22 {
		t.Fatalf("second post-release absolute = (%v,%v), want release anchor (20,22)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != -5 || dy != 3 {
		t.Fatalf("second post-release delta = (%v,%v), want (-5,3)", dx, dy)
	}
}

func TestMainWindowMouseLockGetterABI(t *testing.T) {
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	before := RobloxDirectInputStats()
	if locked, available := RobloxMainWindowMouseLocked(); locked || !available {
		t.Fatalf("initial getter = locked %t, available %t; want false,true", locked, available)
	}
	testDirectRecSetMouseLocked(true)
	if locked, available := RobloxMainWindowMouseLocked(); !locked || !available {
		t.Fatalf("true getter = locked %t, available %t; want true,true", locked, available)
	}
	after := RobloxDirectInputStats()
	if got := after.LockQueries - before.LockQueries; got != 2 {
		t.Fatalf("lock query delta = %d, want 2", got)
	}
	if got := after.LockTrue - before.LockTrue; got != 1 {
		t.Fatalf("true lock read delta = %d, want 1", got)
	}
}

func TestDirectSecondaryEdgesPrecedeLockGetter(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(true)
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	if buttonSeq, lockSeq := testDirectRecButtonSequence(), testDirectRecLockSequence(); buttonSeq == 0 || lockSeq == 0 || buttonSeq >= lockSeq {
		t.Fatalf("secondary DOWN/getter order = button %d, getter %d; want button first", buttonSeq, lockSeq)
	}

	testDirectRecReset()
	testDirectRecSetMouseLocked(false)
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 20, Y: 22,
	})
	if buttonSeq, lockSeq := testDirectRecButtonSequence(), testDirectRecLockSequence(); buttonSeq == 0 || lockSeq == 0 || buttonSeq >= lockSeq {
		t.Fatalf("secondary UP/getter order = button %d, getter %d; want button first", buttonSeq, lockSeq)
	}
}

func TestGetterTrueCapturedMoveAccumulatesLogicalCoordinates(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(true)
	stubPointerLock(t, func(locked bool) (bool, error) {
		return true, nil
	})

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true,
		X: 20, Y: 22, DeltaX: 11, DeltaY: -7,
	})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 31 || y != 15 {
		t.Fatalf("getter-true logical coordinate=(%v,%v), want (31,15)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 11 || dy != -7 {
		t.Fatalf("getter-true captured delta=(%v,%v), want (11,-7)", dx, dy)
	}
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true,
		X: 20, Y: 22, DeltaX: -5, DeltaY: 4,
	})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 26 || y != 19 {
		t.Fatalf("getter-true reversed logical coordinate=(%v,%v), want (26,19)", x, y)
	}
}

func TestCapturedRelativeMotionDeliversWhenPointerActionIsDown(t *testing.T) {
	// Production used to decode captured ring a=3 as PointerDown. Relative
	// motion must still reach nativePassMouseMove rather than being dropped as
	// an unsupported button while the host grab holds the cursor.
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(false)
	stubPointerLock(t, func(locked bool) (bool, error) {
		return true, nil
	})

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	before := RobloxDirectInputStats()
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Relative: true,
		X: 20, Y: 22, DeltaX: 11, DeltaY: -7,
	})
	if got := RobloxDirectInputStats().MoveDelivered - before.MoveDelivered; got != 1 {
		t.Fatalf("mislabelled captured move delivery delta=%d, want 1", got)
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 31 || y != 15 {
		t.Fatalf("mislabelled captured logical=(%v,%v), want (31,15)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 11 || dy != -7 {
		t.Fatalf("mislabelled captured delta=(%v,%v), want (11,-7)", dx, dy)
	}
}

func TestGetterFalseSecondaryDownUsesHeldRMBCapture(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(false)
	var calls []bool
	stubPointerLock(t, func(locked bool) (bool, error) {
		calls = append(calls, locked)
		return true, nil
	})

	before := RobloxDirectInputStats()
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	if testDirectRecButtonPressed() != true || testDirectRecButtonIndex() != 1 {
		t.Fatal("secondary DOWN was not delivered before the fallback grab")
	}
	if len(calls) != 1 || !calls[0] || !rmbPointerFallback.Load() {
		t.Fatalf("fallback acquire calls=%v active=%t, want [true],true", calls, rmbPointerFallback.Load())
	}

	// The getter remains false, so this regression proves the successful
	// held-RMB fallback retains the captured relative motion instead of
	// immediately releasing it through the APK getter-false branch.
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true,
		X: 20, Y: 22, DeltaX: 11, DeltaY: -7,
	})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 31 || y != 15 {
		t.Fatalf("fallback logical coordinate=(%v,%v), want (31,15)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 11 || dy != -7 {
		t.Fatalf("fallback captured delta=(%v,%v), want (11,-7)", dx, dy)
	}
	// A reverse delta advances the same virtual logical pair in the opposite
	// direction. The actual pointer remains host-anchored throughout.
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true,
		X: 20, Y: 22, DeltaX: -5, DeltaY: 4,
	})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 26 || y != 19 {
		t.Fatalf("reversed fallback logical coordinate=(%v,%v), want (26,19)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != -5 || dy != 4 {
		t.Fatalf("reversed fallback delta=(%v,%v), want (-5,4)", dx, dy)
	}

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 20, Y: 22,
	})
	if len(calls) != 2 || !calls[0] || calls[1] || rmbPointerFallback.Load() {
		t.Fatalf("fallback release calls=%v active=%t, want [true false],false", calls, rmbPointerFallback.Load())
	}
	if testDirectRecButtonPressed() {
		t.Fatal("secondary UP was not delivered before fallback cleanup")
	}
	// The release restores the real X11 anchor. Neither the old virtual
	// fallback origin nor stale recenter state may affect either following
	// ordinary physical movement.
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 15, Y: 18,
	})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 15 || y != 18 {
		t.Fatalf("first post-release absolute=(%v,%v), want (15,18)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != -5 || dy != -4 {
		t.Fatalf("first post-release delta=(%v,%v), want (-5,-4)", dx, dy)
	}
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 20, Y: 22,
	})
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 5 || dy != 4 {
		t.Fatalf("second post-release delta=(%v,%v), want (5,4)", dx, dy)
	}
	if got := RobloxDirectInputStats().ButtonDelivered - before.ButtonDelivered; got != 2 {
		t.Fatalf("secondary edge count=%d, want 2", got)
	}
}

func TestGetterFalseFallbackFailureKeepsSecondaryEdges(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(false)
	var calls []bool
	stubPointerLock(t, func(locked bool) (bool, error) {
		calls = append(calls, locked)
		if locked {
			return false, x11.ErrPointerGrab
		}
		return false, nil
	})

	before := RobloxDirectInputStats()
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	if testDirectRecButtonPressed() != true || rmbPointerFallback.Load() {
		t.Fatal("failed fallback swallowed the secondary DOWN or entered fallback state")
	}
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 20, Y: 22,
	})
	if testDirectRecButtonPressed() || rmbPointerFallback.Load() {
		t.Fatal("failed fallback swallowed the secondary UP or retained fallback state")
	}
	if len(calls) != 2 || !calls[0] || calls[1] {
		t.Fatalf("failed fallback calls=%v, want [true false]", calls)
	}
	if got := RobloxDirectInputStats().ButtonDelivered - before.ButtonDelivered; got != 2 {
		t.Fatalf("secondary edge count=%d, want 2", got)
	}
}

func TestGetterFalseFallbackFocusLossReleasesHostGrab(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(false)
	var calls []bool
	stubPointerLock(t, func(locked bool) (bool, error) {
		calls = append(calls, locked)
		return true, nil
	})

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	if !rmbPointerFallback.Load() {
		t.Fatal("getter-false fallback was not active before focus loss")
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	if len(calls) != 2 || !calls[0] || calls[1] || rmbPointerFallback.Load() {
		t.Fatalf("focus-loss fallback calls=%v active=%t, want [true false],false", calls, rmbPointerFallback.Load())
	}
}

// TestDirectMouseWheelABI pins the APK's unique nativePassMouseWheel(FFF)V
// static native. The official ACTION_SCROLL caller passes cached logical x/y
// followed by MotionEvent AXIS_VSCROLL (9).
func TestDirectMouseWheelABI(t *testing.T) {
	const env, class = uintptr(0x1234), uintptr(0x5678)
	wireRecordingDirectTarget(t, env, class)
	before := RobloxDirectInputStats()

	if !DispatchRobloxDirectScroll(401.25, 299.5, 0, -1) {
		t.Fatal("vertical wheel was not delivered")
	}
	if got := testDirectRecWheelID(0); got != env {
		t.Fatalf("recorded env = %#x, want %#x", got, env)
	}
	if got := testDirectRecWheelID(1); got != class {
		t.Fatalf("recorded class = %#x, want %#x", got, class)
	}
	if x, y, delta := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1), testDirectRecWheelFloat(2); x != 401.25 || y != 299.5 || delta != -1 {
		t.Fatalf("wheel args = (%v,%v,%v), want (401.25,299.5,-1)", x, y, delta)
	}
	if got := RobloxDirectInputStats().WheelDelivered - before.WheelDelivered; got != 1 {
		t.Fatalf("direct wheel delivery delta = %d, want 1", got)
	}

	// The same APK listener reads only AXIS_VSCROLL on ACTION_SCROLL. Do not
	// turn horizontal core-X11 wheel buttons into a touch-pan gesture.
	dropped := RobloxDirectInputStats().Dropped
	if DispatchRobloxDirectScroll(1, 2, 1, 0) {
		t.Fatal("horizontal wheel used an unproven native route")
	}
	if got := RobloxDirectInputStats().Dropped - dropped; got != 1 {
		t.Fatalf("horizontal wheel drop delta = %d, want 1", got)
	}
}

func TestX11ScrollBridgeUsesDirectWheelPath(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	before := RobloxDirectInputStats()
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 50, Y: 60, ScrollY: 1})
	if got := testDirectRecWheelFloat(2); got != 1 {
		t.Fatalf("direct wheel delta = %v, want +1", got)
	}
	if got := RobloxDirectInputStats().WheelDelivered - before.WheelDelivered; got != 1 {
		t.Fatalf("direct wheel delivery delta = %d, want 1", got)
	}
}

// TestDirectKeyEventABI pins the supplied APK's exact NativeGLInterface
// static-native call shape: nativePassKeyEvent(ZIIZ)V receives
// (JNIEnv*, jclass, down, KeyEvent.getScanCode(), KeyEvent.getKeyCode(),
// repeatCount > 0). The physical scan code is Linux evdev, not the Android
// keycode vocabulary carried in the next argument.
func TestDirectKeyEventABI(t *testing.T) {
	const env, class = uintptr(0x1234), uintptr(0x9abc)
	wireRecordingDirectKeyTarget(t, env, class)
	before := RobloxDirectInputStats()

	// X11's <AD01>=24 maps to evdev KEY_Q=16; AKEYCODE_Q is 45.
	if !DispatchRobloxDirectKey(24, 45, true) {
		t.Fatal("direct Q DOWN was not delivered")
	}
	if got := testDirectRecKeyID(0); got != env {
		t.Fatalf("recorded env = %#x, want %#x", got, env)
	}
	if got := testDirectRecKeyID(1); got != class {
		t.Fatalf("recorded class = %#x, want %#x", got, class)
	}
	if got := testDirectRecKeyInt(0); got != 1 {
		t.Fatalf("Q DOWN flag = %d, want 1", got)
	}
	if got := testDirectRecKeyInt(1); got != 16 {
		t.Fatalf("Q scan code = %d, want evdev KEY_Q=16", got)
	}
	if got := testDirectRecKeyInt(2); got != 45 {
		t.Fatalf("Q Android keycode = %d, want AKEYCODE_Q=45", got)
	}
	if got := testDirectRecKeyInt(3); got != 0 {
		t.Fatalf("Q repeat = %d, want 0 from one real X11 edge", got)
	}

	if !DispatchRobloxDirectKey(24, 45, false) {
		t.Fatal("direct Q UP was not delivered")
	}
	if got := testDirectRecKeyInt(0); got != 0 {
		t.Fatalf("Q UP flag = %d, want 0", got)
	}

	// Modifiers are ordinary physical edges in this ABI; no synthetic
	// modifier bitfield exists in nativePassKeyEvent's descriptor.
	if !DispatchRobloxDirectKey(50, 59, true) { // <LFSH>=50 -> KEY_LEFTSHIFT=42
		t.Fatal("direct left Shift DOWN was not delivered")
	}
	if got := testDirectRecKeyInt(1); got != 42 {
		t.Fatalf("Shift scan code = %d, want KEY_LEFTSHIFT=42", got)
	}
	if got := testDirectRecKeyInt(2); got != 59 {
		t.Fatalf("Shift Android keycode = %d, want AKEYCODE_SHIFT_LEFT=59", got)
	}
	if !DispatchRobloxDirectKey(50, 59, false) {
		t.Fatal("direct left Shift UP was not delivered")
	}

	// A non-text control keeps its independent Android keycode too:
	// <RTRN>=36 -> KEY_ENTER=28, AKEYCODE_ENTER=66.
	if !DispatchRobloxDirectKey(36, 66, true) {
		t.Fatal("direct Enter DOWN was not delivered")
	}
	if got := testDirectRecKeyInt(1); got != 28 {
		t.Fatalf("Enter scan code = %d, want KEY_ENTER=28", got)
	}
	if got := testDirectRecKeyInt(2); got != 66 {
		t.Fatalf("Enter Android keycode = %d, want AKEYCODE_ENTER=66", got)
	}
	if got := RobloxDirectInputStats().KeyDelivered - before.KeyDelivered; got != 5 {
		t.Fatalf("direct key delivery delta = %d, want 5", got)
	}
}

func TestDirectKeyRejectsIncompleteInput(t *testing.T) {
	wireRecordingDirectKeyTarget(t, 0x1234, 0x5678)
	before := RobloxDirectInputStats().Dropped
	if DispatchRobloxDirectKey(24, 0, true) {
		t.Fatal("key with no Android keycode was delivered")
	}
	if DispatchRobloxDirectKey(8, 45, true) {
		t.Fatal("reserved X11 keycode 8 was delivered")
	}
	if got := RobloxDirectInputStats().Dropped - before; got != 2 {
		t.Fatalf("direct key drop delta = %d, want 2", got)
	}
}

func TestKeyboardDeliveryPathGate(t *testing.T) {
	tests := []struct {
		name       string
		pointer    string
		keyboard   string
		wantGA     uint64
		wantDirect uint64
	}{
		{name: "default matches direct pointer listener", pointer: "direct", wantGA: 0, wantDirect: 1},
		{name: "explicit gameactivity control", pointer: "direct", keyboard: "gameactivity", wantGA: 1, wantDirect: 0},
		{name: "explicit direct", pointer: "gameactivity", keyboard: "direct", wantGA: 0, wantDirect: 1},
		{name: "both is diagnostic", pointer: "direct", keyboard: "both", wantGA: 1, wantDirect: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selectPointerPath(t, tc.pointer)
			selectKeyboardPath(t, tc.keyboard)
			vm := inputTestVM(t, map[string]uintptr{
				methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"): testRecordKeyFn(),
			})
			SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
			wireRecordingDirectKeyTarget(t, vm.Env().Raw(), 88)
			gaBefore := InputDeliveryStats().KeyDelivered
			directBefore := RobloxDirectInputStats().KeyDelivered

			handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 45, ScanCode: 24})

			if got := InputDeliveryStats().KeyDelivered - gaBefore; got != tc.wantGA {
				t.Fatalf("GameActivity key delivery delta = %d, want %d", got, tc.wantGA)
			}
			if got := RobloxDirectInputStats().KeyDelivered - directBefore; got != tc.wantDirect {
				t.Fatalf("direct key delivery delta = %d, want %d", got, tc.wantDirect)
			}
		})
	}
}

// TestDirectPointerMotionIsContinuous pins the X11 bridge behavior the direct
// APK listener requires: a normal unpressed MotionNotify is delivered, then a
// button-held motion continues the same absolute-position/delta stream.
func TestDirectPointerMotionIsContinuous(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	before := RobloxDirectInputStats()

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200,
	})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 100 || y != 200 {
		t.Fatalf("unpressed absolute move = (%v,%v), want (100,200)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 0 || dy != 0 {
		t.Fatalf("first unpressed move delta = (%v,%v), want (0,0)", dx, dy)
	}

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 104, Y: 197,
	})
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 4 || dy != -3 {
		t.Fatalf("continuous unpressed move delta = (%v,%v), want (4,-3)", dx, dy)
	}

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 104, Y: 197,
	})
	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 112.5, Y: 196.25,
	})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 112.5 || y != 196.25 {
		t.Fatalf("button-held absolute move = (%v,%v), want (112.5,196.25)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 8.5 || dy != -0.75 {
		t.Fatalf("button-held move delta = (%v,%v), want (8.5,-0.75)", dx, dy)
	}
	if got := RobloxDirectInputStats().MoveDelivered - before.MoveDelivered; got != 3 {
		t.Fatalf("continuous direct move delivery delta = %d, want 3", got)
	}
}

func TestPointerDeliveryPathGate(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantGA     uint64
		wantDirect uint64
	}{
		{name: "default direct desktop path", path: "", wantGA: 0, wantDirect: 2},
		{name: "direct only", path: "direct", wantGA: 0, wantDirect: 2},
		{name: "gameactivity is an explicit control", path: "gameactivity", wantGA: 2, wantDirect: 0},
		{name: "both explicit", path: "both", wantGA: 2, wantDirect: 2},
		{name: "unknown falls back to production default", path: "not-a-mode", wantGA: 0, wantDirect: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selectPointerPath(t, tc.path)
			vm := inputTestVM(t, map[string]uintptr{
				methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
			})
			SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
			wireRecordingDirectTarget(t, vm.Env().Raw(), 88)
			gaBefore := InputDeliveryStats().PointerDelivered
			directBefore := RobloxDirectInputStats().ButtonDelivered

			handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 631, Y: 383})
			handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 1, X: 631, Y: 383})

			if got := InputDeliveryStats().PointerDelivered - gaBefore; got != tc.wantGA {
				t.Fatalf("GameActivity pointer delta = %d, want %d", got, tc.wantGA)
			}
			if got := RobloxDirectInputStats().ButtonDelivered - directBefore; got != tc.wantDirect {
				t.Fatalf("direct button delta = %d, want %d", got, tc.wantDirect)
			}
		})
	}
}

func TestDirectInputDropsWithoutCompleteTarget(t *testing.T) {
	ClearRobloxDirectInputTarget()
	before := RobloxDirectInputStats().Dropped
	if DispatchRobloxDirectPointer(motionActionDown, 1, 2, 1) {
		t.Fatal("direct input delivered without a target")
	}
	if got := RobloxDirectInputStats().Dropped - before; got != 1 {
		t.Fatalf("direct drop delta = %d, want 1", got)
	}
}

func TestDirectKeyRepeatABI(t *testing.T) {
	selectKeyboardPath(t, "direct")
	vm := inputTestVM(t, nil)
	wireRecordingDirectKeyTarget(t, vm.Env().Raw(), 88)
	before := RobloxDirectInputStats().KeyDelivered
	for _, code := range []struct{ scan, android int32 }{{25, 51}, {22, 67}, {113, 21}, {50, 59}} {
		for i, edge := range []struct {
			down        bool
			count, flag int32
		}{{true, 0, 0}, {true, 1, 1}, {true, 2, 1}, {false, 0, 0}} {
			handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, ScanCode: code.scan, KeyCode: code.android, KeyPressed: edge.down, RepeatCount: edge.count})
			wantDown := int32(0)
			if edge.down {
				wantDown = 1
			}
			if testDirectRecKeyInt(0) != wantDown || testDirectRecKeyInt(1) != code.scan-8 || testDirectRecKeyInt(2) != code.android || testDirectRecKeyInt(3) != edge.flag {
				t.Fatalf("key %d edge %d native ABI: down=%d scan=%d key=%d repeat=%d", code.android, i, testDirectRecKeyInt(0), testDirectRecKeyInt(1), testDirectRecKeyInt(2), testDirectRecKeyInt(3))
			}
		}
	}
	if delta := RobloxDirectInputStats().KeyDelivered - before; delta != 16 {
		t.Fatalf("callbacks=%d, want every repeated down retained", delta)
	}
}

// Slash down is Roblox's chat-open gesture. The native down can
// synchronously focus RbxKeyboard, but that focus transition must not steal
// the matching repeat/up from the native listener. Otherwise the engine keeps
// slash held and a later physical slash can be ignored.
func TestSlashGestureKeepsInitialListenerAcrossEditorFocus(t *testing.T) {
	selectKeyboardPath(t, "direct")
	resetTextInputConnectionForTest()
	t.Cleanup(resetTextInputConnectionForTest)
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	wireRecordingDirectKeyTarget(t, vm.Env().Raw(), 88)
	before := RobloxDirectInputStats().KeyDelivered

	slash := x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 76, ScanCode: 61}
	handleX11InputEvent(slash)
	if got := testDirectRecKeyInt(0); got != 1 {
		t.Fatalf("slash initial edge down=%d, want 1", got)
	}
	if got := testDirectRecKeyInt(1); got != 53 {
		t.Fatalf("slash scan=%d, want evdev KEY_SLASH=53", got)
	}
	if got := testDirectRecKeyInt(2); got != 76 {
		t.Fatalf("slash Android keycode=%d, want AKEYCODE_SLASH=76", got)
	}

	// Model the exact re-entrant transition caused by Roblox handling slash:
	// its direct key callback calls showKeyboard before X11 later supplies the
	// repeated down and physical release.
	vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(101, 1, 0, 0))
	slash.RepeatCount = 1
	handleX11InputEvent(slash)
	if got := testDirectRecKeyInt(3); got != 1 {
		t.Fatalf("slash repeat flag=%d, want 1", got)
	}
	slash.KeyPressed = false
	slash.RepeatCount = 0
	handleX11InputEvent(slash)
	if got := testDirectRecKeyInt(0); got != 0 {
		t.Fatalf("slash final edge down=%d, want 0", got)
	}
	if delta := RobloxDirectInputStats().KeyDelivered - before; delta != 3 {
		t.Fatalf("native slash callbacks=%d, want down/repeat/up", delta)
	}

	// A key whose initial down occurs while the editor is already focused
	// remains editor-owned, so ordinary chat text does not leak to gameplay.
	before = RobloxDirectInputStats().KeyDelivered
	slash.KeyPressed = true
	handleX11InputEvent(slash)
	slash.KeyPressed = false
	handleX11InputEvent(slash)
	if delta := RobloxDirectInputStats().KeyDelivered - before; delta != 0 {
		t.Fatalf("focused-editor slash leaked %d native callbacks", delta)
	}
}

func TestKeyRepeatPreservesTextEditor(t *testing.T) {
	selectKeyboardPath(t, "direct")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	wireRecordingRbxTextTarget(t, vm)
	wireRecordingDirectKeyTarget(t, vm.Env().Raw(), 88)
	initID, infoID := keyboardTestObjects(t, vm, []byte(""), 1)
	vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig, testPackKeyboardArgs(101, 1, initID, infoID))
	// Each XIM commit remains intact, including supplementary runes.
	for i := 0; i < 3; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputText, Text: "😀"})
	}
	if got, _ := vm.Env().GetStringUTFChars(testRbxRecText()); got != "😀😀😀" {
		t.Fatal("repeated committed text missing")
	}
	before := RobloxDirectInputStats().KeyDelivered
	for i := int32(0); i < 2; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 21, ScanCode: 113, RepeatCount: i})
	}
	if testRbxRecCursor() != 2 {
		t.Fatalf("repeated left cursor=%d, want 2", testRbxRecCursor())
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyCode: 21, ScanCode: 113})
	for i := int32(0); i < 2; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 22, ScanCode: 114, RepeatCount: i})
	}
	for i := int32(0); i < 2; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 67, ScanCode: 22, RepeatCount: i})
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyCode: 67, ScanCode: 22})
	if got, _ := vm.Env().GetStringUTFChars(testRbxRecText()); got != "😀" {
		t.Fatal("repeated Backspace did not remove two complete runes")
	}
	if RobloxDirectInputStats().KeyDelivered != before {
		t.Fatal("editor navigation escaped to gameplay listener")
	}
}
