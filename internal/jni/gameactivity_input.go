// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"

// Callers for the GameActivity natives the engine registers
// (RegisterNatives on com/google/androidgamesdk/GameActivity). Signatures
// mirror the registered JNI descriptors exactly; uintptr_t carries
// pointer/int JNI arguments of equal width in the SysV x86-64 ABI.

static void tipsy_input_call_void4(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	((void (*)(uintptr_t, uintptr_t, uintptr_t, uintptr_t))fn)(a0, a1, a2, a3);
}

static unsigned char tipsy_input_call_bool4(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	return ((unsigned char (*)(uintptr_t, uintptr_t, uintptr_t, uintptr_t))fn)(a0, a1, a2, a3);
}

// onTouchEventNative(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z
static unsigned char tipsy_input_call_touch(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3,
	int i1, int i2, int i3, int i4, int i5,
	long long j1, long long j2,
	int k1, int k2, int k3, int k4, int k5, int k6,
	float f1, float f2) {
	return ((unsigned char (*)(uintptr_t, uintptr_t, uintptr_t, uintptr_t,
		int, int, int, int, int,
		long long, long long,
		int, int, int, int, int, int,
		float, float))fn)(a0, a1, a2, a3,
		i1, i2, i3, i4, i5, j1, j2, k1, k2, k3, k4, k5, k6, f1, f2);
}

// Recording natives: test-only fake engine natives that capture arguments
// so unit tests can verify marshaling without loading libroblox.so. Not
// used outside tests; they store and return 1 (consumed).
static uintptr_t tipsy_rec4_args[4];

static void tipsy_input_record_focus(uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	tipsy_rec4_args[0] = a0;
	tipsy_rec4_args[1] = a1;
	tipsy_rec4_args[2] = a2;
	tipsy_rec4_args[3] = a3;
}

static unsigned char tipsy_input_record_key(uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	tipsy_rec4_args[0] = a0;
	tipsy_rec4_args[1] = a1;
	tipsy_rec4_args[2] = a2;
	tipsy_rec4_args[3] = a3;
	return 1;
}

static uintptr_t tipsy_rec_touch_ints[13];
static long long tipsy_rec_touch_longs[2];
static float tipsy_rec_touch_floats[2];
static uintptr_t tipsy_rec_touch_ids[4];

static unsigned char tipsy_input_record_touch(uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3,
	int i1, int i2, int i3, int i4, int i5,
	long long j1, long long j2,
	int k1, int k2, int k3, int k4, int k5, int k6,
	float f1, float f2) {
	tipsy_rec_touch_ids[0] = a0;
	tipsy_rec_touch_ids[1] = a1;
	tipsy_rec_touch_ids[2] = a2;
	tipsy_rec_touch_ids[3] = a3;
	tipsy_rec_touch_ints[0] = (uintptr_t)i1;
	tipsy_rec_touch_ints[1] = (uintptr_t)i2;
	tipsy_rec_touch_ints[2] = (uintptr_t)i3;
	tipsy_rec_touch_ints[3] = (uintptr_t)i4;
	tipsy_rec_touch_ints[4] = (uintptr_t)i5;
	tipsy_rec_touch_ints[5] = (uintptr_t)k1;
	tipsy_rec_touch_ints[6] = (uintptr_t)k2;
	tipsy_rec_touch_ints[7] = (uintptr_t)k3;
	tipsy_rec_touch_ints[8] = (uintptr_t)k4;
	tipsy_rec_touch_ints[9] = (uintptr_t)k5;
	tipsy_rec_touch_ints[10] = (uintptr_t)k6;
	tipsy_rec_touch_longs[0] = j1;
	tipsy_rec_touch_longs[1] = j2;
	tipsy_rec_touch_floats[0] = f1;
	tipsy_rec_touch_floats[1] = f2;
	return 1;
}

