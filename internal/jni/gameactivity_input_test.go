// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

// The tests drive the production dispatch and delivery paths directly;
// cgo is unsupported in this package's test files, so the fake engine
// natives are the recording C functions exposed via test hooks in
// gameactivity_input.go.

func TestDispatchInputMotionEventGetters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newMotionEventLocked(motionActionDown, 3.5, 7.25, 100, 250)
	objID := o.id
	vm.mu.Unlock()

	probe := func(t *testing.T, name, sig string, intArg int32) uintptr {
		t.Helper()
		v, handled := testEventGetter(vm, objID, motionEventClass, name, sig, intArg)
		if !handled {
			t.Fatalf("%s%s not handled", name, sig)
		}
		return v
	}

	if got := probe(t, "getAction", "()I", -1); got != uintptr(uint32(motionActionDown)) {
		t.Fatalf("getAction = %d, want %d", got, motionActionDown)
	}
	if got := probe(t, "getPointerCount", "()I", -1); got != 1 {
		t.Fatalf("getPointerCount = %d, want 1", got)
	}
	if got := probe(t, "getPointerId", "(I)I", 0); got != 0 {
		t.Fatalf("getPointerId(0) = %d, want 0", got)
	}
	if got := int32(probe(t, "getPointerId", "(I)I", 1)); got != -1 {
		t.Fatalf("getPointerId(1) = %d, want -1", got)
	}
	// The MotionEvent carries the selected device identity (see
	// pointerSource/pointerTool): touch (default) or the A/B mouse gate.
	if got := int32(probe(t, "getSource", "()I", -1)); got != pointerSource() {
		t.Fatalf("getSource = %#x, want %#x", got, pointerSource())
	}
	if got := probe(t, "getToolType", "(I)I", 0); got != uintptr(uint32(pointerTool())) {
		t.Fatalf("getToolType(0) = %d, want %d", got, pointerTool())
	}
	// Android order: getAxisValue(axis, pointerIndex) — axis first.
	// x=3.5 and y=7.25 are distinct on purpose: if the axis slot were
	// misread, AXIS_Y would return the X value and fail this assertion.
	{
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", motionAxisX, 0)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		if got := math.Float32frombits(uint32(v)); got != 3.5 {
			t.Fatalf("getAxisValue(AXIS_X,0) = %v, want 3.5", got)
		}
	}
	{
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", motionAxisY, 0)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		if got := math.Float32frombits(uint32(v)); got != 7.25 {
			t.Fatalf("getAxisValue(AXIS_Y,0) = %v, want 7.25", got)
		}
	}
	if got := int64(probe(t, "getEventTime", "()J", -1)); got != 250 {
		t.Fatalf("getEventTime = %d, want 250", got)
	}
	if got := int64(probe(t, "getDownTime", "()J", -1)); got != 100 {
		t.Fatalf("getDownTime = %d, want 100", got)
	}
	if got := probe(t, "getHistorySize", "()I", -1); got != 0 {
		t.Fatalf("getHistorySize = %d, want 0", got)
	}
}

