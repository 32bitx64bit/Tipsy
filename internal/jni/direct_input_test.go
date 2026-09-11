// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

func selectPointerPath(t *testing.T, value string) {
	t.Helper()
	t.Setenv("TIPSY_INPUT_PATH", value)
	ResetPointerInputPath()
	// Input-bridge tests default to the Home lifecycle state: an
	// onGameLoaded dispatched by an earlier test in this package must not
	// leak an in-experience signal here. Experience tests set their place
	// explicitly after this call; the cleanup restores Home for the next.
	ResetGameLoadedForTest()
	t.Cleanup(ResetPointerInputPath)
	t.Cleanup(ResetGameLoadedForTest)
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
	ResetPointerClampViewportForTest()
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
	oldCursor := pointerLockAtCursorSetter
	oldVisible := pointerCursorSetter
	pointerLockSetter = fn
	pointerLockAtCursorSetter = fn
	pointerCursorSetter = func(bool) {}
	rmbPointerFallback.Store(false)
	pointerLockSticky.Store(false)
	resetPointerCaptureState()
	t.Cleanup(func() {
		pointerLockSetter = old
		pointerLockAtCursorSetter = oldCursor
		pointerCursorSetter = oldVisible
		rmbPointerFallback.Store(false)
		pointerLockSticky.Store(false)
		resetPointerCaptureState()
	})
}

// selectMouseCapture sets TIPSY_MOUSE_CAPTURE for one test. Empty selects
// the production default (enabled).
func selectMouseCapture(t *testing.T, value string) {
	t.Helper()
	t.Setenv("TIPSY_MOUSE_CAPTURE", value)
	ResetPointerCapturePolicy()
	t.Cleanup(ResetPointerCapturePolicy)
}

// enterExperienceForTest records an engine onGameLoaded announcement for a
// joined experience (non-zero place id). selectPointerPath resets to Home;
// this is the explicit opt-in for persistent-capture tests.
func enterExperienceForTest(t *testing.T, vm *VM, placeID int64) {
	t.Helper()
	if placeID == 0 {
		t.Fatal("experience tests need a non-zero place id")
	}
	if _, handled := vm.dispatch(jnull(), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(placeID)); !handled {
		t.Fatal("gameActivity_onGameLoaded not handled")
	}
	t.Cleanup(ResetGameLoadedForTest)
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

func TestDirectCapturedMouseMovePreservesFractionalDelta(t *testing.T) {
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	if !DispatchRobloxDirectPointer(motionActionDown, 20, 22, 3) {
		t.Fatal("secondary DOWN was not delivered")
	}
	if !DispatchRobloxDirectPointerDelta(20, 22, 0.25, -0.125) {
		t.Fatal("fractional captured move was not delivered")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 20.25 || y != 21.875 {
		t.Fatalf("fractional captured absolute = (%v,%v), want (20.25,21.875)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 0.25 || dy != -0.125 {
		t.Fatalf("fractional captured delta = (%v,%v), want (0.25,-0.125)", dx, dy)
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
	if pointerLockSticky.Load() {
		t.Fatal("held-RMB fallback must not sticky-recenter after Alt-Tab")
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
	if len(calls) != 1 || !calls[0] || rmbPointerFallback.Load() {
		t.Fatalf("focus-loss fallback calls=%v active=%t, want [true],false", calls, rmbPointerFallback.Load())
	}
}

func TestGetterFalseFallbackUsesCursorAnchorNotCenter(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(false)
	var centerCalls, cursorCalls []bool
	oldCenter := pointerLockSetter
	oldCursor := pointerLockAtCursorSetter
	pointerLockSetter = func(locked bool) (bool, error) {
		centerCalls = append(centerCalls, locked)
		return true, nil
	}
	pointerLockAtCursorSetter = func(locked bool) (bool, error) {
		cursorCalls = append(cursorCalls, locked)
		return true, nil
	}
	rmbPointerFallback.Store(false)
	pointerLockSticky.Store(false)
	t.Cleanup(func() {
		pointerLockSetter = oldCenter
		pointerLockAtCursorSetter = oldCursor
		rmbPointerFallback.Store(false)
		pointerLockSticky.Store(false)
	})

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	if len(cursorCalls) != 1 || !cursorCalls[0] {
		t.Fatalf("cursor-anchor acquire calls=%v, want [true]", cursorCalls)
	}
	if len(centerCalls) != 0 {
		t.Fatalf("centered acquire calls=%v, want none on held-RMB fallback", centerCalls)
	}
	if pointerLockSticky.Load() {
		t.Fatal("held-RMB fallback set first-person sticky recapture")
	}

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 20, Y: 22,
	})
	if len(cursorCalls) != 1 || len(centerCalls) != 1 || centerCalls[0] {
		t.Fatalf("RMB release centerCalls=%v cursorCalls=%v, want unlock via centered setter false", centerCalls, cursorCalls)
	}
}

func TestGetterFalseFallbackFocusInDoesNotRecapture(t *testing.T) {
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
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("held-RMB focus-return lock calls=%v, want [true] (no recapture)", calls)
	}
	if pointerLockSticky.Load() || rmbPointerFallback.Load() {
		t.Fatal("held-RMB fallback remained sticky after focus return")
	}
}

func TestGetterTrueFocusInReacquiresHostGrab(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	testDirectRecSetMouseLocked(true)
	var calls []bool
	stubPointerLock(t, func(locked bool) (bool, error) {
		calls = append(calls, locked)
		return true, nil
	})

	handleX11InputEvent(x11.InputEvent{
		Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22,
	})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	testDirectRecSetMouseLocked(false)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if len(calls) != 2 || !calls[0] || !calls[1] {
		t.Fatalf("focus-return lock calls=%v, want [true true] (no explicit unlock)", calls)
	}
	if !pointerLockSticky.Load() {
		t.Fatal("sticky first-person lock was cleared on focus loss")
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

// --- Desktop persistent pointer capture ---
//
// The engine getter never reports LockCenter for Project 12 [BODY CAM!], so
// the official Android listener would leave the pointer free and stop camera
// travel at the screen edge. The desktop policy captures while in a joined
// experience and frees on a LeftAlt toggle. Every test below pins the
// no-regression boundary against the held-RMB and LockCenter paths above.

type captureCalls struct {
	center  []bool
	cursor  []bool
	zoom    []bool // SetPointerLockAtCenter: the zoom-lock grab
	visible []bool
}

// grabs counts every host grab call of any anchor kind.
func (c *captureCalls) grabs() int { return len(c.center) + len(c.cursor) + len(c.zoom) }

// stubCaptureSeams replaces the host grab and cursor boundaries with
// recorders that always report success. Centered and cursor-anchored grabs
// stay distinguishable, unlike stubPointerLock's shared stub.
func stubCaptureSeams(t *testing.T) *captureCalls {
	t.Helper()
	calls := &captureCalls{}
	oldCenter := pointerLockSetter
	oldCursor := pointerLockAtCursorSetter
	oldZoom := pointerLockAtCenterSetter
	oldVisible := pointerCursorSetter
	pointerLockSetter = func(locked bool) (bool, error) {
		calls.center = append(calls.center, locked)
		return true, nil
	}
	pointerLockAtCursorSetter = func(locked bool) (bool, error) {
		calls.cursor = append(calls.cursor, locked)
		return true, nil
	}
	pointerLockAtCenterSetter = func(locked bool) (bool, error) {
		calls.zoom = append(calls.zoom, locked)
		return true, nil
	}
	pointerCursorSetter = func(visible bool) {
		calls.visible = append(calls.visible, visible)
	}
	rmbPointerFallback.Store(false)
	pointerLockSticky.Store(false)
	resetPointerCaptureState()
	t.Cleanup(func() {
		pointerLockSetter = oldCenter
		pointerLockAtCursorSetter = oldCursor
		pointerLockAtCenterSetter = oldZoom
		pointerCursorSetter = oldVisible
		rmbPointerFallback.Store(false)
		pointerLockSticky.Store(false)
		resetPointerCaptureState()
	})
	return calls
}

func newCaptureVM(t *testing.T) *VM {
	t.Helper()
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	return vm
}

func TestPersistentCaptureAcquiresAndIntegratesMotion(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	before := RobloxDirectInputStats()

	// The first absolute motion is consumed as the capture transition.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	if len(calls.cursor) != 1 || !calls.cursor[0] {
		t.Fatalf("anchored acquire calls=%v, want [true]", calls.cursor)
	}
	if len(calls.center) != 0 {
		t.Fatalf("centered calls=%v, want none for persistent acquire", calls.center)
	}
	if !persistentPointerCapture.Load() {
		t.Fatal("persistent capture flag not set after transition move")
	}
	if got := RobloxDirectInputStats().MoveDelivered - before.MoveDelivered; got != 0 {
		t.Fatalf("transition move deliveries=%d, want 0 (consumed)", got)
	}
	if len(calls.visible) != 1 || calls.visible[0] {
		t.Fatalf("cursor visible calls=%v, want [false] (hidden)", calls.visible)
	}

	// Captured samples integrate the unbounded logical pair.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: 11, DeltaY: -7})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 111 || y != 193 {
		t.Fatalf("persistent logical=(%v,%v), want (111,193)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 11 || dy != -7 {
		t.Fatalf("persistent delta=(%v,%v), want (11,-7)", dx, dy)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: -5, DeltaY: 4})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 106 || y != 197 {
		t.Fatalf("reversed persistent logical=(%v,%v), want (106,197)", x, y)
	}
	if got := RobloxDirectInputStats().MoveDelivered - before.MoveDelivered; got != 2 {
		t.Fatalf("captured move deliveries=%d, want 2", got)
	}
}

func TestPersistentCaptureEnvironmentOptOutKeepsAbsoluteMotion(t *testing.T) {
	for _, value := range []string{"0", "false", "off", "no", "FALSE", " Off "} {
		t.Run(value, func(t *testing.T) {
			selectPointerPath(t, "direct")
			selectMouseCapture(t, value)
			wireRecordingDirectTarget(t, 0x1234, 0x5678)
			calls := stubCaptureSeams(t)
			vm := newCaptureVM(t)
			enterExperienceForTest(t, vm, 155615604)
			testDirectRecSetMouseLocked(false)
			before := RobloxDirectInputStats()

			handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
			handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 104, Y: 197})
			if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 4 || dy != -3 {
				t.Fatalf("opt-out move delta=(%v,%v), want (4,-3)", dx, dy)
			}
			if got := RobloxDirectInputStats().MoveDelivered - before.MoveDelivered; got != 2 {
				t.Fatalf("opt-out deliveries=%d, want 2", got)
			}
			if len(calls.cursor)+len(calls.center) != 0 {
				t.Fatalf("opt-out grab calls center=%v cursor=%v, want none", calls.center, calls.cursor)
			}
			if persistentPointerCapture.Load() {
				t.Fatal("opt-out engaged persistent capture")
			}
		})
	}
}