static void *tipsy_input_record_focus_fn(void) { return (void *)tipsy_input_record_focus; }
static void *tipsy_input_record_key_fn(void) { return (void *)tipsy_input_record_key; }
static void *tipsy_input_record_touch_fn(void) { return (void *)tipsy_input_record_touch; }
static uintptr_t tipsy_input_rec4_get(int i) { return tipsy_rec4_args[i]; }
static uintptr_t tipsy_input_rec_touch_int(int i) { return tipsy_rec_touch_ints[i]; }
static long long tipsy_input_rec_touch_long(int i) { return tipsy_rec_touch_longs[i]; }
static float tipsy_input_rec_touch_float(int i) { return tipsy_rec_touch_floats[i]; }
static uintptr_t tipsy_input_rec_touch_id(int i) { return tipsy_rec_touch_ids[i]; }
static void tipsy_input_rec_reset(void) {
	tipsy_rec4_args[0] = 0;
	tipsy_rec4_args[1] = 0;
	tipsy_rec4_args[2] = 0;
	tipsy_rec4_args[3] = 0;
	for (int i = 0; i < 13; i++) tipsy_rec_touch_ints[i] = 0;
	tipsy_rec_touch_longs[0] = 0;
	tipsy_rec_touch_longs[1] = 0;
	tipsy_rec_touch_floats[0] = 0;
	tipsy_rec_touch_floats[1] = 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

// gameActivityClass is the class the engine registers its lifecycle and
// input natives on (RegisterNatives, observed in every launch log).
const gameActivityClass = "com/google/androidgamesdk/GameActivity"

const (
	motionEventClass = "android/view/MotionEvent"
	keyEventClass    = "android/view/KeyEvent"
)

// MotionEvent action constants (android.view.MotionEvent).
const (
	motionActionDown        = int32(0)
	motionActionUp          = int32(1)
	motionActionMove        = int32(2)
	motionActionCancel      = int32(3)
	motionActionPointerDown = int32(5)
	motionActionPointerUp   = int32(6)
)

// MotionEvent axis constants (AMOTION_EVENT_AXIS_*).
const (
	motionAxisX = int32(0)
	motionAxisY = int32(1)
)

// Tool and source constants the engine's glue queries (public
// android.view.MotionEvent / InputDevice values).
const (
	toolTypeMouse      = int32(3)      // MOTION_EVENT_TOOL_TYPE_MOUSE
	toolTypeFinger     = int32(1)      // MOTION_EVENT_TOOL_TYPE_FINGER
	sourceMouse        = int32(0x2002) // SOURCE_MOUSE (0x2000 | CLASS_POINTER)
	sourceTouchscreen  = int32(0x1002) // SOURCE_TOUCHSCREEN (0x1000 | CLASS_POINTER)
	sourceKeyboard     = int32(0x301)  // SOURCE_CLASS_BUTTON | SOURCE_KEYBOARD
	keyEventActionDown = int32(0)
	keyEventActionUp   = int32(1)
)

// Pointer device identity, decided once per process from
// TIPSY_INPUT_DEVICE and shared by every input surface (MotionEvent
// source/toolType, touch primitives, and PlatformParams) so the engine
// sees exactly one coherent device, never a mix:
//
//   - mouse (default): the native desktop identity
//     SOURCE_MOUSE (0x2002) + TOOL_TYPE_MOUSE (3) +
//     PlatformParams.isMouseDevice=true.
//   - touch (TIPSY_INPUT_DEVICE=touch): SOURCE_TOUCHSCREEN (0x1002) +
//     TOOL_TYPE_FINGER (1) and PlatformParams.isTouchDevice=true — the
//     official Android phone identity, retained as an explicit A/B control.
var pointerDevice struct {
	sync.Once
	touch bool
}

func pointerDeviceIsTouch() bool {
	pointerDevice.Do(func() {
		pointerDevice.touch = strings.EqualFold(strings.TrimSpace(os.Getenv("TIPSY_INPUT_DEVICE")), "touch")
	})
	return pointerDevice.touch
}

// pointerSource is the MotionEvent source and onTouchEventNative source
// primitive for the selected device identity.
func pointerSource() int32 {
	if pointerDeviceIsTouch() {
		return sourceTouchscreen
	}
	return sourceMouse
}

// pointerTool is the MotionEvent tool type for the selected device
// identity.
func pointerTool() int32 {
	if pointerDeviceIsTouch() {
		return toolTypeFinger
	}
	return toolTypeMouse
}

// PointerDeviceIsTouch reports the pointer device identity Tipsy presents.
// The runtime launcher consults this when building PlatformParams so the
// engine sees one coherent identity across input and platform params.
func PointerDeviceIsTouch() bool { return pointerDeviceIsTouch() }

// ResetPointerDeviceMode discards the memoized device mode so the next
// PointerDeviceIsTouch call re-reads TIPSY_INPUT_DEVICE. Test seam for the
// A/B gate; production code calls it never.
func ResetPointerDeviceMode() { pointerDevice.Once = sync.Once{} }

// newMotionEventLocked builds a single-pointer MotionEvent object whose
// getters (dispatchInput) answer from real X11 pointer data, carrying the
// selected device identity (desktop mouse by default, touch finger behind the
// TIPSY_INPUT_DEVICE=touch gate).
func (vm *VM) newMotionEventLocked(action int32, x, y float32, downTime, eventTime int64) *Object {
	cls := vm.ensureClassLocked(motionEventClass)
	o := vm.newObjectLocked(cls)
	o.fields["action"] = action
	o.fields["deviceId"] = int32(0)
	o.fields["source"] = pointerSource()
	o.fields["edgeFlags"] = int32(0)
	o.fields["flags"] = int32(0)
	o.fields["metaState"] = int32(0)
	o.fields["downTime"] = downTime
	o.fields["eventTime"] = eventTime
	o.fields["pointerCount"] = int32(1)
	o.fields["pointerId0"] = int32(0)
	o.fields["toolType0"] = pointerTool()
	o.fields["x"] = x
	o.fields["y"] = y
	o.fields["xPrecision"] = float32(1)
	o.fields["yPrecision"] = float32(1)
	return o
}

// newKeyEventLocked builds a KeyEvent object for a non-text key.
// scanCode carries the raw X11 keycode (the closest honest hardware code).
func (vm *VM) newKeyEventLocked(keyCode int32, pressed bool, downTime, eventTime int64, scanCode int32) *Object {
	cls := vm.ensureClassLocked(keyEventClass)
	o := vm.newObjectLocked(cls)
	if pressed {
		o.fields["action"] = keyEventActionDown
	} else {
		o.fields["action"] = keyEventActionUp
	}
	o.fields["keyCode"] = keyCode
	o.fields["deviceId"] = int32(0)
	o.fields["source"] = sourceKeyboard
	o.fields["repeatCount"] = int32(0)
	o.fields["metaState"] = int32(0)
	o.fields["flags"] = int32(0)
	o.fields["scanCode"] = scanCode
	o.fields["downTime"] = downTime
	o.fields["eventTime"] = eventTime
	o.fields["unicodeChar"] = int32(0)
	return o
}

// dispatchInput serves the MotionEvent/KeyEvent getter surface the engine's
// glue resolves via GetMethodID during initializeNativeCode (each name in
// this switch is in the observed missing-method list at that call site).
// Everything else falls through to the normal dispatch path.
func (vm *VM) dispatchInput(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if o == nil || (class != motionEventClass && class != keyEventClass) {
		return jnull(), false
	}
	val := func(field string) int64 {
		switch t := o.fields[field].(type) {
		case int32:
			return int64(t)
		case int64:
			return t
		case int:
			return int64(t)
		}
		return 0
	}
	floatVal := func(field string) float32 {
		if f, ok := o.fields[field].(float32); ok {
			return f
		}
		return 0
	}
	intOut := func(v int64) (C.jobject, bool) {
		// Observation-only: count the getter identity native consumed
		// (name+sig, never the value) for the per-event consumption proof.
		noteGetterCall(o.id, name, sig)
		return C.jobject(unsafe.Pointer(uintptr(uint32(v)))), true
	}
	longOut := func(v int64) (C.jobject, bool) {
		noteGetterCall(o.id, name, sig)
		return C.jobject(unsafe.Pointer(uintptr(v))), true
	}
	floatOut := func(v float32) (C.jobject, bool) {
		noteGetterCall(o.id, name, sig)
		return C.jobject(unsafe.Pointer(uintptr(float32bits(v)))), true
	}

	switch name + sig {
	// Shared MotionEvent/KeyEvent getters.
	case "getDeviceId()I":
		return intOut(val("deviceId"))
	case "getSource()I":
		return intOut(val("source"))
	case "getFlags()I":
		return intOut(val("flags"))
	case "getMetaState()I":
		return intOut(val("metaState"))
	case "getDownTime()J":
		return longOut(val("downTime"))
	case "getEventTime()J":
		return longOut(val("eventTime"))

	// MotionEvent getters.
	case "getAction()I":
		return intOut(val("action"))
	case "getActionMasked()I":
		return intOut(val("action") & 0xff)
	case "getActionIndex()I":
		return intOut(0)
	case "getEdgeFlags()I":
		return intOut(val("edgeFlags"))
	case "getHistorySize()I":
		return intOut(0)
	case "getHistoricalEventTime(I)J":
		return longOut(val("eventTime"))
	case "getPointerCount()I":
		return intOut(val("pointerCount"))
	case "getPointerId(I)I":
		if jvalueIAt(args, 0) == 0 {
			return intOut(val("pointerId0"))
		}
		return intOut(-1)
	case "getToolType(I)I":
		if jvalueIAt(args, 0) == 0 {
			return intOut(val("toolType0"))
		}
		return intOut(0)
	case "getXPrecision()F":
		return floatOut(floatVal("xPrecision"))
	case "getYPrecision()F":
		return floatOut(floatVal("yPrecision"))
	case "getX()F", "getX(I)F":
		return floatOut(floatVal("x"))
	case "getY()F", "getY(I)F":
		return floatOut(floatVal("y"))
	case "getAxisValue(II)F":
		// Android order: getAxisValue(int axis, int pointerIndex) —
		// args[0] is the axis (developer.android.com MotionEvent; the
		// public GameActivity glue calls getAxisValue(axisIndex, i)).
		// The single-pointer event ignores pointerIndex, like getX(I).
		switch jvalueIAt(args, 0) {
		case motionAxisX:
			return floatOut(floatVal("x"))
		case motionAxisY:
			return floatOut(floatVal("y"))
		}
		return floatOut(0)
	case "getHistoricalAxisValue(III)F":
		// Same axis-first order: (axis, pointerIndex, pos).
		switch jvalueIAt(args, 0) {
		case motionAxisX:
			return floatOut(floatVal("x"))
		case motionAxisY:
			return floatOut(floatVal("y"))
		}
		return floatOut(0)

	// KeyEvent getters (shared getAction/getDeviceId/... cases above).
	case "getKeyCode()I":
		return intOut(val("keyCode"))
	case "getRepeatCount()I":
		return intOut(val("repeatCount"))
	case "getScanCode()I":
		return intOut(val("scanCode"))
	case "getUnicodeChar()I":
		return intOut(val("unicodeChar"))
	}
	return jnull(), false
}

func float32bits(f float32) uint32 {
	return *(*uint32)(unsafe.Pointer(&f))
}

// Input delivery target: the (env, activity, handle) triple the engine's
// registered GameActivity natives require. The runtime launcher knows all
// three right after initializeNativeCode returns and wires them here; until
// then events are counted as dropped, never queued or synthesized.
var inputTarget struct {
	mu       sync.RWMutex
	env      uintptr
	activity uintptr
	handle   uintptr
}

// InputStats counts delivered, consumed, and dropped input events.
type InputStats struct {
	FocusDelivered   uint64
	KeyDelivered     uint64
	PointerDelivered uint64
	KeyConsumed      uint64
	PointerConsumed  uint64
	Dropped          uint64
}

var inputStats InputStats

// SetGameActivityInputTarget wires the lifecycle identity that registered
// GameActivity natives require. Call once after initializeNativeCode
// returns a non-zero handle. Zero values park delivery (events drop).
func SetGameActivityInputTarget(env, activity, handle uintptr) {
	inputTarget.mu.Lock()
	defer inputTarget.mu.Unlock()
	inputTarget.env, inputTarget.activity, inputTarget.handle = env, activity, handle
	if handle != 0 {
		touch := pointerDeviceIsTouch()
		mode := "mouse"
		if touch {
			mode = "touch"
		}
		logging.Logger(logging.CatJNI).Info("[jni] input device mode",
			"mode", mode,
			"source", fmt.Sprintf("%#x", pointerSource()),
			"toolType", pointerTool(),
			"androidPCFeature", !touch,
			"keyboardDevice", !touch,
			"mouseDevice", !touch,
			"touchDevice", touch,
			"apkProduct", "Android")
	}
}

// ClearGameActivityInputTarget parks delivery (e.g. on teardown).
func ClearGameActivityInputTarget() {
	SetGameActivityInputTarget(0, 0, 0)
}

// InputDeliveryStats returns a snapshot of delivery counters.
func InputDeliveryStats() InputStats {
	return InputStats{
		FocusDelivered:   atomic.LoadUint64(&inputStats.FocusDelivered),
		KeyDelivered:     atomic.LoadUint64(&inputStats.KeyDelivered),
		PointerDelivered: atomic.LoadUint64(&inputStats.PointerDelivered),
		KeyConsumed:      atomic.LoadUint64(&inputStats.KeyConsumed),
		PointerConsumed:  atomic.LoadUint64(&inputStats.PointerConsumed),
		Dropped:          atomic.LoadUint64(&inputStats.Dropped),
	}
}

func inputTargetSnapshot() (env, activity, handle uintptr) {
	inputTarget.mu.RLock()
	defer inputTarget.mu.RUnlock()
	return inputTarget.env, inputTarget.activity, inputTarget.handle
}

func inputVM() *VM {
	globalMu.Lock()
	defer globalMu.Unlock()
	return globalVM
}

func dropEvent(reason string) {
	atomic.AddUint64(&inputStats.Dropped, 1)
	logging.Logger(logging.CatJNI).Info("[jni] input dropped", "reason", reason)
}

// DispatchGameActivityFocus delivers a real X11 focus transition through
// the engine-registered onWindowFocusChangedNative(JZ)V. Returns false if
// no target is wired or the native is not registered.
func DispatchGameActivityFocus(gained bool) bool {
	env, activity, handle := inputTargetSnapshot()
	if handle == 0 {
		dropEvent("focus: no target wired")
		return false
	}
	vm := inputVM()
	if vm == nil {
		dropEvent("focus: no vm")
		return false
	}
	fn := vm.NativeMethod(gameActivityClass, "onWindowFocusChangedNative", "(JZ)V")
	if fn == 0 {
		dropEvent("focus: native not registered")
		return false
	}
	z := uintptr(0)
	if gained {
		z = 1
	}
	C.tipsy_input_call_void4(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(activity), C.uintptr_t(handle), C.uintptr_t(z))
	atomic.AddUint64(&inputStats.FocusDelivered, 1)
	return true
}

// gestureClock tracks the downTime of the currently open pointer and key
// gesture streams. Android semantics: every event of one gesture (the
// MOVEs and the terminating ACTION_UP / ACTION_UP key event) must carry
// the downTime of the ACTION_DOWN that started it, and its own eventTime.
// X11 delivers real press/release pairs, so the recorded downTime is real
// measured data, never synthesized.
var gestureClock struct {
	mu          sync.Mutex
	pointerOpen bool  // an ACTION_DOWN is pending its ACTION_UP
	pointerDown int64 // downTime of the open pointer gesture
	keyOpen     bool  // a key ACTION_DOWN is pending its ACTION_UP
	keyDown     int64 // downTime of the open key gesture
	keyCode     int32 // Android keycode the open key gesture belongs to
}

func pointerGestureStart(now int64) int64 {
	gestureClock.mu.Lock()
	defer gestureClock.mu.Unlock()
	gestureClock.pointerOpen = true
	gestureClock.pointerDown = now
	return now
}

// pointerGestureDownTime returns the open gesture's downTime; a stray
// event with no open gesture self-anchors (downTime = eventTime). end
// closes the gesture (ACTION_UP / ACTION_CANCEL). The open state is
// tracked separately from the time: downTime 0 is a legitimate early
// clock value.
func pointerGestureDownTime(now int64, end bool) int64 {
	gestureClock.mu.Lock()
	defer gestureClock.mu.Unlock()
	if gestureClock.pointerOpen {
		dt := gestureClock.pointerDown
		if end {
			gestureClock.pointerOpen = false
		}
		return dt
	}
	return now
}

// pointerGestureOpen reports whether a pointer gesture (an ACTION_DOWN
// awaiting its ACTION_UP) is currently open.
func pointerGestureOpen() bool {
	gestureClock.mu.Lock()
	defer gestureClock.mu.Unlock()
	return gestureClock.pointerOpen
}

func keyGestureStart(now int64, keyCode int32) int64 {
	gestureClock.mu.Lock()
	defer gestureClock.mu.Unlock()
	gestureClock.keyOpen = true
	gestureClock.keyDown = now
	gestureClock.keyCode = keyCode
	return now
}

// keyGestureDownTime returns the matching key-down's downTime for a key
// release; a release with no open matching gesture self-anchors.
func keyGestureDownTime(now int64, keyCode int32) int64 {
	gestureClock.mu.Lock()
	defer gestureClock.mu.Unlock()
	if gestureClock.keyOpen && gestureClock.keyCode == keyCode {
		dt := gestureClock.keyDown
		gestureClock.keyOpen = false
		gestureClock.keyCode = 0
		return dt
	}
	return now
}

// DispatchGameActivityKey delivers a non-text key press/release through
// onKeyDownNative/onKeyUpNative(JLandroid/view/KeyEvent;)Z — the exact
// signatures the engine registers (RegisterNatives batch, launch logs);
// the leading J carries the wired input target handle. scanCode is the
// raw X11 keycode (closest honest hardware code). The returned bool is
// the engine's consumption verdict (false also when not wired).
func DispatchGameActivityKey(keyCode int32, scanCode int32, pressed bool) bool {
	if keyCode <= 0 {
		return false
	}
	env, activity, handle := inputTargetSnapshot()
	if handle == 0 {
		dropEvent("key: no target wired")
		return false
	}
	vm := inputVM()
	if vm == nil {
		dropEvent("key: no vm")
		return false
	}
	name := "onKeyUpNative"
	if pressed {
		name = "onKeyDownNative"
	}
	fn := vm.NativeMethod(gameActivityClass, name, "(JLandroid/view/KeyEvent;)Z")
	if fn == 0 {
		dropEvent("key: native not registered")
		return false
	}
	now := monotimeMillis()
	var down int64
	if pressed {
		down = keyGestureStart(now, keyCode)
	} else {
		down = keyGestureDownTime(now, keyCode)
	}
	vm.mu.Lock()
	ev := vm.newKeyEventLocked(keyCode, pressed, down, now, scanCode)
	obj := idToJobject(ev.id)
	vm.mu.Unlock()
	consumed := C.tipsy_input_call_bool4(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(activity), C.uintptr_t(handle), C.uintptr_t(obj)) != 0
	traceEventGetterLine("key", ev.fields["action"].(int32), ev.id)
	atomic.AddUint64(&inputStats.KeyDelivered, 1)
	if consumed {
		atomic.AddUint64(&inputStats.KeyConsumed, 1)
	}
	return consumed
}

// DispatchGameActivityPointer delivers a pointer event through
// onTouchEventNative(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z. The
// primitive packing after the event object is proven from the APK's own
// Java glue (classes2.dex, Lcom/google/androidgamesdk/GameActivity;.N1):
// the 5 I's are (pointerCount, historySize, deviceId, source, action),
// the 2 J's are (eventTime, downTime) — eventTime first —, the 6 I's are
// (flags, metaState, actionButton, buttonState, classification,
// edgeFlags), and the 2 F's are (xPrecision, yPrecision). The event
// object remains authoritative for coordinates (the real Java glue reads
// no x/y primitives; native reads them from the MotionEvent object).
// The source primitive and the object's source/toolType always carry the
// selected device identity. In touch mode buttonState is 0 — Android
// touchscreen events carry no mouse buttons — regardless of the caller
// argument. Android requires every event of a gesture (MOVEs and the
// ACTION_UP) to carry the initiating ACTION_DOWN's downTime.
func DispatchGameActivityPointer(action int32, x, y float32, buttonState int32) bool {
	env, activity, handle := inputTargetSnapshot()
	if handle == 0 {
		dropEvent("pointer: no target wired")
		return false
	}
	vm := inputVM()
	if vm == nil {
		dropEvent("pointer: no vm")
		return false
	}
	fn := vm.NativeMethod(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z")
	if fn == 0 {
		dropEvent("pointer: native not registered")
		return false
	}
	source, pressed := pointerSource(), buttonState
	if pointerDeviceIsTouch() {
		pressed = 0
	}
	now := monotimeMillis()
	var down int64
	switch action {
	case motionActionDown:
		down = pointerGestureStart(now)
	case motionActionUp, motionActionCancel:
		down = pointerGestureDownTime(now, true)
	default:
		down = pointerGestureDownTime(now, false)
	}
	vm.mu.Lock()
	ev := vm.newMotionEventLocked(action, x, y, down, now)
	obj := idToJobject(ev.id)
	vm.mu.Unlock()
	consumed := C.tipsy_input_call_touch(unsafe.Pointer(fn),
		C.uintptr_t(env), C.uintptr_t(activity), C.uintptr_t(handle), C.uintptr_t(obj),
		// i1..i5: pointerCount, historySize, deviceId, source, action
		// (GameActivity.N1 register order, proven from the APK DEX).
		1, 0, 0, C.int(source), C.int(action),
		// j1, j2: eventTime, downTime (DEX order — eventTime first).
		C.longlong(now), C.longlong(down),
		// k1..k6: flags, metaState, actionButton, buttonState,
		// classification, edgeFlags (classification 0 = CLASSIFICATION_NONE,
		// the real value at the SDK 26 this client reports).
		0, 0, 0, C.int(pressed), 0, 0,
		// f1, f2: xPrecision, yPrecision.
		1, 1) != 0
	traceEventGetterLine("pointer", action, ev.id)
	atomic.AddUint64(&inputStats.PointerDelivered, 1)
	if consumed {
		atomic.AddUint64(&inputStats.PointerConsumed, 1)
	}
	return consumed
}

// monotimeMillis is the Android event-time clock stand-in: monotonic
// milliseconds since VM creation, matching KeyEvent/MotionEvent uptime
// time semantics.
var inputClockStart = time.Now()

func monotimeMillis() int64 {
	return time.Since(inputClockStart).Milliseconds()
}

// pointerButtons tracks the Android button state derived from real X11
// button press/release pairs (BUTTON_PRIMARY=1, BUTTON_SECONDARY=2).
// Android reports buttonState as the set of pressed buttons during the
// event, i.e. the state after the reported change.
var pointerButtons struct {
	mu    sync.Mutex
	state int32
}

func pointerButtonState(x11Button int32, down bool) int32 {
	pointerButtons.mu.Lock()
	defer pointerButtons.mu.Unlock()
	switch x11Button {
	case 1:
		if down {
			pointerButtons.state |= 1
		} else {
			pointerButtons.state &^= 1
		}
	case 3:
		if down {
			pointerButtons.state |= 2
		} else {
			pointerButtons.state &^= 2
		}
	}
	return pointerButtons.state
}

// bindX11InputBridge subscribes the GameActivity input dispatcher to the
// X11 window's captured input events. Called from NewVM; a VM without a
// launched window simply never sees events.
func bindX11InputBridge() {
	x11.OnInput(handleX11InputEvent)
}

// handleX11InputEvent is the x11→GameActivity conversion for one captured
// real X11 event. Split from bindX11InputBridge so tests can drive the
// production mapping directly (X→x, Y→y must stay distinct end to end;
// the axis-slot bug class regresses here first).
func handleX11InputEvent(ev x11.InputEvent) {
	switch ev.Kind {
	case x11.InputFocus:
		DispatchGameActivityFocus(ev.FocusGained)
	case x11.InputKey:
		// A genuine engine showKeyboard call transfers focus to the APK's
		// RbxKeyboard editor. Preserve X11's physical edge as its own event,
		// but give that focused editor first refusal so Backspace/arrows/Enter
		// edit the textbox and do not also reach the SurfaceView listener.
		// With no engine-owned textbox session, the existing physical path is
		// byte-for-byte unchanged.
		if DispatchRobloxTextKey(ev.KeyCode, ev.KeyPressed) {
			return
		}
		path := keyboardDeliveryPath()
		// The supplied APK's final Roblox listener calls the direct key
		// native. GameActivity remains a control path; `both` is solely a
		// diagnostic to distinguish target wiring from listener selection.
		if (path == KeyboardPathGameActivity || path == KeyboardPathBoth) && ev.KeyCode > 0 {
			DispatchGameActivityKey(ev.KeyCode, ev.ScanCode, ev.KeyPressed)
		}
		if path == KeyboardPathDirect || path == KeyboardPathBoth {
			DispatchRobloxDirectKey(ev.ScanCode, ev.KeyCode, ev.KeyPressed)
		}
	case x11.InputText:
		// InputText is UTF-8 committed by X11/XIM, not reconstructed from a
		// physical keycode. Dispatch rejects it unless Roblox itself opened a
		// textbox through showKeyboard. Never log ev.Text: it may be a secret.
		DispatchRobloxTextCommit(ev.Text)
	case x11.InputPointer:
		path := pointerDeliveryPath()
		// Experimental `both` deliberately delivers GameActivity first and
		// direct second. The APK itself establishes no dual-delivery order:
		// MainGameActivity replaces GameActivity's single SurfaceView touch
		// listener with its NativeInputInterface listener. No other mode can
		// double-deliver an event.
		if path == PointerPathGameActivity || path == PointerPathBoth {
			dispatchGameActivityX11Pointer(ev)
		}
		if path == PointerPathDirect || path == PointerPathBoth {
			DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
		}
	case x11.InputScroll:
		// The final APK mouse listener sends ACTION_SCROLL only through the
		// direct NativeInputInterface wheel native. GameActivity's replaced
		// touch listener has no faithful wheel equivalent.
		path := pointerDeliveryPath()
		if path == PointerPathDirect || path == PointerPathBoth {
			DispatchRobloxDirectScroll(ev.X, ev.Y, ev.ScrollX, ev.ScrollY)
		}
	}
}

func dispatchGameActivityX11Pointer(ev x11.InputEvent) bool {
	if pointerDeviceIsTouch() {
		// Touch identity: a finger has no secondary button and a MOVE
		// without an open gesture is not a touch drag. Real X11 events that
		// cannot honestly be presented as touch are counted as dropped,
		// never rewritten into a lie. This filter belongs only to the
		// GameActivity arm; the descriptor-proven direct arm is mouse input.
		if ev.PointerAction == x11.PointerMove {
			if !pointerGestureOpen() {
				dropEvent("pointer: move without an open touch gesture")
				return false
			}
		} else if ev.Button != 1 {
			dropEvent("pointer: non-primary button in touch mode")
			return false
		}
		return DispatchGameActivityPointer(ev.PointerAction, ev.X, ev.Y, 0)
	}
	buttonState := pointerButtonState(ev.Button, ev.PointerAction == x11.PointerDown)
	return DispatchGameActivityPointer(ev.PointerAction, ev.X, ev.Y, buttonState)
}

// Test hooks exposing the recording natives (see the C preamble). Used
// only by _test.go files, which cannot import "C" in this package.
func testRecordFocusFn() uintptr    { return uintptr(C.tipsy_input_record_focus_fn()) }
func testRecordKeyFn() uintptr      { return uintptr(C.tipsy_input_record_key_fn()) }
func testRecordTouchFn() uintptr    { return uintptr(C.tipsy_input_record_touch_fn()) }
func testRec4(i int) uintptr        { return uintptr(C.tipsy_input_rec4_get(C.int(i))) }
func testRecTouchInt(i int) uintptr { return uintptr(C.tipsy_input_rec_touch_int(C.int(i))) }
func testRecTouchLong(i int) int64  { return int64(C.tipsy_input_rec_touch_long(C.int(i))) }
func testRecTouchFloat(i int) float32 {
	return float32(C.tipsy_input_rec_touch_float(C.int(i)))
}

func testRecTouchID(i int) uintptr { return uintptr(C.tipsy_input_rec_touch_id(C.int(i))) }

func testRecReset() { C.tipsy_input_rec_reset() }

// testEventGetter runs vm.dispatch for one getter on an event object.
// intArg >= 0 packs one jint argument; intArg < 0 passes nil args. Test
// files cannot import "C" in this package, so the C-typed call happens
// here.
func testEventGetter(vm *VM, objID int64, class, name, sig string, intArg int32) (uintptr, bool) {
	var args *C.jvalue
	if intArg >= 0 {
		args = packJint(intArg)
	}
	v, ok := vm.dispatch(idToJobject(objID), class, name, sig, args)
	return uintptr(v), ok
}

// testEventGetterII packs two jint arguments (e.g. getAxisValue(II)F).
func testEventGetterII(vm *VM, objID int64, class, name, sig string, a, b int32) (uintptr, bool) {
	sl := make([]C.jvalue, 2)
	C.tipsy_jvalue_set_i(&sl[0], C.jint(a))
	C.tipsy_jvalue_set_i(&sl[1], C.jint(b))
	v, ok := vm.dispatch(idToJobject(objID), class, name, sig, &sl[0])
	return uintptr(v), ok
}

// testEventGetterIII packs three jint arguments
// (e.g. getHistoricalAxisValue(III)F).
func testEventGetterIII(vm *VM, objID int64, class, name, sig string, a, b, c int32) (uintptr, bool) {
	sl := make([]C.jvalue, 3)
	C.tipsy_jvalue_set_i(&sl[0], C.jint(a))
	C.tipsy_jvalue_set_i(&sl[1], C.jint(b))
	C.tipsy_jvalue_set_i(&sl[2], C.jint(c))
	v, ok := vm.dispatch(idToJobject(objID), class, name, sig, &sl[0])
	return uintptr(v), ok
}