// TestGetAxisValueAndroidArgumentOrder pins the Android MotionEvent
// argument order — getAxisValue(axis, pointerIndex): the AXIS request is
// args[0], the pointer index args[1]. Regression for the swapped-slot bug
// that answered every AXIS_Y read with the X coordinate (single-pointer
// events carry pointerIndex 0, which matched AXIS_X under the swapped
// reading). x=3.5 and y=7.25 are distinct so the X/Y mix-up cannot pass.
func TestGetAxisValueAndroidArgumentOrder(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newMotionEventLocked(motionActionDown, 3.5, 7.25, 100, 250)
	objID := o.id
	vm.mu.Unlock()

	axis := func(t *testing.T, a, b int32) float32 {
		t.Helper()
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", a, b)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		return math.Float32frombits(uint32(v))
	}

	if x, y := float32(3.5), float32(7.25); x == y {
		t.Fatalf("test fixture degenerate: x == y == %v", x)
	}
	if got := axis(t, motionAxisX, 0); got != 3.5 {
		t.Fatalf("getAxisValue(AXIS_X, pointer 0) = %v, want 3.5", got)
	}
	if got := axis(t, motionAxisY, 0); got != 7.25 {
		t.Fatalf("getAxisValue(AXIS_Y, pointer 0) = %v, want 7.25 (Android: args[0] is the axis)", got)
	}
	// The pointer index is the second slot and is ignored for this
	// single-pointer event: it must never be read as an axis (old swapped
	// reading turned AXIS_Y+0 into "axis 0" = X for both calls).
	if got := axis(t, motionAxisX, 1); got != 3.5 {
		t.Fatalf("getAxisValue(AXIS_X, pointer 1) = %v, want 3.5 (pointer index is not the axis)", got)
	}
	// Unknown axis answers 0, per Android.
	if got := axis(t, 2, 0); got != 0 {
		t.Fatalf("getAxisValue(axis 2, pointer 0) = %v, want 0", got)
	}

	// getHistoricalAxisValue(axis, pointerIndex, pos) — same axis-first
	// order on the three-int form.
	hist := func(t *testing.T, a, b, c int32) float32 {
		t.Helper()
		v, handled := testEventGetterIII(vm, objID, motionEventClass, "getHistoricalAxisValue", "(III)F", a, b, c)
		if !handled {
			t.Fatal("getHistoricalAxisValue(III)F not handled")
		}
		return math.Float32frombits(uint32(v))
	}
	if got := hist(t, motionAxisX, 0, 0); got != 3.5 {
		t.Fatalf("getHistoricalAxisValue(AXIS_X, 0, 0) = %v, want 3.5", got)
	}
	if got := hist(t, motionAxisY, 0, 0); got != 7.25 {
		t.Fatalf("getHistoricalAxisValue(AXIS_Y, 0, 0) = %v, want 7.25 (axis is args[0])", got)
	}
	if got := hist(t, 2, 0, 0); got != 0 {
		t.Fatalf("getHistoricalAxisValue(axis 2, 0, 0) = %v, want 0", got)
	}
}

// TestX11BridgeKeepsXYDistinct drives the production x11→GameActivity
// handler with distinct coordinates and verifies the delivered MotionEvent
// answers getAxisValue(axis, pointerIndex) with X and Y unmixed end to end.
// Regression seam for the axis-slot bug class: the getter slot itself is
// pinned by TestGetAxisValueAndroidArgumentOrder; this pins the bridge hop
// in front of it (ev.X→x, ev.Y→y must not swap) and the unmapped-key guard.
func TestX11BridgeKeepsXYDistinct(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	// InputStats are package-global and shared across tests: assert deltas.
	consumedBefore := InputDeliveryStats().PointerConsumed

	const cx, cy = float32(640), float32(383)
	handleX11InputEvent(x11.InputEvent{
		Kind:          x11.InputPointer,
		PointerAction: x11.PointerDown,
		Button:        1,
		X:             cx,
		Y:             cy,
	})
	objID := int64(testRecTouchID(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	if o.fields["action"] != int32(x11.PointerDown) {
		t.Fatalf("bridged action = %v, want %d", o.fields["action"], x11.PointerDown)
	}
	if o.fields["x"] != cx || o.fields["y"] != cy {
		t.Fatalf("bridged position = x=%v y=%v, want x=%v y=%v", o.fields["x"], o.fields["y"], cx, cy)
	}
	axis := func(t *testing.T, a, b int32) float32 {
		t.Helper()
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", a, b)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		return math.Float32frombits(uint32(v))
	}
	if got := axis(t, motionAxisX, 0); got != cx {
		t.Fatalf("bridged getAxisValue(AXIS_X,0) = %v, want %v", got, cx)
	}
	if got := axis(t, motionAxisY, 0); got != cy {
		t.Fatalf("bridged getAxisValue(AXIS_Y,0) = %v, want %v (X and Y must stay distinct through the bridge)", got, cy)
	}
	if got := InputDeliveryStats().PointerConsumed; got != consumedBefore+1 {
		t.Fatalf("PointerConsumed delta = %d, want 1", got-consumedBefore)
	}

	// An unmapped key (keycode 0) must be dropped by the bridge guard,
	// never dispatched as an invalid key event.
	before := InputDeliveryStats().KeyDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyCode: 0, KeyPressed: true})
	if got := InputDeliveryStats().KeyDelivered; got != before {
		t.Fatalf("unmapped key dispatched through the bridge")
	}
}