func TestPersistentCaptureHomeKeepsAbsoluteAndAltPassThrough(t *testing.T) {
	selectPointerPath(t, "direct")
	selectKeyboardPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	wireRecordingDirectKeyTarget(t, 0x1234, 0x88)
	calls := stubCaptureSeams(t)
	// No enterExperience: Home lifecycle state.
	testDirectRecSetMouseLocked(false)
	moveBefore := RobloxDirectInputStats().MoveDelivered
	keyBefore := RobloxDirectInputStats().KeyDelivered

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 104, Y: 197})
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 4 || dy != -3 {
		t.Fatalf("Home move delta=(%v,%v), want (4,-3)", dx, dy)
	}
	if got := RobloxDirectInputStats().MoveDelivered - moveBefore; got != 2 {
		t.Fatalf("Home move deliveries=%d, want 2", got)
	}
	if len(calls.cursor)+len(calls.center) != 0 {
		t.Fatalf("Home grab calls center=%v cursor=%v, want none", calls.center, calls.cursor)
	}

	// LeftAlt (X11 keycode 64 → evdev 56, Android 57) routes to the engine.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 57, ScanCode: 64})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: 57, ScanCode: 64})
	if got := RobloxDirectInputStats().KeyDelivered - keyBefore; got != 2 {
		t.Fatalf("Home Alt key deliveries=%d, want 2 (pass-through)", got)
	}
	if pointerCaptureReleased.Load() {
		t.Fatal("Home Alt press toggled capture release")
	}
}

