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
	if !SetRobloxDirectInputTarget(env, class, testDirectRecordButtonFn(), testDirectRecordMoveFn()) {
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