func TestDispatchInputKeyEventGetters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newKeyEventLocked(111, true, 50, 51, 9)
	objID := o.id
	vm.mu.Unlock()

	probe := func(t *testing.T, name, sig string, intArg int32) uintptr {
		t.Helper()
		v, handled := testEventGetter(vm, objID, keyEventClass, name, sig, intArg)
		if !handled {
			t.Fatalf("%s%s not handled", name, sig)
		}
		return v
	}

	if got := probe(t, "getAction", "()I", -1); got != uintptr(uint32(keyEventActionDown)) {
		t.Fatalf("getAction = %d, want %d", got, keyEventActionDown)
	}
	if got := probe(t, "getKeyCode", "()I", -1); got != 111 {
		t.Fatalf("getKeyCode = %d, want 111", got)
	}
	if got := probe(t, "getScanCode", "()I", -1); got != 9 {
		t.Fatalf("getScanCode = %d, want 9", got)
	}
	if got := probe(t, "getUnicodeChar", "()I", -1); got != 0 {
		t.Fatalf("getUnicodeChar = %d, want 0 (no text mapping this pass)", got)
	}
	if got := probe(t, "getRepeatCount", "()I", -1); got != 0 {
		t.Fatalf("getRepeatCount = %d, want 0", got)
	}
	if got := probe(t, "getSource", "()I", -1); got != uintptr(uint32(sourceKeyboard)) {
		t.Fatalf("getSource = %#x, want %#x", got, sourceKeyboard)
	}
}

func TestDispatchInputOnlyEventClasses(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	cls := vm.classes["java/io/File"]
	o := vm.newObjectLocked(cls)
	objID := o.id
	vm.mu.Unlock()

	if _, handled := testEventGetter(vm, objID, "java/io/File", "getAction", "()I", -1); handled {
		t.Fatal("non-event class handled by input dispatch")
	}
}

func inputTestVM(t *testing.T, natives map[string]uintptr) *VM {
	t.Helper()
	testRecReset()
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	for k, v := range natives {
		vm.natives[k] = v
	}
	vm.mu.Unlock()
	t.Cleanup(ClearGameActivityInputTarget)
	return vm
}

func TestDispatchGameActivityFocus(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onWindowFocusChangedNative", "(JZ)V"): testRecordFocusFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)

	if !DispatchGameActivityFocus(true) {
		t.Fatal("focus dispatch failed")
	}
	if got := testRec4(0); got != vm.Env().Raw() {
		t.Fatalf("recorded env = %#x, want %#x", got, vm.Env().Raw())
	}
	if got := testRec4(1); got != 42 {
		t.Fatalf("recorded activity = %d, want 42", got)
	}
	if got := testRec4(2); got != 77 {
		t.Fatalf("recorded handle = %d, want 77", got)
	}
	if got := testRec4(3); got != 1 {
		t.Fatalf("recorded gained = %d, want 1", got)
	}
	if got := InputDeliveryStats().FocusDelivered; got != 1 {
		t.Fatalf("FocusDelivered = %d, want 1", got)
	}
}