func TestPersistentCaptureToggleReleasesAndRecaptures(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	selectKeyboardPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	wireRecordingDirectKeyTarget(t, 0x1234, 0x88)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	if !persistentPointerCapture.Load() {
		t.Fatal("persistent capture did not acquire")
	}
	keyBefore := RobloxDirectInputStats().KeyDelivered

	// Toggle off: ungrab, visible cursor, Alt edges swallowed.
	altDown := x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 57, ScanCode: 64}
	altRepeat := x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 57, ScanCode: 64, RepeatCount: 1}
	altUp := x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: 57, ScanCode: 64}
	handleX11InputEvent(altDown)
	if !pointerCaptureReleased.Load() {
		t.Fatal("Alt press did not release capture")
	}
	if persistentPointerCapture.Load() {
		t.Fatal("persistent flag survived Alt release")
	}
	if len(calls.center) != 1 || calls.center[0] {
		t.Fatalf("release grab calls center=%v, want [false]", calls.center)
	}
	if n := len(calls.visible); n != 2 || calls.visible[n-1] != true {
		t.Fatalf("cursor calls=%v, want last true (visible)", calls.visible)
	}
	handleX11InputEvent(altRepeat)
	handleX11InputEvent(altUp)
	if got := RobloxDirectInputStats().KeyDelivered - keyBefore; got != 0 {
		t.Fatalf("Alt toggle key deliveries=%d, want 0 (consumed)", got)
	}

	// Free absolute motion while released.
	moveBefore := RobloxDirectInputStats().MoveDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 50, Y: 60})
	if got := RobloxDirectInputStats().MoveDelivered - moveBefore; got != 1 {
		t.Fatalf("released move deliveries=%d, want 1 (absolute)", got)
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 50 || y != 60 {
		t.Fatalf("released absolute=(%v,%v), want (50,60)", x, y)
	}

	// Toggle back on: hidden cursor, next motion re-acquires.
	handleX11InputEvent(altDown)
	if pointerCaptureReleased.Load() {
		t.Fatal("second Alt press did not re-arm capture")
	}
	if n := len(calls.visible); calls.visible[n-1] != false {
		t.Fatalf("cursor calls=%v, want last false (hidden)", calls.visible)
	}
	handleX11InputEvent(altUp)
	cursorAcquires := len(calls.cursor)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 60, Y: 70})
	if len(calls.cursor) != cursorAcquires+1 || !calls.cursor[cursorAcquires] {
		t.Fatalf("re-acquire cursor calls=%v, want one more [true]", calls.cursor)
	}
	if !persistentPointerCapture.Load() {
		t.Fatal("persistent capture did not re-acquire after toggle-on")
	}
}

func TestPointerCaptureToggleReleasesStickyEngineLock(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 155615604)
	testDirectRecSetMouseLocked(true)

	// Getter-true transition move acquires centered sticky.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 640, Y: 360})
	if !pointerLockSticky.Load() {
		t.Fatal("getter-true move did not set sticky")
	}
	if len(calls.center) != 1 || !calls.center[0] {
		t.Fatalf("centered acquire calls=%v, want [true]", calls.center)
	}

	// Alt toggle releases everything and gates the getter-true branch.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 57, ScanCode: 64})
	if !pointerCaptureReleased.Load() || pointerLockSticky.Load() {
		t.Fatal("Alt toggle did not release sticky centered lock")
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: 57, ScanCode: 64})
	centerCalls := len(calls.center)
	moveBefore := RobloxDirectInputStats().MoveDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 100})
	if len(calls.center) != centerCalls {
		t.Fatalf("released getter-true move grabbed center=%v, want gated", calls.center)
	}
	if got := RobloxDirectInputStats().MoveDelivered - moveBefore; got != 1 {
		t.Fatalf("released move deliveries=%d, want 1 absolute", got)
	}

	// Toggle back: the next getter-true move re-acquires centered.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 57, ScanCode: 64})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: 57, ScanCode: 64})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 200, Y: 200})
	if !pointerLockSticky.Load() {
		t.Fatal("re-armed getter-true move did not restore sticky")
	}
	if len(calls.center) != centerCalls+1 || !calls.center[centerCalls] {
		t.Fatalf("re-acquire center calls=%v, want one more [true]", calls.center)
	}
}

func TestPersistentCaptureFocusLossClearsWithoutUnlock(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	if !persistentPointerCapture.Load() {
		t.Fatal("persistent capture did not acquire")
	}
	// Focus loss drops the transient stream without an explicit unlock (X11
	// already ungrabs) and preserves the released toggle (false here).
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	if persistentPointerCapture.Load() {
		t.Fatal("focus loss did not clear persistent capture")
	}
	if len(calls.center)+len(calls.cursor) != 1 {
		t.Fatalf("focus-loss grab calls center=%v cursor=%v, want only the acquire", calls.center, calls.cursor)
	}
	// Focus gain reapplies the hidden cursor but waits for motion to
	// re-acquire (no click needed, no grab yet).
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if len(calls.center)+len(calls.cursor) != 1 {
		t.Fatalf("focus-gain grab calls center=%v cursor=%v, want no recapture yet", calls.center, calls.cursor)
	}
	if n := len(calls.visible); n != 2 || calls.visible[n-1] != false {
		t.Fatalf("cursor calls=%v, want [false false] (hidden reapplied)", calls.visible)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 120, Y: 210})
	if !persistentPointerCapture.Load() {
		t.Fatal("post-focus motion did not re-acquire persistent capture")
	}
}

func TestPersistentCaptureRMBRestoresClickPointAndStaysCaptured(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: 11, DeltaY: -7})
	grabsBefore := len(calls.cursor) + len(calls.center)

	// RMB down lands at the logical cursor (111,193); no second grab.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 100, Y: 200})
	if x, y := testDirectRecButtonFloat(0), testDirectRecButtonFloat(1); x != 111 || y != 193 {
		t.Fatalf("captured RMB down=(%v,%v), want logical (111,193)", x, y)
	}
	if !testDirectRecButtonPressed() || testDirectRecButtonIndex() != 1 {
		t.Fatal("captured RMB down lost pressed/index")
	}
	// Held relative motion integrates from the press point with exact deltas.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: -5, DeltaY: 4})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 106 || y != 197 {
		t.Fatalf("held RMB logical=(%v,%v), want (106,197)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != -5 || dy != 4 {
		t.Fatalf("held RMB delta=(%v,%v), want (-5,4)", dx, dy)
	}
	// The release lands at the press anchor (LockCurrentPosition), exactly
	// like the old held-RMB fallback released at its grab anchor.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 100, Y: 200})
	if x, y := testDirectRecButtonFloat(0), testDirectRecButtonFloat(1); x != 111 || y != 193 {
		t.Fatalf("captured RMB up=(%v,%v), want press anchor (111,193)", x, y)
	}
	if testDirectRecButtonPressed() {
		t.Fatal("captured RMB up still pressed")
	}
	// Post-release motion continues from the anchor: the engine cursor is
	// back where the operator clicked.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: 2, DeltaY: 3})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 113 || y != 196 {
		t.Fatalf("post-release logical=(%v,%v), want (113,196) from the anchor", x, y)
	}
	if !persistentPointerCapture.Load() {
		t.Fatal("persistent stream did not survive RMB")
	}
	if rmbPointerFallback.Load() {
		t.Fatal("RMB fallback flag set inside persistent stream")
	}
	if got := len(calls.cursor) + len(calls.center); got != grabsBefore {
		t.Fatalf("grab calls grew to %d, want %d (RMB is an ordinary button while captured)", got, grabsBefore)
	}
}