func TestDispatchGameActivityKey(t *testing.T) {
	// The engine registers onKeyDownNative/onKeyUpNative with a leading J
	// handle: (JLandroid/view/KeyEvent;)Z (RegisterNatives batch, launch
	// logs). Dispatch must resolve exactly that identity and pass
	// (env, activity, handle, keyEvent) in that order.
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"): testRecordKeyFn(),
		methodLogName(gameActivityClass, "onKeyUpNative", "(JLandroid/view/KeyEvent;)Z"):   testRecordKeyFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	consumedBefore := InputDeliveryStats().KeyConsumed

	if !DispatchGameActivityKey(111, 9, true) {
		t.Fatal("key down dispatch failed (engine verdict false)")
	}
	if got := testRec4(2); got != 77 {
		t.Fatalf("recorded leading J (handle) = %d, want 77", got)
	}
	objID := int64(testRec4(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != keyEventClass {
		t.Fatalf("delivered object = %+v, want KeyEvent", o)
	}
	if o.fields["keyCode"] != int32(111) || o.fields["scanCode"] != int32(9) || o.fields["action"] != keyEventActionDown {
		t.Fatalf("KeyEvent fields = %+v", o.fields)
	}
	if got := InputDeliveryStats().KeyConsumed - consumedBefore; got != 1 {
		t.Fatalf("KeyConsumed delta = %d, want 1", got)
	}

	if !DispatchGameActivityKey(111, 9, false) {
		t.Fatal("key up dispatch failed")
	}
	o2 := vm.get(int64(testRec4(3)))
	if o2 == nil || o2.fields["action"] != keyEventActionUp {
		t.Fatalf("key up event fields = %+v", o2.fields)
	}
	if down, _ := o.fields["downTime"].(int64); o2.fields["downTime"] != down {
		t.Fatalf("key UP downTime = %v, want the matching DOWN's %d", o2.fields["downTime"], down)
	}
}

func TestDispatchGameActivityPointerMarshaling(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	// InputStats are package-global and shared across tests: assert deltas.
	consumedBefore := InputDeliveryStats().PointerConsumed

	if !DispatchGameActivityPointer(motionActionDown, 3.5, 7.25, 1) {
		t.Fatal("pointer dispatch failed")
	}
	// Proven packing (APK DEX, GameActivity.N1):
	// i1..i5 = pointerCount, historySize, deviceId, source, action.
	if got := testRecTouchInt(0); got != 1 {
		t.Fatalf("primitive i1 (pointerCount) = %d, want 1", got)
	}
	if got := testRecTouchInt(1); got != 0 {
		t.Fatalf("primitive i2 (historySize) = %d, want 0", got)
	}
	if got := testRecTouchInt(2); got != 0 {
		t.Fatalf("primitive i3 (deviceId) = %d, want 0", got)
	}
	if got := testRecTouchInt(3); got != uintptr(uint32(pointerSource())) {
		t.Fatalf("primitive i4 (source) = %#x, want %#x", got, pointerSource())
	}
	if got := testRecTouchInt(4); got != uintptr(uint32(motionActionDown)) {
		t.Fatalf("primitive i5 (action) = %d, want %d", got, motionActionDown)
	}
	// j1, j2 = eventTime, downTime (DEX order — eventTime first).
	if got := testRecTouchLong(0); got < 0 || got != testRecTouchLong(1) {
		t.Fatalf("primitive j1/j2 (eventTime/downTime) = %d/%d, want equal monotonic millis for DOWN", testRecTouchLong(0), testRecTouchLong(1))
	}
	// k1..k6 = flags, metaState, actionButton, buttonState, classification, edgeFlags.
	// Touch mode: buttonState is 0 (Android touchscreen events carry no
	// mouse buttons) regardless of the caller argument; mouse mode keeps
	// the derived BUTTON_PRIMARY press state.
	wantButton := int32(0)
	if !pointerDeviceIsTouch() {
		wantButton = 1
	}
	if got := int32(testRecTouchInt(8)); got != wantButton {
		t.Fatalf("primitive k4 (buttonState) = %d, want %d", got, wantButton)
	}
	if got := testRecTouchFloat(0); got != 1 {
		t.Fatalf("primitive f1 (xPrecision) = %v, want 1", got)
	}
	objID := int64(testRecTouchID(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	if o.fields["x"] != float32(3.5) || o.fields["y"] != float32(7.25) {
		t.Fatalf("MotionEvent position = %+v", o.fields)
	}
	if got := InputDeliveryStats().PointerConsumed; got != consumedBefore+1 {
		t.Fatalf("PointerConsumed delta = %d, want 1", got-consumedBefore)
	}
}

func TestInputDroppedWithoutTarget(t *testing.T) {
	ClearGameActivityInputTarget()
	testRecReset()
	// Target stays unwired: everything drops, nothing is queued or sent.
	before := InputDeliveryStats().Dropped
	if DispatchGameActivityFocus(true) {
		t.Fatal("focus delivered without target")
	}
	if DispatchGameActivityKey(111, 9, true) {
		t.Fatal("key delivered without target")
	}
	if DispatchGameActivityPointer(motionActionDown, 0, 0, 1) {
		t.Fatal("pointer delivered without target")
	}
	after := InputDeliveryStats().Dropped
	if after != before+3 {
		t.Fatalf("Dropped = %d, want %d", after, before+3)
	}
	if testRec4(2) != 0 {
		t.Fatal("recording native invoked without target")
	}
}

// lastTouch reads the (downTime, eventTime, action) of the most recently
// delivered MotionEvent object.
func lastTouch(t *testing.T, vm *VM) (down, event int64, action int32) {
	t.Helper()
	o := vm.get(int64(testRecTouchID(3)))
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	down, _ = o.fields["downTime"].(int64)
	event, _ = o.fields["eventTime"].(int64)
	action, _ = o.fields["action"].(int32)
	return down, event, action
}

// TestPointerGestureDownTimeSemantics proves the Android MotionEvent
// gesture contract on the delivered events: the MOVEs and the ACTION_UP of
// one gesture carry the initiating ACTION_DOWN's downTime, eventTime
// advances, the primitive j1/j2 slots mirror the object, and a new gesture
// starts a fresh downTime.
func TestPointerGestureDownTimeSemantics(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)

	if !DispatchGameActivityPointer(motionActionDown, 10, 10, 1) {
		t.Fatal("pointer down dispatch failed")
	}
	downDown, downAt, act := lastTouch(t, vm)
	if act != motionActionDown || downDown != downAt {
		t.Fatalf("DOWN times = (%d, %d) action=%d, want downTime==eventTime", downDown, downAt, act)
	}

	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionMove, 20, 25, 1) {
		t.Fatal("pointer move dispatch failed")
	}
	moveDown, moveAt, act := lastTouch(t, vm)
	if act != motionActionMove {
		t.Fatalf("action = %d, want MOVE", act)
	}
	if moveDown != downDown {
		t.Fatalf("MOVE downTime = %d, want the DOWN's %d", moveDown, downDown)
	}
	if moveAt <= moveDown {
		t.Fatalf("MOVE eventTime = %d, want > downTime %d", moveAt, moveDown)
	}
	// j1 = eventTime, j2 = downTime (DEX order) on a non-DOWN event.
	if got := testRecTouchLong(0); got != moveAt {
		t.Fatalf("primitive j1 (eventTime) = %d, want %d", got, moveAt)
	}
	if got := testRecTouchLong(1); got != moveDown {
		t.Fatalf("primitive j2 (downTime) = %d, want %d", got, moveDown)
	}

	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionUp, 20, 25, 0) {
		t.Fatal("pointer up dispatch failed")
	}
	upDown, upAt, act := lastTouch(t, vm)
	if act != motionActionUp {
		t.Fatalf("action = %d, want UP", act)
	}
	if upDown != downDown {
		t.Fatalf("UP downTime = %d, want the matching DOWN's %d (Android gesture contract)", upDown, downDown)
	}
	if upAt <= moveAt {
		t.Fatalf("UP eventTime = %d, want > MOVE eventTime %d", upAt, moveAt)
	}
	if got := testRecTouchLong(0); got != upAt {
		t.Fatalf("primitive j1 (eventTime) = %d, want %d (must mirror the object)", got, upAt)
	}
	if got := testRecTouchLong(1); got != upDown {
		t.Fatalf("primitive j2 (downTime) = %d, want %d (must mirror the object)", got, upDown)
	}

	// A new gesture starts a fresh downTime, and a stray move with no open
	// gesture self-anchors (downTime == eventTime) instead of inventing one.
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionDown, 5, 5, 1) {
		t.Fatal("second pointer down dispatch failed")
	}
	down2, down2At, act := lastTouch(t, vm)
	if act != motionActionDown || down2 != down2At {
		t.Fatalf("second DOWN times = (%d, %d), want fresh downTime==eventTime", down2, down2At)
	}
	if down2 <= upAt {
		t.Fatalf("second DOWN downTime = %d, want > previous gesture UP time %d", down2, upAt)
	}
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionUp, 5, 5, 0) {
		t.Fatal("second pointer up dispatch failed")
	}
	if !DispatchGameActivityPointer(motionActionMove, 1, 1, 0) {
		t.Fatal("stray pointer move dispatch failed")
	}
	strayDown, strayAt, _ := lastTouch(t, vm)
	if strayDown != strayAt {
		t.Fatalf("stray MOVE times = (%d, %d), want self-anchored downTime==eventTime", strayDown, strayAt)
	}
}

// TestPointerButtonState proves the Android button-state derivation from
// real X11 button press/release pairs: left=BUTTON_PRIMARY(1),
// right=BUTTON_SECONDARY(2), moves report the held set, and the state is
// clear after both releases.
func TestPointerButtonState(t *testing.T) {
	if got := pointerButtonState(1, true); got != 1 {
		t.Fatalf("left down state = %d, want 1", got)
	}
	if got := pointerButtonState(0, false); got != 1 {
		t.Fatalf("move during left drag state = %d, want 1", got)
	}
	if got := pointerButtonState(3, true); got != 3 {
		t.Fatalf("right added state = %d, want 3", got)
	}
	if got := pointerButtonState(1, false); got != 2 {
		t.Fatalf("left released state = %d, want 2", got)
	}
	if got := pointerButtonState(3, false); got != 0 {
		t.Fatalf("right released state = %d, want 0", got)
	}
}

// TestKeyGestureDownTimeSemantics proves the Android KeyEvent contract on
// the delivered events: a key UP carries the matching DOWN's downTime and
// a new press starts a fresh one. The key stream is independent of the
// pointer stream (they must not share gesture state).
func TestKeyGestureDownTimeSemantics(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"):                      testRecordKeyFn(),
		methodLogName(gameActivityClass, "onKeyUpNative", "(JLandroid/view/KeyEvent;)Z"):                        testRecordKeyFn(),
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)

	lastKey := func(t *testing.T) (down, event int64) {
		t.Helper()
		o := vm.get(int64(testRec4(3)))
		if o == nil || o.class == nil || o.class.name != keyEventClass {
			t.Fatalf("delivered object = %+v, want KeyEvent", o)
		}
		down, _ = o.fields["downTime"].(int64)
		event, _ = o.fields["eventTime"].(int64)
		return down, event
	}

	if !DispatchGameActivityKey(111, 9, true) {
		t.Fatal("key down dispatch failed")
	}
	kDown, kAt := lastKey(t)
	if kDown != kAt {
		t.Fatalf("key DOWN times = (%d, %d), want downTime==eventTime", kDown, kAt)
	}

	// An unrelated pointer gesture must not disturb the key gesture clock.
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionDown, 0, 0, 1) || !DispatchGameActivityPointer(motionActionUp, 0, 0, 0) {
		t.Fatal("pointer gesture dispatch failed")
	}
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityKey(111, 9, false) {
		t.Fatal("key up dispatch failed")
	}
	kUpDown, kUpAt := lastKey(t)
	if kUpDown != kDown {
		t.Fatalf("key UP downTime = %d, want the matching DOWN's %d", kUpDown, kDown)
	}
	if kUpAt <= kUpDown {
		t.Fatalf("key UP eventTime = %d, want > downTime %d", kUpAt, kUpDown)
	}

	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityKey(111, 9, true) {
		t.Fatal("second key down dispatch failed")
	}
	kDown2, kAt2 := lastKey(t)
	if kDown2 != kAt2 || kDown2 <= kUpAt {
		t.Fatalf("second key DOWN times = (%d, %d), want fresh downTime > %d", kDown2, kAt2, kUpAt)
	}
}