func TestPersistentCaptureRMBOffViewPinsPressAndRestoresIt(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	// A long first-person look drifts the logical cursor off-view.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 1270, Y: 710})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 1270, Y: 710, DeltaX: 50, DeltaY: 50})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 1320 || y != 760 {
		t.Fatalf("drifted logical=(%v,%v), want unbounded (1320,760)", x, y)
	}
	// The press pins on-view and re-seeds the drag from that point.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 1270, Y: 710})
	if x, y := testDirectRecButtonFloat(0), testDirectRecButtonFloat(1); x != 1279 || y != 719 {
		t.Fatalf("off-view RMB down=(%v,%v), want pinned (1279,719)", x, y)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 1270, Y: 710, DeltaX: -100, DeltaY: -100})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 1179 || y != 619 {
		t.Fatalf("held RMB logical=(%v,%v), want (1179,619) from the pinned press", x, y)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 1270, Y: 710})
	if x, y := testDirectRecButtonFloat(0), testDirectRecButtonFloat(1); x != 1279 || y != 719 {
		t.Fatalf("off-view RMB up=(%v,%v), want press anchor (1279,719)", x, y)
	}
	if fx, fy, ok := RobloxDirectFallbackPosition(); !ok || fx != 1279 || fy != 719 {
		t.Fatalf("post-release integrator=(%v,%v,%v), want (1279,719,true)", fx, fy, ok)
	}
}

func TestCapturedFallbackReentersViewportWithoutDeadZone(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	t.Cleanup(ResetPointerClampViewportForTest)

	// Outward travel is unbounded: every sample keeps changing position.
	BeginRobloxDirectPointerFallback(1270, 710)
	if !DispatchRobloxDirectPointerFallbackDelta(50, 50) {
		t.Fatal("outward delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 1320 || y != 760 {
		t.Fatalf("outward logical=(%v,%v), want (1320,760)", x, y)
	}
	if !DispatchRobloxDirectPointerFallbackDelta(500, 0) {
		t.Fatal("further outward delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 1820 || y != 760 {
		t.Fatalf("further outward logical=(%v,%v), want (1820,760)", x, y)
	}
	// Reversing re-enters from the crossed edge at once; the delta is exact.
	if !DispatchRobloxDirectPointerFallbackDelta(-10, -10) {
		t.Fatal("inward delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 1269 || y != 709 {
		t.Fatalf("re-entered logical=(%v,%v), want (1269,709)", x, y)
	}
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != -10 || dy != -10 {
		t.Fatalf("re-entry delta=(%v,%v), want (-10,-10) exact", dx, dy)
	}
	// Same contract past the origin.
	if !DispatchRobloxDirectPointerFallbackDelta(-2000, -2000) {
		t.Fatal("negative outward delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != -731 || y != -1291 {
		t.Fatalf("negative outward logical=(%v,%v), want (-731,-1291)", x, y)
	}
	if !DispatchRobloxDirectPointerFallbackDelta(5, 6) {
		t.Fatal("return delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 5 || y != 6 {
		t.Fatalf("returned logical=(%v,%v), want (5,6)", x, y)
	}
	// Inside the surface the pair is untouched.
	if !DispatchRobloxDirectPointerFallbackDelta(100, 100) {
		t.Fatal("interior delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 105 || y != 106 {
		t.Fatalf("interior logical=(%v,%v), want (105,106)", x, y)
	}
}

func TestScrollGetterTrueConvertsPersistentCaptureOnce(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 155615604)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: 11, DeltaY: -7})

	testDirectRecSetMouseLocked(true)
	queriesBefore := RobloxDirectInputStats().LockQueries
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: 1})
	if got := RobloxDirectInputStats().LockQueries - queriesBefore; got != 1 {
		t.Fatalf("scroll getter queries=%d, want exactly 1", got)
	}
	if x, y, d := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1), testDirectRecWheelFloat(2); x != 111 || y != 193 || d != 1 {
		t.Fatalf("captured wheel=(%v,%v,%v), want (111,193,1)", x, y, d)
	}
	if persistentPointerCapture.Load() || !pointerLockSticky.Load() {
		t.Fatal("scroll did not convert persistent to centered sticky")
	}
	if len(calls.center) != 1 || !calls.center[0] {
		t.Fatalf("conversion center calls=%v, want [true]", calls.center)
	}
}

func TestGetterFalseScrollUsesLogicalPositionAndKeepsPersistentCapture(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: 11, DeltaY: -7})
	grabsBefore := len(calls.cursor) + len(calls.center)
	visibleBefore := len(calls.visible)
	queriesBefore := RobloxDirectInputStats().LockQueries
	wheelsBefore := RobloxDirectInputStats().WheelDelivered

	// Any number of getter-false detents preserve the anchored stream.
	for i := 0; i < 3; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: -1})
	}
	if got := RobloxDirectInputStats().WheelDelivered - wheelsBefore; got != 3 {
		t.Fatalf("wheel deliveries=%d, want 3 (UI scroll preserved)", got)
	}
	if got := RobloxDirectInputStats().LockQueries - queriesBefore; got != 3 {
		t.Fatalf("scroll getter queries=%d, want exactly 3 (one per detent)", got)
	}
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 111 || y != 193 {
		t.Fatalf("captured wheel=(%v,%v), want logical (111,193)", x, y)
	}
	if !persistentPointerCapture.Load() {
		t.Fatal("getter-false scroll suspended persistent capture")
	}
	if got := len(calls.cursor) + len(calls.center); got != grabsBefore {
		t.Fatalf("grab calls grew to %d, want %d (stream preserved)", got, grabsBefore)
	}
	if len(calls.visible) != visibleBefore {
		t.Fatalf("cursor calls=%v, want no change (stays hidden)", calls.visible)
	}
}

func TestWheelSequencePreservesFallbackThenConvertsAndReappliesCursorPolicy(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 155615604)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: -1})
	if !persistentPointerCapture.Load() {
		t.Fatal("first getter-false scroll did not preserve persistent capture")
	}
	testDirectRecSetMouseLocked(true)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: -1})
	if persistentPointerCapture.Load() || !pointerLockSticky.Load() {
		t.Fatal("getter-true scroll did not convert to centered sticky")
	}
	if len(calls.center) != 1 || !calls.center[0] {
		t.Fatalf("conversion center calls=%v, want [true]", calls.center)
	}
	for _, v := range calls.visible {
		if v {
			t.Fatalf("cursor calls=%v, want always hidden (no release in this sequence)", calls.visible)
		}
	}
	// Zoom back out: getter-false from centered releases on the detent.
	testDirectRecSetMouseLocked(false)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 640, Y: 360, ScrollY: 1})
	if pointerLockSticky.Load() {
		t.Fatal("zoom-out scroll did not release centered sticky")
	}
	if len(calls.center) != 2 || calls.center[1] {
		t.Fatalf("release center calls=%v, want [true false]", calls.center)
	}
	for _, v := range calls.visible {
		if v {
			t.Fatalf("cursor calls=%v, want always hidden after zoom-out release", calls.visible)
		}
	}
	// The next motion restores the anchored fallback, proving the release
	// left a re-acquirable stream rather than a stuck state.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 300, Y: 300})
	if !persistentPointerCapture.Load() {
		t.Fatal("post-zoom-out motion did not restore persistent capture")
	}
}

func TestAltTabRestoresActivePointerPolicy(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	if !persistentPointerCapture.Load() {
		t.Fatal("persistent capture did not acquire")
	}
	// Alt-Tab chord: Alt press toggles off, the WM takes focus while Alt is
	// still held (X11 synthesizes the Alt release first, then focus loss
	// carrying the Alt-held evidence).
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 57, ScanCode: 64})
	if !pointerCaptureReleased.Load() {
		t.Fatal("Alt press did not toggle release before Alt-Tab")
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: 57, ScanCode: 64})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false, FocusAltHeld: true})
	if pointerCaptureReleased.Load() {
		t.Fatal("Alt-Tab focus loss did not neutralize the chord toggle")
	}
	if n := len(calls.visible); calls.visible[n-1] != false {
		t.Fatalf("cursor calls=%v, want last false (hidden restored)", calls.visible)
	}
	// Focus return: no recapture yet (waits for motion), cursor hidden.
	grabsBefore := len(calls.cursor) + len(calls.center)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if got := len(calls.cursor) + len(calls.center); got != grabsBefore {
		t.Fatalf("focus-gain grab calls grew to %d, want %d (motion re-acquires)", got, grabsBefore)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 120, Y: 210})
	if !persistentPointerCapture.Load() {
		t.Fatal("post-Alt-Tab motion did not re-acquire persistent capture")
	}
}

func TestAltTabRestoresStickyCenteredPolicy(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 155615604)
	testDirectRecSetMouseLocked(true)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 640, Y: 360})
	if !pointerLockSticky.Load() {
		t.Fatal("centered sticky did not acquire")
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 57, ScanCode: 64})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: 57, ScanCode: 64})
	// Neutralize; the sticky request must survive the chord.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false, FocusAltHeld: true})
	if pointerCaptureReleased.Load() || !pointerLockSticky.Load() {
		t.Fatalf("Alt-Tab restore released=%t sticky=%t, want false,true",
			pointerCaptureReleased.Load(), pointerLockSticky.Load())
	}
	// Focus return recaptures centered without waiting for a click. The
	// getter often drops across focus loss; sticky must still recapture.
	centerBefore := len(calls.center)
	testDirectRecSetMouseLocked(false)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if len(calls.center) != centerBefore+1 || !calls.center[centerBefore] {
		t.Fatalf("focus-gain center calls=%v, want recapture [true]", calls.center)
	}
}

func TestHeldRMBWorksFromUnlockedUIStream(t *testing.T) {
	// Home: no persistent interference. Getter-false RMB acquires the
	// cursor-anchored fallback, hides the cursor, integrates, and releases
	// once at the physical anchor.
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22})
	if len(calls.cursor) != 1 || !calls.cursor[0] {
		t.Fatalf("RMB acquire cursor calls=%v, want [true]", calls.cursor)
	}
	if len(calls.center) != 0 {
		t.Fatalf("RMB acquire center calls=%v, want none (anchored, not centered)", calls.center)
	}
	if !rmbPointerFallback.Load() || persistentPointerCapture.Load() {
		t.Fatal("RMB fallback state wrong from unlocked stream")
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 20, Y: 22, DeltaX: 11, DeltaY: -7})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 31 || y != 15 {
		t.Fatalf("RMB logical=(%v,%v), want (31,15)", x, y)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 20, Y: 22})
	if len(calls.center) != 1 || calls.center[0] {
		t.Fatalf("RMB release center calls=%v, want [false]", calls.center)
	}
	if rmbPointerFallback.Load() {
		t.Fatal("RMB fallback survived release")
	}
	// Post-release motion is absolute from the physical anchor (Home: no
	// persistent steal).
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 15, Y: 18})
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 15 || y != 18 {
		t.Fatalf("post-release absolute=(%v,%v), want (15,18)", x, y)
	}
	for _, v := range calls.visible {
		if v {
			t.Fatalf("cursor calls=%v, want always hidden on this stream", calls.visible)
		}
	}
}

func TestHeldRMBConvertsToCenteredWhenGetterTurnsTrue(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22})
	if !rmbPointerFallback.Load() {
		t.Fatal("RMB fallback did not acquire")
	}
	// Getter flips true mid-hold (shift-lock pressed while aiming): the
	// next captured sample converts in place to centered sticky.
	testDirectRecSetMouseLocked(true)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 20, Y: 22, DeltaX: 11, DeltaY: -7})
	if rmbPointerFallback.Load() || !pointerLockSticky.Load() {
		t.Fatal("getter-true sample did not convert RMB fallback to centered sticky")
	}
	if len(calls.center) != 1 || !calls.center[0] {
		t.Fatalf("conversion center calls=%v, want [true]", calls.center)
	}
	// Release with the getter still true stays centered (no ungrab).
	centerBefore := len(calls.center)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 20, Y: 22})
	if len(calls.center) != centerBefore {
		t.Fatalf("center calls grew to %v, want no release while getter-true", calls.center)
	}
	if !pointerLockSticky.Load() {
		t.Fatal("sticky cleared while getter-true")
	}
}

func TestLeavingExperienceReleasesPersistentCapture(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	if !persistentPointerCapture.Load() {
		t.Fatal("persistent capture did not acquire")
	}
	// Leave to Home: the engine announces place 0.
	if _, handled := vm.dispatch(jnull(), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
		t.Fatal("leave onGameLoaded not handled")
	}
	// A relative sample from the dying grab is consumed, not delivered.
	moveBefore := RobloxDirectInputStats().MoveDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: 5, DeltaY: 5})
	if got := RobloxDirectInputStats().MoveDelivered - moveBefore; got != 0 {
		t.Fatalf("dying-grab deliveries=%d, want 0 (consumed)", got)
	}
	if persistentPointerCapture.Load() {
		t.Fatal("persistent flag survived leaving the experience")
	}
	if len(calls.center) != 1 || calls.center[0] {
		t.Fatalf("leave release center calls=%v, want [false]", calls.center)
	}
	// Absolute motion is free Home navigation again.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 50, Y: 60})
	if got := RobloxDirectInputStats().MoveDelivered - moveBefore; got != 1 {
		t.Fatalf("Home move deliveries=%d, want 1", got)
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 50 || y != 60 {
		t.Fatalf("Home absolute=(%v,%v), want (50,60)", x, y)
	}
}