// TestPointerDeviceTouchIdentity pins the explicit coherent touch identity
// against the public Android constants: SOURCE_TOUCHSCREEN (0x1002) +
// TOOL_TYPE_FINGER (1) on every MotionEvent, with the KeyEvent stream
// untouched (SOURCE_KEYBOARD).
func TestPointerDeviceTouchIdentity(t *testing.T) {
	t.Setenv("TIPSY_INPUT_DEVICE", "touch")
	t.Cleanup(ResetPointerDeviceMode)
	ResetPointerDeviceMode()
	if !PointerDeviceIsTouch() {
		t.Fatal("TIPSY_INPUT_DEVICE=touch did not select the touch identity")
	}
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newMotionEventLocked(motionActionDown, 3.5, 7.25, 100, 250)
	objID := o.id
	k := vm.newKeyEventLocked(111, true, 100, 101, 9)
	keyID := k.id
	vm.mu.Unlock()

	if got, _ := testEventGetter(vm, objID, motionEventClass, "getSource", "()I", -1); int32(uint32(got)) != sourceTouchscreen {
		t.Fatalf("MotionEvent getSource = %#x, want %#x (SOURCE_TOUCHSCREEN)", int32(uint32(got)), sourceTouchscreen)
	}
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getToolType", "(I)I", 0); int32(uint32(got)) != toolTypeFinger {
		t.Fatalf("getToolType(0) = %d, want %d (TOOL_TYPE_FINGER)", int32(uint32(got)), toolTypeFinger)
	}
	if got, _ := testEventGetter(vm, keyID, keyEventClass, "getSource", "()I", -1); int32(uint32(got)) != sourceKeyboard {
		t.Fatalf("KeyEvent getSource = %#x, want %#x (keys are unaffected by the pointer identity)", int32(uint32(got)), sourceKeyboard)
	}
}