func TestPersistentCaptureGrabFailureFallsBackToAbsolute(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	// Acquisition always rejected (another client holds the pointer).
	oldCursor := pointerLockAtCursorSetter
	oldCenter := pointerLockSetter
	oldVisible := pointerCursorSetter
	pointerLockAtCursorSetter = func(bool) (bool, error) { return false, x11.ErrPointerGrab }
	pointerLockSetter = func(bool) (bool, error) { return false, nil }
	pointerCursorSetter = func(bool) {}
	resetPointerCaptureState()
	t.Cleanup(func() {
		pointerLockAtCursorSetter, pointerLockSetter, pointerCursorSetter = oldCursor, oldCenter, oldVisible
		resetPointerCaptureState()
	})
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	// Rejected acquisition must not consume moves nor break continuity.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 104, Y: 197})
	if dx, dy := testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); dx != 4 || dy != -3 {
		t.Fatalf("rejected-grab move delta=(%v,%v), want (4,-3)", dx, dy)
	}
	if persistentPointerCapture.Load() {
		t.Fatal("rejected grab set persistent capture")
	}
}

func TestCapturedPointsPinWhileMotionStaysUnbounded(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	t.Cleanup(ResetPointerClampViewportForTest)

	// The engine differentiates positions, so captured motion must stay
	// unbounded (pinning reads as no motion and the camera stops). Points
	// carry no continuity and pin to the live surface instead.
	BeginRobloxDirectPointerFallback(1270, 710)
	if !DispatchRobloxDirectPointerFallbackDelta(50, 50) {
		t.Fatal("fallback delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 1320 || y != 760 {
		t.Fatalf("motion logical=(%v,%v), want unbounded (1320,760)", x, y)
	}
	if !DispatchRobloxDirectButtonCaptured(motionActionDown, 1) {
		t.Fatal("captured button did not dispatch")
	}
	if x, y := testDirectRecButtonFloat(0), testDirectRecButtonFloat(1); x != 1279 || y != 719 {
		t.Fatalf("button point=(%v,%v), want pinned (1279,719)", x, y)
	}
	fx, fy, ok := RobloxDirectFallbackPosition()
	if !ok || fx != 1320 || fy != 760 {
		t.Fatalf("fallback position=(%v,%v,%v), want (1320,760,true) untouched by the click", fx, fy, ok)
	}
	if !DispatchRobloxDirectScroll(5000, -5000, 0, 1) {
		t.Fatal("wheel did not dispatch")
	}
	if x, y, d := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1), testDirectRecWheelFloat(2); x != 1279 || y != 0 || d != 1 {
		t.Fatalf("wheel point=(%v,%v,%v), want (1279,0,1)", x, y, d)
	}
}

func TestCapturedPointsFollowViewportResize(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	t.Cleanup(ResetPointerClampViewportForTest)

	SetPointerClampViewport(800, 600)
	BeginRobloxDirectPointerFallback(790, 590)
	if !DispatchRobloxDirectPointerFallbackDelta(50, 50) {
		t.Fatal("fallback delta did not dispatch")
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 840 || y != 640 {
		t.Fatalf("motion logical=(%v,%v), want unbounded (840,640)", x, y)
	}
	fx, fy, _ := RobloxDirectFallbackPosition()
	if !DispatchRobloxDirectScroll(fx, fy, 0, 1) {
		t.Fatal("wheel did not dispatch")
	}
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 799 || y != 599 {
		t.Fatalf("resized wheel point=(%v,%v), want (799,599)", x, y)
	}
	// Fullscreen growth unpins: the same logical point is on-view again.
	SetPointerClampViewport(2560, 1440)
	if !DispatchRobloxDirectScroll(fx, fy, 0, 1) {
		t.Fatal("grown wheel did not dispatch")
	}
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 840 || y != 640 {
		t.Fatalf("grown wheel point=(%v,%v), want (840,640) preserved", x, y)
	}
}

func TestPointerClampViewportIgnoresNonPositive(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	t.Cleanup(ResetPointerClampViewportForTest)

	SetPointerClampViewport(0, -5)
	if !DispatchRobloxDirectScroll(5000, 5000, 0, 1) {
		t.Fatal("wheel did not dispatch")
	}
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 1279 || y != 719 {
		t.Fatalf("default wheel point=(%v,%v), want (1279,719)", x, y)
	}
}

// fakeZoomClock pins the zoom-lock heuristic's clock for a test.
func fakeZoomClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	old := zoomLockNow
	zoomLockNow = func() time.Time { return now }
	t.Cleanup(func() { zoomLockNow = old })
	return &now
}

func zoomDetent(dir float32) {
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: dir})
}

func lookRelative(dx, dy float32) {
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Relative: true, X: 100, Y: 200, DeltaX: dx, DeltaY: dy})
}

func moveAbsolute(x, y float32) {
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: x, Y: y})
}

func TestZoomLockArmsOnDeliberateZoomInAndCentersTheUnlock(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	fakeZoomClock(t)

	// Default zoom policy: a zoomed-out pointer is free. Absolute motion in
	// the experience is delivered as-is and takes no grab.
	moveAbsolute(100, 200)
	moveAbsolute(111, 193)
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 111 || y != 193 {
		t.Fatalf("free motion=(%v,%v), want absolute (111,193)", x, y)
	}
	if calls.grabs() != 0 || persistentPointerCapture.Load() {
		t.Fatalf("free pointer took a grab: %+v", calls)
	}

	// Two zoom-in notches are casual third-person zoom: delivered at the
	// pointer, no extra move, not armed, still free.
	movesBefore := RobloxDirectInputStats().MoveDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 111, Y: 193, ScrollY: 1})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 111, Y: 193, ScrollY: 1})
	if x, y, d := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1), testDirectRecWheelFloat(2); x != 111 || y != 193 || d != 1 {
		t.Fatalf("casual zoom-in wheel=(%v,%v,%v), want (111,193,1)", x, y, d)
	}
	if zoomLockArmed() || calls.grabs() != 0 {
		t.Fatalf("two zoom-in detents armed or grabbed: armed=%t calls=%+v", zoomLockArmed(), calls)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 0 {
		t.Fatalf("casual zoom-in produced %d moves, want 0", got)
	}

	// The third notch arms: the centered non-sticky grab is taken on the
	// detent itself, the logical pair seeds at the center, a zero-delta move
	// gives the engine that origin, then the detent is delivered there.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 111, Y: 193, ScrollY: 1})
	if !zoomLockArmed() {
		t.Fatal("third zoom-in detent did not arm")
	}
	if len(calls.zoom) != 1 || !calls.zoom[0] || len(calls.cursor) != 0 || len(calls.center) != 0 {
		t.Fatalf("arming grab calls=%+v, want exactly one SetPointerLockAtCenter(true)", calls)
	}
	if !persistentPointerCapture.Load() {
		t.Fatal("arming did not publish persistent capture")
	}
	if n := len(calls.visible); n == 0 || calls.visible[n-1] {
		t.Fatalf("arming cursor calls=%v, want hidden last", calls.visible)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 1 {
		t.Fatalf("arming produced %d moves, want exactly 1 zero-delta move", got)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != 0 || dy != 0 {
		t.Fatalf("arming move=(%v,%v,%v,%v), want (640,360,0,0)", x, y, dx, dy)
	}
	if x, y, d := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1), testDirectRecWheelFloat(2); x != 640 || y != 360 || d != 1 {
		t.Fatalf("arming wheel=(%v,%v,%v), want (640,360,1)", x, y, d)
	}

	// Motion between detents still integrates exact dx/dy from the center;
	// nothing is pinned and the lock stays armed.
	lookRelative(5, -3)
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 645 || y != 357 || dx != 5 || dy != -3 {
		t.Fatalf("armed motion=(%v,%v,%v,%v), want (645,357,5,-3)", x, y, dx, dy)
	}
	if !zoomLockArmed() {
		t.Fatal("motion between zoom-in detents disarmed")
	}
	// Every further zoom-in notch re-centers again (the notch that actually
	// enters first person is unobservable, so all of them carry the origin).
	zoomDetent(1)
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 640 || y != 360 {
		t.Fatalf("armed zoom-in wheel=(%v,%v), want (640,360)", x, y)
	}

	// First-person look: unbounded travel, far off-view.
	for i := 0; i < 3; i++ {
		lookRelative(300, 0)
	}
	if fx, fy, _ := RobloxDirectFallbackPosition(); fx != 1540 || fy != 360 {
		t.Fatalf("look logical=(%v,%v), want unbounded (1540,360)", fx, fy)
	}

	// Zoom-out while armed: the detent that unlocks is delivered at the
	// center and the integrator returns there, but no move is sent (the
	// engine may still be locked for this detent).
	movesBefore = RobloxDirectInputStats().MoveDelivered
	zoomDetent(-1)
	if x, y, d := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1), testDirectRecWheelFloat(2); x != 640 || y != 360 || d != -1 {
		t.Fatalf("unlock wheel=(%v,%v,%v), want (640,360,-1)", x, y, d)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 0 {
		t.Fatalf("unlock detent produced %d moves, want 0", got)
	}
	if !zoomLockArmed() {
		t.Fatal("unlock detent disarmed before the first motion")
	}
	// A second zoom-out notch (games whose first notch stays in first
	// person) re-centers again.
	zoomDetent(-1)
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 640 || y != 360 {
		t.Fatalf("second unlock wheel=(%v,%v), want (640,360)", x, y)
	}

	if !persistentPointerCapture.Load() || len(calls.zoom) != 1 {
		t.Fatalf("unlock detents changed the grab early: persistent=%t calls=%+v", persistentPointerCapture.Load(), calls)
	}

	// The first motion after unlocking disarms and frees the pointer: the
	// grab is released (X11 leaves the pointer at its center anchor), the
	// transition sample is consumed, and the ordinary dispatcher keeps the
	// center as its last origin.
	movesBefore = RobloxDirectInputStats().MoveDelivered
	lookRelative(2, 3)
	if zoomLockArmed() || persistentPointerCapture.Load() {
		t.Fatal("post-unlock motion did not disarm and free the pointer")
	}
	if len(calls.zoom) != 2 || calls.zoom[1] {
		t.Fatalf("release grab calls=%+v, want SetPointerLockAtCenter(false)", calls)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 0 {
		t.Fatalf("transition sample delivered %d moves, want 0", got)
	}
	if lx, ly, ok := robloxDirectLastPosition(); !ok || lx != 640 || ly != 360 {
		t.Fatalf("last origin after release=(%v,%v,%v), want center (640,360,true)", lx, ly, ok)
	}
	// Free absolute motion from the center is continuous with the engine
	// cursor that reappeared there: exact position, exact delta, no teleport.
	moveAbsolute(642, 363)
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 642 || y != 363 || dx != 2 || dy != 3 {
		t.Fatalf("post-release motion=(%v,%v,%v,%v), want (642,363,2,3)", x, y, dx, dy)
	}
	// Ordinary third-person zoom afterwards follows the free pointer and
	// takes no grab.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 642, Y: 363, ScrollY: -1})
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 642 || y != 363 {
		t.Fatalf("free wheel=(%v,%v), want pointer (642,363)", x, y)
	}
	if calls.grabs() != 2 || persistentPointerCapture.Load() {
		t.Fatalf("free zoom-out grabbed again: calls=%+v", calls)
	}
}

func TestZoomPolicyReacquiresArmedGrabAfterFocusFlapAndFreesOnUnlock(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	fakeZoomClock(t)

	moveAbsolute(100, 200)
	for i := 0; i < zoomLockArmDetents; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: 1})
	}
	if !zoomLockArmed() || !persistentPointerCapture.Load() || len(calls.zoom) != 1 {
		t.Fatalf("arming state: armed=%t persistent=%t calls=%+v", zoomLockArmed(), persistentPointerCapture.Load(), calls)
	}

	// Alt-Tab away mid-first-person: X11 drops the grab, the stream clears,
	// the lock stays armed.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	if persistentPointerCapture.Load() || !zoomLockArmed() {
		t.Fatalf("focus loss: persistent=%t armed=%t, want false/true", persistentPointerCapture.Load(), zoomLockArmed())
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	// The first absolute motion back re-acquires the centered grab (not the
	// cursor-anchored one) and is consumed as the transition.
	movesBefore := RobloxDirectInputStats().MoveDelivered
	moveAbsolute(300, 300)
	if !persistentPointerCapture.Load() || len(calls.zoom) != 2 || !calls.zoom[1] || len(calls.cursor) != 0 {
		t.Fatalf("re-acquire calls=%+v persistent=%t, want a second SetPointerLockAtCenter(true)", calls, persistentPointerCapture.Load())
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 0 {
		t.Fatalf("re-acquire transition delivered %d moves, want 0", got)
	}
	if fx, fy, ok := RobloxDirectFallbackPosition(); !ok || fx != 640 || fy != 360 {
		t.Fatalf("re-acquired logical=(%v,%v,%v), want center", fx, fy, ok)
	}
	lookRelative(7, -2)
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 647 || y != 358 {
		t.Fatalf("re-acquired motion=(%v,%v), want (647,358)", x, y)
	}

	// Zoom out and move: free again.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: -1})
	lookRelative(1, 1)
	if persistentPointerCapture.Load() || zoomLockArmed() || len(calls.zoom) != 3 || calls.zoom[2] {
		t.Fatalf("unlock: persistent=%t armed=%t calls=%+v", persistentPointerCapture.Load(), zoomLockArmed(), calls)
	}
	// Free again: a later focus flap and motion take no grab.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	moveAbsolute(500, 400)
	if persistentPointerCapture.Load() || calls.grabs() != 3 {
		t.Fatalf("free pointer grabbed after focus flap: calls=%+v", calls)
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 500 || y != 400 {
		t.Fatalf("free motion=(%v,%v), want absolute (500,400)", x, y)
	}
}

func TestZoomPolicyFreePointerKeepsHeldRMBFallbackInExperience(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	fakeZoomClock(t)

	moveAbsolute(400, 300)
	// Zoomed-out RMB camera look: the old held-RMB cursor-anchored fallback,
	// not the zoom grab. Release lands at the click and frees the pointer.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 400, Y: 300})
	if len(calls.cursor) != 1 || !calls.cursor[0] || len(calls.zoom) != 0 {
		t.Fatalf("RMB grab calls=%+v, want one cursor-anchored grab", calls)
	}
	if !rmbPointerFallback.Load() || persistentPointerCapture.Load() {
		t.Fatal("RMB fallback state wrong in a free experience stream")
	}
	lookRelative(-9, 4)
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 391 || y != 304 || dx != -9 || dy != 4 {
		t.Fatalf("RMB drag=(%v,%v,%v,%v), want (391,304,-9,4)", x, y, dx, dy)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 400, Y: 300})
	// The held-RMB path releases through the generic unlock seam, as before.
	if len(calls.center) != 1 || calls.center[0] || len(calls.cursor) != 1 || len(calls.zoom) != 0 {
		t.Fatalf("RMB release calls=%+v, want the held-RMB grab released once", calls)
	}
	if rmbPointerFallback.Load() || persistentPointerCapture.Load() || zoomLockArmed() {
		t.Fatal("RMB release left a captured or armed state")
	}
	moveAbsolute(405, 302)
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 405 || y != 302 {
		t.Fatalf("post-RMB motion=(%v,%v), want absolute (405,302)", x, y)
	}
	if calls.grabs() != 2 {
		t.Fatalf("post-RMB motion grabbed: calls=%+v", calls)
	}
}