// TestTouchModeCoherentDelivery drives the production x11→GameActivity
// bridge in explicit touch mode: a real left DOWN/UP and its drag MOVE
// arrive as SOURCE_TOUCHSCREEN/TOOL_TYPE_FINGER with buttonState 0 (touch
// events carry no mouse buttons) and gesture-paired downTimes; a real
// right-button press has no honest touch identity and is dropped; a MOVE
// without an open gesture is dropped.
func TestTouchModeCoherentDelivery(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	t.Setenv("TIPSY_INPUT_DEVICE", "touch")
	t.Cleanup(ResetPointerDeviceMode)
	ResetPointerDeviceMode()
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	// Close any gesture a previous test left open, so this test starts
	// from a closed gesture stream like a fresh launch.
	DispatchGameActivityPointer(motionActionUp, 0, 0, 0)
	deliveredBefore := InputDeliveryStats().PointerDelivered
	droppedBefore := InputDeliveryStats().Dropped

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 640, Y: 383})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Button: 0, X: 650, Y: 385})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 1, X: 650, Y: 385})
	// Right button: a real X11 event, but not presentable as touch.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 650, Y: 385})
	// Stray MOVE with no open gesture (a right-drag would look like this).
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Button: 0, X: 660, Y: 390})

	if got := InputDeliveryStats().PointerDelivered; got != deliveredBefore+3 {
		t.Fatalf("PointerDelivered delta = %d, want 3 (down, move, up)", got-deliveredBefore)
	}
	if got := InputDeliveryStats().Dropped; got != droppedBefore+2 {
		t.Fatalf("Dropped delta = %d, want 2 (right button + stray move)", got-droppedBefore)
	}
	objID := int64(testRecTouchID(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	if o.fields["action"] != int32(x11.PointerUp) {
		t.Fatalf("last bridged action = %v, want UP", o.fields["action"])
	}
	if o.fields["x"] != float32(650) || o.fields["y"] != float32(385) {
		t.Fatalf("bridged position = %v/%v, want 650/385", o.fields["x"], o.fields["y"])
	}
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getSource", "()I", -1); int32(uint32(got)) != sourceTouchscreen {
		t.Fatalf("bridged getSource = %#x, want %#x", int32(uint32(got)), sourceTouchscreen)
	}
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getToolType", "(I)I", 0); int32(uint32(got)) != toolTypeFinger {
		t.Fatalf("bridged getToolType = %d, want %d", int32(uint32(got)), toolTypeFinger)
	}
	if got := int32(testRecTouchInt(3)); got != sourceTouchscreen {
		t.Fatalf("bridged primitive i4 (source) = %#x, want %#x", got, sourceTouchscreen)
	}
	if got := int32(testRecTouchInt(8)); got != 0 {
		t.Fatalf("bridged primitive k4 (buttonState) = %d, want 0 in touch mode", got)
	}
	// The UP's primitive longs must mirror the delivered object.
	if dt, _ := o.fields["downTime"].(int64); testRecTouchLong(1) != dt {
		t.Fatalf("UP primitive j2 (downTime) = %d, want object %d (must mirror)", testRecTouchLong(1), dt)
	}
}