func TestZoomLockRunNeedsDeliberateDetentsWithinWindow(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	now := fakeZoomClock(t)

	moveAbsolute(100, 200)
	moveAbsolute(111, 193)
	wheelAt := func(dir float32) {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 111, Y: 193, ScrollY: dir})
	}

	// Two notches, a pause longer than the window, one more notch: the run
	// restarted, so a slow drift of adjustments never arms or grabs.
	wheelAt(1)
	wheelAt(1)
	*now = now.Add(zoomLockRunWindow + time.Second)
	wheelAt(1)
	if zoomLockArmed() {
		t.Fatal("stale detents counted toward arming")
	}
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 111 || y != 193 {
		t.Fatalf("unarmed wheel=(%v,%v), want pointer (111,193)", x, y)
	}
	// A zoom-out resets the run: in+in+out+in+in is not a deliberate spin.
	wheelAt(1)
	wheelAt(-1)
	wheelAt(1)
	wheelAt(1)
	if zoomLockArmed() {
		t.Fatal("zoom-out did not reset the zoom-in run")
	}
	// Ordinary third-person zooming never moves the cursor or confines the
	// pointer.
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 111 || y != 193 {
		t.Fatalf("third-person wheel=(%v,%v), want pointer (111,193)", x, y)
	}
	if calls.grabs() != 0 || persistentPointerCapture.Load() {
		t.Fatalf("third-person zooming grabbed: calls=%+v", calls)
	}
	if _, _, ok := RobloxDirectFallbackPosition(); ok {
		t.Fatal("free pointer has a captured integrator")
	}
}

func TestZoomLockUnarmedZoomOutOffViewReappearsAtCenter(t *testing.T) {
	// Policy-agnostic integrator path, pinned under the always policy where
	// an unarmed captured stream can drift off-view.
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	fakeZoomClock(t)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	lookRelative(1220, 567) // logical (1320,767): off-view
	movesBefore := RobloxDirectInputStats().MoveDelivered

	// Unarmed zoom-in off-view: the point pins to the edge, the integrator
	// is untouched (motion back inward re-enters at that edge).
	zoomDetent(1)
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 1279 || y != 719 {
		t.Fatalf("off-view zoom-in wheel=(%v,%v), want pinned (1279,719)", x, y)
	}
	if fx, fy, _ := RobloxDirectFallbackPosition(); fx != 1320 || fy != 767 {
		t.Fatalf("integrator=(%v,%v), want untouched (1320,767)", fx, fy)
	}
	// Unarmed zoom-out off-view: nothing visible can jump, so the cursor
	// reappears at the center instead of a clamped edge, silently (no move).
	zoomDetent(-1)
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 640 || y != 360 {
		t.Fatalf("off-view zoom-out wheel=(%v,%v), want center (640,360)", x, y)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 0 {
		t.Fatalf("off-view zoom-out produced %d moves, want 0", got)
	}
	if zoomLockArmed() {
		t.Fatal("off-view recenter armed the lock")
	}
	lookRelative(-4, 6)
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 636 || y != 366 || dx != -4 || dy != 6 {
		t.Fatalf("post-recenter motion=(%v,%v,%v,%v), want (636,366,-4,6)", x, y, dx, dy)
	}
}

func TestZoomLockClearsWhenCaptureDrops(t *testing.T) {
	selectPointerPath(t, "direct")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	fakeZoomClock(t)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	lookRelative(11, -7)
	for i := 0; i < zoomLockArmDetents; i++ {
		zoomDetent(1)
	}
	if !zoomLockArmed() {
		t.Fatal("deliberate spin did not arm")
	}
	// Operator Alt release: the next relative sample drops the stream and the
	// heuristic with it; a later stream starts unarmed.
	pointerCaptureReleased.Store(true)
	lookRelative(1, 1)
	if persistentPointerCapture.Load() {
		t.Fatal("released stream stayed persistent")
	}
	if zoomLockArmed() {
		t.Fatal("dropped stream left the zoom lock armed")
	}
	// Getter-true conversion hands centering to the engine: unarmed too.
	pointerCaptureReleased.Store(false)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 100, Y: 200})
	lookRelative(11, -7)
	for i := 0; i < zoomLockArmDetents; i++ {
		zoomDetent(1)
	}
	if !zoomLockArmed() || !persistentPointerCapture.Load() {
		t.Fatal("second stream did not re-arm under persistent capture")
	}
	testDirectRecSetMouseLocked(true)
	zoomDetent(1)
	if persistentPointerCapture.Load() || !pointerLockSticky.Load() {
		t.Fatal("getter-true detent did not convert to centered sticky")
	}
	if zoomLockArmed() {
		t.Fatal("centered sticky conversion left the zoom lock armed")
	}
}