// TestPointerDeviceMouseIdentity pins the default desktop identity end to end
// — SOURCE_MOUSE (0x2002) +
// TOOL_TYPE_MOUSE (3), right-button delivery, and BUTTON_PRIMARY-derived
// buttonState on the primitives.
func TestPointerDeviceMouseIdentity(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	t.Setenv("TIPSY_INPUT_DEVICE", "")
	t.Cleanup(ResetPointerDeviceMode)
	ResetPointerDeviceMode()
	if PointerDeviceIsTouch() {
		t.Fatal("default device mode is not mouse")
	}
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	DispatchGameActivityPointer(motionActionUp, 0, 0, 0) // close any open gesture
	deliveredBefore := InputDeliveryStats().PointerDelivered

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 640, Y: 383})
	if got := InputDeliveryStats().PointerDelivered; got != deliveredBefore+1 {
		t.Fatalf("PointerDelivered delta = %d, want 1", got-deliveredBefore)
	}
	if got := int32(testRecTouchInt(3)); got != sourceMouse {
		t.Fatalf("primitive i4 (source) = %#x, want %#x", got, sourceMouse)
	}
	if got := int32(testRecTouchInt(8)); got != 1 {
		t.Fatalf("primitive k4 (buttonState) = %d, want 1 (BUTTON_PRIMARY)", got)
	}
	objID := int64(testRecTouchID(3))
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getToolType", "(I)I", 0); int32(uint32(got)) != toolTypeMouse {
		t.Fatalf("getToolType(0) = %d, want %d (TOOL_TYPE_MOUSE)", int32(uint32(got)), toolTypeMouse)
	}
	// Right button is delivered in mouse mode, as before this pass.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 1, Y: 2})
	if got := InputDeliveryStats().PointerDelivered; got != deliveredBefore+2 {
		t.Fatalf("right button dropped in mouse mode: delta = %d, want 2", got-deliveredBefore)
	}
}
