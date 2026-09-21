// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include "gameactivity_input.h"
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

// inputEventPool reuses one MotionEvent and one KeyEvent per VM. The
// engine's registered glue consumes the event object synchronously inside
// onKeyDownNative/onTouchEventNative; creating a fresh object (plus its
// 15-entry field map and a JNI local reference on the calling thread state)
// for every X11 key/touch edge made object and local-reference growth
// unbounded. Pooling keeps the delivered-field ABI and the synchronous call
// semantics while making per-edge allocation flat. The pool is keyed by VM
// because objects are VM-local, and holds at most one object per event class.
type inputEventPoolEntry struct {
	vm  *VM
	obj *Object
}

var inputEventPool struct {
	mu     sync.Mutex
	key    inputEventPoolEntry
	motion inputEventPoolEntry
}

// pooledInputEventLocked returns this VM's reusable event object for the
// class, creating and marking it immortal on first use. The caller must hold
// vm.mu and, when created is true, release the creation local reference with
// vm.deleteLocal after unlocking (the object is immortal, so it stays alive
// with no local reference held).
func (vm *VM) pooledInputEventLocked(slot *inputEventPoolEntry, class string) (*Object, bool) {
	inputEventPool.mu.Lock()
	defer inputEventPool.mu.Unlock()
	if slot.vm == vm && slot.obj != nil {
		return slot.obj, false
	}
	cls := vm.ensureClassLocked(class)
	o := vm.newObjectOn(vm.envRaw, cls)
	o.markImmortal()
	*slot = inputEventPoolEntry{vm: vm, obj: o}
	return o, true
}

// resetMotionEventLocked fills the single-pointer MotionEvent fields whose
// getters (dispatchInput) answer from real X11 pointer data, carrying the
// selected device identity (desktop mouse by default, touch finger behind the
// TIPSY_INPUT_DEVICE=touch gate).
func (vm *VM) resetMotionEventLocked(o *Object, action int32, x, y float32, downTime, eventTime int64) {
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
}

// newMotionEventLocked builds a standalone single-pointer MotionEvent object.
// Production dispatch uses pooledMotionEventLocked; this constructor remains
// for the ABI-witness tests and one-off objects.
func (vm *VM) newMotionEventLocked(action int32, x, y float32, downTime, eventTime int64) *Object {
	cls := vm.ensureClassLocked(motionEventClass)
	o := vm.newObjectLocked(cls)
	vm.resetMotionEventLocked(o, action, x, y, downTime, eventTime)
	return o
}

// pooledMotionEventLocked resets the VM's reusable MotionEvent and reports
// whether the caller must release its creation local reference.
func (vm *VM) pooledMotionEventLocked(action int32, x, y float32, downTime, eventTime int64) (*Object, bool) {
	o, created := vm.pooledInputEventLocked(&inputEventPool.motion, motionEventClass)
	vm.resetMotionEventLocked(o, action, x, y, downTime, eventTime)
	return o, created
}

// resetKeyEventLocked fills a KeyEvent object for a non-text key. scanCode
// carries the raw X11 keycode (the closest honest hardware code).
func (vm *VM) resetKeyEventLocked(o *Object, keyCode int32, pressed bool, downTime, eventTime int64, scanCode int32) {
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
}

// newKeyEventLocked builds a standalone KeyEvent object. Production dispatch
// uses pooledKeyEventLocked; this constructor remains for the ABI-witness
// tests and one-off objects.
func (vm *VM) newKeyEventLocked(keyCode int32, pressed bool, downTime, eventTime int64, scanCode int32) *Object {
	cls := vm.ensureClassLocked(keyEventClass)
	o := vm.newObjectLocked(cls)
	vm.resetKeyEventLocked(o, keyCode, pressed, downTime, eventTime, scanCode)
	return o
}

// pooledKeyEventLocked resets the VM's reusable KeyEvent and reports whether
// the caller must release its creation local reference.
func (vm *VM) pooledKeyEventLocked(keyCode int32, pressed bool, downTime, eventTime int64, scanCode int32) (*Object, bool) {
	o, created := vm.pooledInputEventLocked(&inputEventPool.key, keyEventClass)
	vm.resetKeyEventLocked(o, keyCode, pressed, downTime, eventTime, scanCode)
	return o, created
}

// dispatchInput serves the MotionEvent/KeyEvent getter surface the engine's
// glue resolves via GetMethodID during initializeNativeCode (each name in
// this switch is in the observed missing-method list at that call site).
// Everything else falls through to the normal dispatch path. Callers arrive
// through resolveDispatch/wrapStub (GoJNI_CallA, callDispatchOrStubEnv,
// VM.dispatch), none of which hold vm.mu, so field reads take vm.mu.RLock to
// pair with the vm.mu-held pooled-event reset writers.
func (vm *VM) dispatchInput(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if o == nil || (class != motionEventClass && class != keyEventClass) {
		return jnull(), false
	}
	val := func(field string) int64 {
		vm.mu.RLock()
		v := o.fields[field]
		vm.mu.RUnlock()
		switch t := v.(type) {
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
		vm.mu.RLock()
		f, ok := o.fields[field].(float32)
		vm.mu.RUnlock()
		if ok {
			return f
		}
		return 0
	}
	intOut := func(v int64) (C.jobject, bool) {
		// Observation-only: count the getter identity native consumed
		// (name+sig, never the value) for the per-event consumption proof.
		noteGetterCall(o.id, name, sig)
		return C.jobject(uintptr(uint32(v))), true
	}
	longOut := func(v int64) (C.jobject, bool) {
		noteGetterCall(o.id, name, sig)
		return C.jobject(uintptr(v)), true
	}
	floatOut := func(v float32) (C.jobject, bool) {
		noteGetterCall(o.id, name, sig)
		return C.jobject(uintptr(float32bits(v))), true
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
		case motionAxisZ, motionAxisRX, motionAxisRY, motionAxisRZ,
			motionAxisHatX, motionAxisHatY, motionAxisLTrigger, motionAxisRTrigger:
			// Gamepad axes (Z/RZ + RX/RY mirror, hats, triggers) ride the
			// per-object gamepadAxes map; pointer/key objects without the
			// map answer 0, exactly as before.
			return floatOut(vm.gamepadAxisValue(o, jvalueIAt(args, 0)))
		}
		return floatOut(0)
	case "getHistoricalAxisValue(III)F":
		// Same axis-first order: (axis, pointerIndex, pos).
		switch jvalueIAt(args, 0) {
		case motionAxisX:
			return floatOut(floatVal("x"))
		case motionAxisY:
			return floatOut(floatVal("y"))
		case motionAxisZ, motionAxisRX, motionAxisRY, motionAxisRZ,
			motionAxisHatX, motionAxisHatY, motionAxisLTrigger, motionAxisRTrigger:
			return floatOut(vm.gamepadAxisValue(o, jvalueIAt(args, 0)))
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
	return globalVM.Load()
}

func dropEvent(reason string) {
	atomic.AddUint64(&inputStats.Dropped, 1)
	// High-frequency honest drops (stray moves, unmapped buttons) must not
	// flood the default Info log; the counter remains the authoritative
	// diagnostic.
	logging.Logger(logging.CatJNI).Debug("[jni] input dropped", "reason", reason)
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
	pointerOpen bool            // an ACTION_DOWN is pending its ACTION_UP
	pointerDown int64           // downTime of the open pointer gesture
	keys        map[int32]int64 // downTime for each held Android key
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

// Each held key owns its original downTime, including repeated downs and
// interleaved modifiers/movement keys. A stray release self-anchors.
func keyGestureTime(now int64, keyCode int32, pressed bool, repeatCount int32) int64 {
	gestureClock.mu.Lock()
	defer gestureClock.mu.Unlock()
	down, held := gestureClock.keys[keyCode]
	if pressed {
		if repeatCount > 0 && held {
			return down
		}
		if gestureClock.keys == nil {
			gestureClock.keys = make(map[int32]int64)
		}
		gestureClock.keys[keyCode] = now
		return now
	}
	delete(gestureClock.keys, keyCode)
	if held {
		return down
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
	return dispatchGameActivityKey(keyCode, scanCode, pressed, 0)
}

func dispatchGameActivityKey(keyCode int32, scanCode int32, pressed bool, repeatCount int32) bool {
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
	if !pressed || repeatCount < 0 {
		repeatCount = 0
	}
	down := keyGestureTime(now, keyCode, pressed, repeatCount)
	vm.mu.Lock()
	ev, created := vm.pooledKeyEventLocked(keyCode, pressed, down, now, scanCode)
	ev.fields["repeatCount"] = repeatCount
	obj := idToJobject(ev.id)
	evID := ev.id
	vm.mu.Unlock()
	if created {
		// The pooled object is immortal; this drops the creation-time local
		// reference from the VM's own env, so no per-edge reference remains.
		vm.deleteLocal(evID)
	}
	consumed := C.tipsy_input_call_bool4(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(activity), C.uintptr_t(handle), C.uintptr_t(obj)) != 0
	vm.mu.RLock()
	action, _ := ev.fields["action"].(int32)
	vm.mu.RUnlock()
	traceEventGetterLine("key", action, ev.id)
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
	ev, created := vm.pooledMotionEventLocked(action, x, y, down, now)
	obj := idToJobject(ev.id)
	evID := ev.id
	vm.mu.Unlock()
	if created {
		// See dispatchGameActivityKey: drop the creation local reference.
		vm.deleteLocal(evID)
	}
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

// rmbPointerFallback is deliberately narrower than Roblox's native lock
// getter. The APK remains authoritative when its getter is true. This state
// exists only for the measured desktop hole where a real secondary DOWN leaves
// that getter false: after the already-delivered edge, a successful host grab
// supplies the relative stream for the duration of that held RMB gesture.
// It is cleared before every ungrab path.
var rmbPointerFallback atomic.Bool

// pointerLockSticky remembers a live first-person/host grab across Alt-Tab.
// Roblox's getter often goes false after GameActivity focus loss, so FocusIn
// must recapture without waiting for another click. It is the LockCenter half
// of the engine authority; the LockCurrentPosition half is engineAnchorLock.
var pointerLockSticky atomic.Bool

// pointerLockSetter is a test seam for the host boundary. Production never
// replaces it; retaining the call behind this seam lets tests prove that a
// rejected fallback preserves Roblox's button edges. First-person / shift-lock
// uses the centered grab; held-RMB camera look uses the cursor-anchor grab.
var pointerLockSetter = x11.SetPointerLock
var pointerLockAtCursorSetter = x11.SetPointerLockAtCursor

// pointerLockAtCenterSetter re-anchors a live non-sticky grab at the window
// center and releases it there. Only the zoom-lock unlock uses it: the
// engine cursor reappears at the center when first person ends, so the host
// pointer is moved to meet it just before the ungrab. It is never the
// acquiring anchor -- arming is a guess and must not move a cursor.
var pointerLockAtCenterSetter = x11.SetPointerLockAtCenter

// pointerCursorSetter is a test seam for the host cursor boundary.
// Production never replaces it.
var pointerCursorSetter = x11.SetCursorVisible

// leftAltKeyCode is Android KEYCODE_ALT_LEFT, the desktop persistent-capture
// toggle. RightAlt (AltGr) is never consumed so layout input keeps working.
const leftAltKeyCode = 57

// persistentPointerCapture is the desktop-style in-experience grab. The
// official Android listener captures only under the engine LockCenter getter,
// but several first-person experiences (notably Project 12 [BODY CAM!]) never
// set that state, leaving an uncaptured pointer that stops camera travel at
// the screen edge. While in an experience this anchored grab confines the
// pointer and integrates the same unbounded logical cursor as the held-RMB
// fallback, so look keeps working; buttons and wheel are points and clamp to
// the live surface at dispatch so clicks and zoom always land on-view. The
// engine getter stays the sole LockCenter authority: a getter-true transition
// converts this grab to the centered sticky grab in place, and an enum read of
// LockCurrentPosition hands it to engineAnchorLock instead. Mutually exclusive
// with rmbPointerFallback; at most one anchored mode holds the X11 grab.
var persistentPointerCapture atomic.Bool

// engineLock is the last observed value of the engine's own lock authority:
// nativeGetMainWindowIsMouseLockedCenter, a direct read of
// UserInputService.MouseBehavior == LockCenter. Observing it costs one native
// call and changes nothing, so observation is never suppressed; only *acting*
// on it is gated by the operator's LeftAlt toggle. Keeping the state tracked
// across a release is what lets the next re-arm honour the engine immediately
// instead of waiting for a motion sample, and it is what makes an engine-driven
// session legible in a trace.
var engineLock struct {
	known  atomic.Bool
	locked atomic.Bool
}

// engineBehavior tracks the last observed full Enum.MouseBehavior, alongside
// engineLock's boolean. The exported getter is `cmpl $0x1`, so it collapses
// LockCurrentPosition into "not locked" and that value is invisible without
// this. Tracking it separately is what gives the anchored grab an edge to
// engage and release on, and what makes the third value legible in a trace.
var engineBehavior struct {
	known atomic.Bool
	value atomic.Uint32
}

// engineLockObservation is one observation of the engine's lock authority: the
// exported boolean and, when the read-only enum authority is live, the full
// Enum.MouseBehavior behind it.
type engineLockObservation struct {
	locked     bool
	available  bool
	behavior   MouseBehavior
	behaviorOK bool
}

// wantsCenter reports LockCenter: the engine froze its cursor at the viewport
// center, so the host grab warps there and is sticky. With the enum authority
// unavailable this is exactly the exported boolean, unchanged.
func (o engineLockObservation) wantsCenter() bool {
	return o.locked || (o.behaviorOK && o.behavior == MouseBehaviorLockCenter)
}

// wantsAnchor reports LockCurrentPosition: the engine froze its cursor exactly
// where it already was, so the host grab is anchored at that same point and
// nothing warps. The exported boolean reports this value as "not locked",
// which is the whole reason the enum authority exists.
func (o engineLockObservation) wantsAnchor() bool {
	return o.behaviorOK && o.behavior == MouseBehaviorLockCurrentPosition
}

// observeEngineLock probes the engine getter, records it and logs every
// transition exactly once. It never grabs, never releases and never touches a
// capture flag: callers decide what to do with the answer. Every getter probe
// in this file goes through it so no observation is lost.
func observeEngineLock() engineLockObservation {
	locked, available := RobloxMainWindowMouseLocked()
	if !available {
		if engineLock.known.Swap(false) {
			logging.Logger(logging.CatJNI).Info("[jni] engine mouse lock authority gone")
		}
		return engineLockObservation{}
	}
	first := !engineLock.known.Swap(true)
	if changed := engineLock.locked.Swap(locked) != locked; first || changed {
		logging.Logger(logging.CatJNI).Info("[jni] engine mouse lock",
			"locked", locked, "first", first)
	}
	obs := engineLockObservation{locked: locked, available: true}
	// The read-only enum authority is read here and only here, at exactly the
	// cadence the boolean already had: no new thread, no polling loop, no
	// timer, and nothing is written anywhere in the engine's address space.
	behavior, ok := engineMouseBehaviorProbe()
	if !ok {
		return obs
	}
	// The engine's own boolean polices the read. One straddled transition is
	// tolerated; three in a row disable the authority and hand the decision
	// back to the boolean, loudly.
	if !engineMouseBehaviorAgrees(behavior, locked) {
		return obs
	}
	obs.behavior, obs.behaviorOK = behavior, true
	if seenFirst := !engineBehavior.known.Swap(true); seenFirst ||
		engineBehavior.value.Swap(uint32(behavior)) != uint32(behavior) {
		logging.Logger(logging.CatJNI).Info("[jni] engine MouseBehavior",
			"behavior", behavior.String(), "value", uint8(behavior), "first", seenFirst)
	}
	return obs
}

// noteEngineStickyEngaged records that the engine's own LockCenter authority
// took the centered sticky grab. x11.SetPointerLock reports "unchanged" when
// it re-centers a grab Tipsy already held, so an engine-driven conversion in
// place otherwise leaves no trace at all and a trace reader sees only the
// heuristic's earlier acquire. It replaces the old "pointer lock armed after
// scroll" line, which covered exactly one of the four edges that can engage it.
func noteEngineStickyEngaged(edge string, converted bool) {
	logging.Logger(logging.CatJNI).Info("[jni] engine lock: centered sticky grab engaged",
		"edge", edge, "converted", converted)
}

// engineAnchorLock is the engine's own LockCurrentPosition authority. The
// exported boolean cannot see that value, so it owns a state of its own: the
// engine froze its cursor exactly where it already was, and the host grab
// that mirrors it is anchored at that same logical point instead of being
// warped to the window center. Like pointerLockSticky it is engine state, so
// focus loss and the operator's LeftAlt release suppress the *grab* but never
// the state, and the re-acquire point is remembered so the next focus gain or
// Alt re-arm takes the grab where the host pointer is instead of waiting for a
// motion sample. Unlike the heuristic it is never disarmed by motion: the
// engine, not a guess, owns it. Mutually exclusive with pointerLockSticky and
// persistentPointerCapture.
//
// It remembers two different points, and the difference is the whole release
// contract:
//
//   - x, y is where the host grab was last taken, which is where the desktop
//     pointer sits while the grab holds. A focus gain or an Alt re-arm
//     re-acquires there so the logical pair and the host pointer agree. It is
//     deliberately not advanced by captured motion: the X11 grab's anchor does
//     not move when the integrator drifts, so following the drift would make
//     the remembered point disagree with the host pointer.
//   - originX, originY is the engage-time frozen origin: the point the engine
//     froze its cursor at when it entered LockCurrentPosition, and therefore
//     the point its cursor reappears at when MouseBehavior returns to Default.
//     It never moves while the engine holds the value, so it is the release
//     restore point; the integrator's drift is a Tipsy-side device to keep
//     deltas flowing and says nothing about where the engine's cursor is.
var engineAnchorLock struct {
	mu               sync.Mutex
	active           bool
	x, y             float32
	originX, originY float32
	// releaseAtCenter inverts the restore point for the deferred release.
	// LockCurrentPosition's own semantic is "the cursor reappears where it
	// was frozen", so the origin is the default and the viewport center is
	// the exception: only a first-person zoom-out earns it, and the wheel
	// path marks it when the engine's value lags the detent that caused it.
	releaseAtCenter bool
}

func engineAnchorActive() bool {
	engineAnchorLock.mu.Lock()
	defer engineAnchorLock.mu.Unlock()
	return engineAnchorLock.active
}

// storeEngineAnchorLock latches or clears the authority, remembering the point
// the host grab was taken at while it is active. Clearing also forgets the
// frozen origin, which belongs to a live lock.
func storeEngineAnchorLock(active bool, x, y float32) {
	engineAnchorLock.mu.Lock()
	engineAnchorLock.active, engineAnchorLock.x, engineAnchorLock.y = active, x, y
	// A release, and equally a fresh take, drops a stale restore mark: it
	// belongs to one ending gesture and is consumed by the next sample.
	engineAnchorLock.releaseAtCenter = false
	if !active {
		engineAnchorLock.originX, engineAnchorLock.originY = 0, 0
	}
	engineAnchorLock.mu.Unlock()
}

// rememberEngineAnchorOrigin records the engage-time frozen origin. Only a
// fresh engagement calls it: a re-acquire after a focus flap or an Alt toggle
// is the same engine lock, whose frozen origin has not moved.
func rememberEngineAnchorOrigin(x, y float32) {
	engineAnchorLock.mu.Lock()
	engineAnchorLock.originX, engineAnchorLock.originY = x, y
	engineAnchorLock.mu.Unlock()
}

// engineAnchorFrozenOrigin reports the engage-time frozen origin, the point the
// engine's cursor reappears at when LockCurrentPosition ends. ok is false when
// no such lock is latched.
func engineAnchorFrozenOrigin() (float32, float32, bool) {
	engineAnchorLock.mu.Lock()
	defer engineAnchorLock.mu.Unlock()
	if !engineAnchorLock.active {
		return 0, 0, false
	}
	return engineAnchorLock.originX, engineAnchorLock.originY, true
}

// markEngineAnchorReleaseAtCenter records that the gesture ending this
// LockCurrentPosition lock is a first-person zoom-out, so its deferred release
// centers both cursors instead of restoring the engine's frozen origin. The
// engine's MouseBehavior lags the input that changes it (26-42 ms measured in
// place 79966250354565), so a zoom-out detent can still probe as
// LockCurrentPosition and leave the actual release to the next motion sample.
// Without this mark that deferred release cannot tell a zoom-out from an RMB
// camera-look release, and centering both is what teleported the pointer to
// the middle of the window on the first pixel after every RMB release.
func markEngineAnchorReleaseAtCenter() {
	engineAnchorLock.mu.Lock()
	if engineAnchorLock.active {
		engineAnchorLock.releaseAtCenter = true
	}
	engineAnchorLock.mu.Unlock()
}

// engineAnchorReleasesAtCenter reports whether the pending release was marked
// as a first-person zoom-out. False -- the default -- means the frozen origin,
// which is LockCurrentPosition's own contract.
func engineAnchorReleasesAtCenter() bool {
	engineAnchorLock.mu.Lock()
	defer engineAnchorLock.mu.Unlock()
	return engineAnchorLock.releaseAtCenter
}

// engineAnchorOrigin reports the remembered point the host grab was taken at,
// the last-resort re-acquire anchor. ok is false before the first engagement
// or after a release.
func engineAnchorOrigin() (float32, float32, bool) {
	engineAnchorLock.mu.Lock()
	defer engineAnchorLock.mu.Unlock()
	if !engineAnchorLock.active {
		return 0, 0, false
	}
	return engineAnchorLock.x, engineAnchorLock.y, true
}

// engineAnchorPoint reports the logical position the engine's frozen cursor is
// at, and therefore where the host grab must anchor and seed. prefer is the
// event's own pointer coordinate when that event carries a real position (an
// absolute move, a button edge, a wheel detent); havePrefer is false for a
// captured relative sample, whose X/Y is the grab anchor rather than a
// position, and for the focus and Alt edges, which carry no coordinates at
// all. ok is false when no source has established a position yet, and the
// caller then takes no grab: waiting one sample for a real origin beats
// inventing one the engine never had.
func engineAnchorPoint(preferX, preferY float32, havePrefer bool) (float32, float32, bool) {
	if havePrefer {
		return preferX, preferY, true
	}
	if x, y, ok := RobloxDirectFallbackPosition(); ok {
		return x, y, true
	}
	if x, y, ok := robloxDirectLastPosition(); ok {
		return x, y, true
	}
	return engineAnchorOrigin()
}

// engageEngineAnchorLock mirrors the engine's LockCurrentPosition on the host,
// taking the cursor-anchored grab. The engine froze its cursor where it already
// was, so the grab is anchored at that same logical point: nothing warps, and
// centering here would desync the engine's frozen origin from the host pointer
// so the first absolute sample teleported by the whole distance. The integrator
// keeps running from the frozen origin exactly as the held-RMB fallback does,
// so look keeps working and buttons and wheel still land on the engine cursor.
// Returns false when no honest anchor point exists yet, or when the host
// refused the grab.
func engageEngineAnchorLock(edge string, preferX, preferY float32, havePrefer bool) bool {
	return takeEngineAnchorLock(edge, preferX, preferY, havePrefer, true)
}

// adoptEngineAnchorLock hands an already-held cursor-anchored grab to the
// engine without re-taking it. It is the button-edge path: the persistent
// stream already holds exactly the grab LockCurrentPosition wants, so the
// press, drag and release keep their logical coordinates and no host call is
// made.
func adoptEngineAnchorLock(edge string) bool {
	return takeEngineAnchorLock(edge, 0, 0, false, false)
}

func takeEngineAnchorLock(edge string, preferX, preferY float32, havePrefer, reacquire bool) bool {
	x, y, ok := engineAnchorPoint(preferX, preferY, havePrefer)
	if !ok {
		return false
	}
	// A guess that was already right is not re-guessed: the heuristic's or the
	// held-RMB fallback's cursor-anchored grab is exactly the grab this value
	// wants, so the engine takes ownership of it in place instead of re-taking
	// it.
	converted := persistentPointerCapture.Swap(false) || rmbPointerFallback.Swap(false)
	if converted {
		// The engine owns the lock from here; the wheel guess is retired.
		resetZoomLock()
	}
	// LockCenter and LockCurrentPosition are mutually exclusive engine
	// states, so the centered request never stays armed underneath.
	pointerLockSticky.Store(false)
	if reacquire {
		if _, err := pointerLockAtCursorSetter(true); err != nil {
			storeEngineAnchorLock(false, 0, 0)
			return false
		}
	}
	BeginRobloxDirectPointerFallback(x, y)
	engaged := !engineAnchorActive()
	storeEngineAnchorLock(true, x, y)
	if engaged {
		// A fresh engagement means the engine has just entered
		// LockCurrentPosition, so x, y is the point it froze its cursor at.
		// Remember it separately: the integrator drifts from here for the rest
		// of the lock, and a release that restored at the drifted point would
		// teleport the engine cursor by the whole look.
		rememberEngineAnchorOrigin(x, y)
	}
	pointerCursorSetter(false)
	if engaged {
		noteEngineAnchorEngaged(edge, converted)
	}
	return true
}

// noteEngineAnchorEngaged records that the engine's own LockCurrentPosition
// authority took the anchored grab. It is the sibling of
// noteEngineStickyEngaged and exists for the same reason: the value the
// exported boolean reports as "not locked" otherwise leaves no trace at all.
func noteEngineAnchorEngaged(edge string, converted bool) {
	logging.Logger(logging.CatJNI).Info("[jni] engine lock: anchored sticky grab engaged",
		"edge", edge, "converted", converted,
		"behavior", MouseBehaviorLockCurrentPosition.String())
}

// releaseEngineLockAtCenter frees an engine-authoritative grab when
// MouseBehavior returns to Default and puts both cursors back together at the
// viewport center. The engine cursor reappears there, so the logical pair is
// re-seeded to meet it (SeedRobloxDirectPointerAtCenter) and the host pointer
// is warped to the same point by the same X11 re-anchor
// releasePersistentCaptureAtCenter uses. Two cursors that agree make the first
// absolute sample continuous instead of a teleport.
//
// This is the release for the states that really did freeze the engine cursor
// at the viewport center -- LockCenter (pointerLockSticky) and the wheel
// heuristic's own releasePersistentCaptureAtCenter -- and for the camera-driven
// edges of a first-person lock, where the long-standing contract is that the
// cursor comes back in the middle when first person ends.
// releaseEngineLockAtOrigin is its sibling for LockCurrentPosition.
func releaseEngineLockAtCenter(edge string) {
	storeEngineAnchorLock(false, 0, 0)
	pointerLockSticky.Store(false)
	rmbPointerFallback.Store(false)
	persistentPointerCapture.Store(false)
	SeedRobloxDirectPointerAtCenter()
	ClearRobloxDirectPointerFallbackKeepLast()
	_, _ = pointerLockAtCenterSetter(true)
	_, _ = pointerLockAtCenterSetter(false)
	logging.Logger(logging.CatJNI).Info("[jni] engine lock released; pointer free", "edge", edge)
}

// releaseEngineLockAtOrigin frees the engine's own LockCurrentPosition grab on
// the edge that ends it and puts both cursors back at the engage-time frozen
// origin. LockCurrentPosition froze the engine cursor exactly where it already
// was, so that point is where it reappears when MouseBehavior returns to
// Default; restoring anywhere else -- the viewport center in particular --
// teleports it by the whole distance and breaks the long-standing held-RMB
// contract that RMB camera look unlocks where the operator clicked
// (status §217/§245). The integrator's drift says nothing about where the
// engine's cursor is, which is why the origin is remembered at engage time
// rather than read back from the live integrator.
//
// The host side needs no warp: the anchored grab was taken at the frozen origin
// and tipsy_pointer_unlock deliberately leaves the desktop pointer at the grab
// anchor, so the ungrab alone leaves it exactly there. Only the logical pair is
// re-seeded, which is the same release-at-grab-anchor in logical space that
// EndRobloxDirectCapturedSecondary implements for the persistent stream.
//
// The engine's lock and Tipsy's own desktop capture policy are separate
// decisions. When the policy still wants the pointer confined (the always
// policy, or an armed zoom lock) the grab it already holds is simply handed
// back to it, exactly as an RMB gesture inside a captured stream never broke
// confinement before the authority existed, and the policy's own release path
// frees the pointer later. Otherwise the pointer goes free on this edge.
func releaseEngineLockAtOrigin(edge string) {
	x, y, ok := engineAnchorFrozenOrigin()
	storeEngineAnchorLock(false, 0, 0)
	pointerLockSticky.Store(false)
	rmbPointerFallback.Store(false)
	if ok {
		SeedRobloxDirectPointerAt(x, y)
	}
	// The captured-secondary gesture is over; keep the restored origin as the
	// ordinary dispatcher's seed exactly as the center release does.
	ClearRobloxDirectPointerFallbackKeepLast()
	if ok && persistentAcquireWanted() && inExperience() {
		BeginRobloxDirectPointerFallback(x, y)
		persistentPointerCapture.Store(true)
		logging.Logger(logging.CatJNI).Info("[jni] engine lock released at the frozen origin; desktop capture retained",
			"edge", edge, "policy", pointerCapturePolicyName(),
			"behavior", MouseBehaviorLockCurrentPosition.String())
		return
	}
	persistentPointerCapture.Store(false)
	_, _ = pointerLockAtCursorSetter(false)
	logging.Logger(logging.CatJNI).Info("[jni] engine lock released at the frozen origin; pointer free",
		"edge", edge, "behavior", MouseBehaviorLockCurrentPosition.String())
}

// zoomLock is the wheel-driven first-person heuristic for persistent
// capture. The engine exports no first-person or cursor-visibility signal
// (Project 12 never reports LockCenter yet hides its cursor and locks the
// look), so the only observable edge of such a lock is the wheel that causes
// it. A deliberate run of zoom-in detents (zoomLockArmDetents within
// zoomLockRunWindow of each other) arms the lock. Arming is a guess, so it
// is anchored, never centered: the grab and the logical pair both stay at
// the live pointer (acquirePersistentCapture), so a false positive -- a GUI
// panel scrolled three notches -- moves no cursor at all and the panel stays
// usable. There is no engine origin to agree with here by construction: the
// only mechanism that freezes a cursor origin is UserInputService
// .MouseBehavior, and RobloxMainWindowMouseLocked reads that property
// directly, so a real engine lock is the getter-true path and owns centering
// itself (pointerLockSticky, and the anchored->centered conversion).
//
// The heuristic is gated on the read-only enum authority being *unavailable*
// (see engineBehaviorAuthoritative). Once the engine is answering for real,
// Tipsy never guesses: the wheel run is retired at its gate rather than
// deleted, so an experience whose MouseBehavior the authority cannot read at
// all -- no object yet, a decode failure, or three disagreements with the
// exported boolean -- still gets it as the honest fallback.
//
// The zoom-out detents that leave first person do re-seed the logical cursor
// to the viewport center, because there the engine cursor becomes visible
// again and the operator expects it back in the middle. That re-seed and the
// matching host re-anchor in releasePersistentCaptureAtCenter keep the two
// cursors together, so the first motion afterwards does not teleport. That
// first motion disarms. Motion between detents integrates exact dx/dy from
// wherever the pair is; nothing is ever pinned. A one- or two-notch
// third-person zoom never arms, so ordinary zooming is untouched.
//
// Under the default zoom capture policy the host grab follows the same
// edges: the arming detent takes the anchored non-sticky grab and the
// disarming motion frees it at the center
// (releasePersistentCaptureAtCenter), so a zoomed-out pointer is free to
// leave the window. Under TIPSY_MOUSE_CAPTURE=always the grab is held for
// the whole experience and only the logical re-seeding applies.
const (
	zoomLockArmDetents = 3
	zoomLockRunWindow  = 2 * time.Second
)

var zoomLock struct {
	mu         sync.Mutex
	run        int
	lastDetent time.Time
	armed      bool
	unlocking  bool
}

// zoomLockNow is the heuristic's clock; tests substitute it.
var zoomLockNow = time.Now

func resetZoomLock() {
	zoomLock.mu.Lock()
	zoomLock.run, zoomLock.armed, zoomLock.unlocking = 0, false, false
	zoomLock.lastDetent = time.Time{}
	zoomLock.mu.Unlock()
}

// zoomLockArmed reports the heuristic state for tests and diagnostics.
func zoomLockArmed() bool {
	zoomLock.mu.Lock()
	defer zoomLock.mu.Unlock()
	return zoomLock.armed
}

// zoomLockNoteMotion disarms after the zoom-out detents that ended a lock:
// the engine cursor is visible again and now tracks from the center. It
// reports true on the motion sample that completed the disarm.
func zoomLockNoteMotion() bool {
	zoomLock.mu.Lock()
	defer zoomLock.mu.Unlock()
	if !zoomLock.unlocking {
		return false
	}
	zoomLock.armed, zoomLock.unlocking, zoomLock.run = false, false, 0
	return true
}

// zoomLockDetent records one wheel detent and reports whether the logical
// cursor must be re-seeded to the center for it (center; the zoom-out detents
// that end a lock, where the engine cursor becomes visible again) and whether
// this detent must take the anchored host grab (acquire; zoom-in only, and
// only once the run has armed).
func zoomLockDetent(zoomIn bool, now time.Time) (center, acquire bool) {
	zoomLock.mu.Lock()
	defer zoomLock.mu.Unlock()
	if !zoomLock.lastDetent.IsZero() && now.Sub(zoomLock.lastDetent) > zoomLockRunWindow {
		zoomLock.run = 0
	}
	zoomLock.lastDetent = now
	if zoomIn {
		zoomLock.run++
		zoomLock.unlocking = false
		if zoomLock.run >= zoomLockArmDetents {
			zoomLock.armed = true
		}
		// Armed zoom-in never re-seeds: the engine has no frozen origin on
		// this getter-false path, so centering here would be a visible cost
		// with no engine-side counterpart.
		return false, zoomLock.armed
	}
	zoomLock.run = 0
	if zoomLock.armed {
		zoomLock.unlocking = true
		return true, false
	}
	return false, false
}

// zoomLockWheel applies the first-person heuristic to one wheel detent taken
// in a joined experience under the desktop capture policy: from a free
// pointer, under the persistent grab, or during the held-RMB fallback. When
// it re-seeds the logical pair it returns the center and ok=true; the detent
// must then be delivered at that point. Otherwise the caller delivers the
// detent where it already was: the live logical cursor under a grab, or the
// raw pointer when free. An unarmed zoom-out whose logical cursor already
// drifted off-view re-seeds too: nothing visible can jump, and reappearing at
// the center beats reappearing at a clamped edge.
//
// In the default zoom policy the arming detent is also where the pointer
// gets confined, on the detent itself. That grab is anchored at the live
// pointer, not at the window center: arming is a heuristic guess and a wrong
// guess must cost nothing visible. A refused grab (unfocused window) leaves
// the lock armed and the next absolute motion retries. x, y is the pointer
// position carried by the wheel event, the anchor for that acquire.
func zoomLockWheel(x, y, scrollY float32) (cx, cy float32, ok bool) {
	if scrollY == 0 {
		return 0, 0, false
	}
	center, acquire := zoomLockDetent(scrollY > 0, zoomLockNow())
	if !center && scrollY < 0 {
		if off, has := RobloxDirectFallbackOffView(); has && off {
			center = true
		}
	}
	if acquire && !persistentPointerCapture.Load() && !rmbPointerFallback.Load() &&
		!pointerLockSticky.Load() {
		// Anchoring in place leaves the engine cursor exactly where the
		// detent found it, so no zero-delta move is needed to announce a new
		// origin: there is no new origin.
		acquirePersistentCapture(x, y)
	}
	if !center {
		return 0, 0, false
	}
	return SnapRobloxDirectPointerFallbackToCenter()
}

// persistentAcquireWanted reports whether an absolute motion sample in a
// joined experience should take the persistent grab: always under the
// always policy, only while the zoom lock is armed under the default zoom
// policy (re-acquire after a focus flap or an Alt re-arm).
func persistentAcquireWanted() bool {
	if !persistentEngageAllowed() {
		return false
	}
	return pointerCaptureAlways() || zoomLockArmed()
}

// acquirePersistentCapture takes the persistent host grab and seeds the
// logical integrator at the pointer position x, y (the held-RMB style grab),
// under both capture policies. Neither policy warps: the engine freezes a
// cursor origin only under UserInputService.MouseBehavior, which
// RobloxMainWindowMouseLocked reads directly, so a true getter is the
// centering authority (pointerLockSticky) and nothing on this getter-false
// path has a center to agree with. Publish happens only after the logical
// origin is ready; see the held-RMB acquisition ordering. A refused grab
// changes nothing and reports false.
func acquirePersistentCapture(x, y float32) bool {
	changed, err := pointerLockAtCursorSetter(true)
	if err != nil || !changed {
		return false
	}
	BeginRobloxDirectPointerFallback(x, y)
	persistentPointerCapture.Store(true)
	pointerCursorSetter(false)
	logging.Logger(logging.CatJNI).Info("[jni] persistent pointer capture acquired",
		"policy", pointerCapturePolicyName())
	return true
}

// releasePersistentCaptureAtCenter frees the zoom-lock grab once its unlock
// completed, leaving both cursors at the viewport center where the engine
// cursor reappears after first person. The zoom-out detent already re-seeded
// the logical pair there and the ordinary dispatcher keeps it as its last
// origin; the host pointer needs the matching move, because the grab is
// anchored where the lock armed (acquirePersistentCapture) and the X11
// unlock leaves the pointer at the grab anchor. Re-anchoring a live grab at
// the center warps it there without disturbing its non-sticky kind, so the
// first absolute sample after the release is continuous.
func releasePersistentCaptureAtCenter() {
	persistentPointerCapture.Store(false)
	ClearRobloxDirectPointerFallbackKeepLast()
	_, _ = pointerLockAtCenterSetter(true)
	_, _ = pointerLockAtCenterSetter(false)
	logging.Logger(logging.CatJNI).Info("[jni] zoom lock released; pointer free")
}

func pointerCapturePolicyName() string {
	if pointerCaptureAlways() {
		return "always"
	}
	return "zoom"
}

// pointerCaptureReleased is the operator's LeftAlt toggle. One physical
// LeftAlt press shows the host cursor and frees every grab; the next press
// hides the cursor and lets the next motion re-acquire. It survives focus
// changes by design; only an explicit press changes it, except for the
// Alt-Tab chord neutralize on focus loss (see pointerCaptureAltSnapshot).
var pointerCaptureReleased atomic.Bool

// altToggleConsumed tracks a LeftAlt physical gesture whose down edge was
// swallowed as a capture toggle. Repeats and the release of that same gesture
// are swallowed too; edges of a gesture that started before the feature
// became active (or after it deactivated) still route normally so Alt can
// never stick down or up in the engine across experience transitions.
var altToggleConsumed atomic.Bool

// pointerCaptureAltSnapshot is the pre-press {released, sticky} state saved
// on every consumed LeftAlt down. If X11 focus loss reports LeftAlt held,
// that press belonged to an Alt-Tab chord and the snapshot is restored so
// Alt-Tab stays capture-neutral instead of becoming a lasting toggle.
var pointerCaptureAltSnapshot struct {
	mu       sync.Mutex
	released bool
	sticky   bool
}

func savePointerCaptureAltSnapshot(released, sticky bool) {
	pointerCaptureAltSnapshot.mu.Lock()
	pointerCaptureAltSnapshot.released = released
	pointerCaptureAltSnapshot.sticky = sticky
	pointerCaptureAltSnapshot.mu.Unlock()
}

func loadPointerCaptureAltSnapshot() (released, sticky bool) {
	pointerCaptureAltSnapshot.mu.Lock()
	defer pointerCaptureAltSnapshot.mu.Unlock()
	return pointerCaptureAltSnapshot.released, pointerCaptureAltSnapshot.sticky
}

// pointerCapturePolicy is the TIPSY_MOUSE_CAPTURE default-on switch.
// Unset or any value other than an explicit false spelling enables the
// desktop persistent capture; 0/false/off/no (case-insensitive) restores the
// unmodified Android listener behavior.
//
// TIPSY_MOUSE_CAPTURE=always keeps the previous policy: the pointer is
// confined for the whole experience from its first motion. The default,
// zoom, confines only while the wheel-driven dead-center lock is armed
// (see zoomLock), so a zoomed-out third-person pointer is free and may leave
// the window exactly like the desktop client.
var pointerCapturePolicy struct {
	sync.Once
	enabled bool
	always  bool
}

func pointerCapturePolicyEnabled() bool {
	pointerCapturePolicy.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("TIPSY_MOUSE_CAPTURE"))) {
		case "0", "false", "off", "no":
			pointerCapturePolicy.enabled = false
		case "always":
			pointerCapturePolicy.enabled = true
			pointerCapturePolicy.always = true
		default:
			pointerCapturePolicy.enabled = true
		}
	})
	return pointerCapturePolicy.enabled
}

// pointerCaptureAlways reports the whole-experience confinement policy.
func pointerCaptureAlways() bool {
	return pointerCapturePolicyEnabled() && pointerCapturePolicy.always
}

// ResetPointerCapturePolicy makes the next policy lookup re-read
// TIPSY_MOUSE_CAPTURE and clears all capture toggle state. Test seam;
// production never changes policy within a process.
func ResetPointerCapturePolicy() {
	pointerCapturePolicy.Once = sync.Once{}
	pointerCapturePolicy.enabled = true
	pointerCapturePolicy.always = false
	resetPointerCaptureState()
}

// SetPointerCapturePolicyForTest pins the policy without the environment.
// Test seam only.
func SetPointerCapturePolicyForTest(enabled, always bool) {
	pointerCapturePolicy.Once = sync.Once{}
	pointerCapturePolicy.Do(func() {})
	pointerCapturePolicy.enabled = enabled
	pointerCapturePolicy.always = always
	resetPointerCaptureState()
}

// resetPointerCaptureState clears the live capture flags and the Alt-Tab
// snapshot without touching the environment policy. Input-path and target
// resets call it so a reconfigured stream never inherits a stale grab.
func resetPointerCaptureState() {
	engineLock.known.Store(false)
	engineLock.locked.Store(false)
	engineBehavior.known.Store(false)
	engineBehavior.value.Store(0)
	storeEngineAnchorLock(false, 0, 0)
	persistentPointerCapture.Store(false)
	pointerCaptureReleased.Store(false)
	altToggleConsumed.Store(false)
	savePointerCaptureAltSnapshot(false, false)
	resetZoomLock()
}

// pointerCaptureAvailable reports whether the desktop persistent capture
// feature can engage on this stream: policy enabled, desktop mouse identity
// (never touch), the direct pointer path selected, and the direct mouse-move
// native wired. It does not consider experience presence or the Alt toggle.
func pointerCaptureAvailable() bool {
	if !pointerCapturePolicyEnabled() || pointerDeviceIsTouch() {
		return false
	}
	if pointerDeliveryPath() == PointerPathGameActivity {
		return false
	}
	return robloxDirectMoveTargetLive()
}

// inExperience reports whether the engine's latest onGameLoaded announcement
// named a joined experience (non-zero place id) rather than Home (0) or no
// announcement yet. Persistent capture engages only in an experience so Home
// and Login keep absolute UI navigation.
func inExperience() bool {
	_, placeID := NativeHelperGameLoaded()
	return placeID != 0
}

// persistentEngageAllowed reports whether a free absolute motion may acquire
// the persistent anchored grab right now.
func persistentEngageAllowed() bool {
	return pointerCaptureAvailable() && inExperience() && !pointerCaptureReleased.Load()
}

// persistentAltActive reports whether LeftAlt edges are capture toggles
// rather than engine keys: the feature is available and the client is in an
// experience. On Home the Alt key always routes normally.
func persistentAltActive() bool {
	return pointerCaptureAvailable() && inExperience()
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

// A physical key gesture must keep the listener that received its initial
// down. In particular, nativePassKeyEvent for slash can synchronously make
// Roblox focus its RbxKeyboard editor. Re-evaluating editor focus on the
// matching up would then swallow that up, leaving the native listener with a
// permanently held slash and making later chat-open presses unreliable.
//
// The raw core-X11 keycode is the stable per-physical-key identity. X11 owns
// the [0,255] range, and its repeat bridge already guarantees one initial
// down, zero or more repeated downs, and one release (including focus-loss
// releases). We retain only the selected route, never key text.
type x11KeyGestureOwner uint8

const (
	x11KeyOwnerNone x11KeyGestureOwner = iota
	x11KeyOwnerEditor
	x11KeyOwnerGameActivity
	x11KeyOwnerDirect
	x11KeyOwnerBoth
)

var x11KeyGestures struct {
	sync.Mutex
	owners [256]x11KeyGestureOwner
}

func x11KeyGestureSlot(scanCode int32) (int, bool) {
	if scanCode < 0 || scanCode >= int32(len(x11KeyGestures.owners)) {
		return 0, false
	}
	return int(scanCode), true
}

func rememberedX11KeyOwner(scanCode int32) (x11KeyGestureOwner, bool) {
	slot, ok := x11KeyGestureSlot(scanCode)
	if !ok {
		return x11KeyOwnerNone, false
	}
	x11KeyGestures.Lock()
	owner := x11KeyGestures.owners[slot]
	x11KeyGestures.Unlock()
	return owner, owner != x11KeyOwnerNone
}

func rememberX11KeyOwner(scanCode int32, owner x11KeyGestureOwner) {
	slot, ok := x11KeyGestureSlot(scanCode)
	if !ok {
		return
	}
	x11KeyGestures.Lock()
	x11KeyGestures.owners[slot] = owner
	x11KeyGestures.Unlock()
}

func takeX11KeyOwner(scanCode int32) (x11KeyGestureOwner, bool) {
	slot, ok := x11KeyGestureSlot(scanCode)
	if !ok {
		return x11KeyOwnerNone, false
	}
	x11KeyGestures.Lock()
	owner := x11KeyGestures.owners[slot]
	x11KeyGestures.owners[slot] = x11KeyOwnerNone
	x11KeyGestures.Unlock()
	return owner, owner != x11KeyOwnerNone
}

func selectedX11SurfaceKeyOwner() x11KeyGestureOwner {
	switch keyboardDeliveryPath() {
	case KeyboardPathGameActivity:
		return x11KeyOwnerGameActivity
	case KeyboardPathBoth:
		return x11KeyOwnerBoth
	default:
		return x11KeyOwnerDirect
	}
}

func dispatchX11KeyToOwner(owner x11KeyGestureOwner, ev x11.InputEvent) {
	switch owner {
	case x11KeyOwnerEditor:
		// The release still belongs to the editor even if Enter or an engine
		// hideKeyboard call deactivated it after the down. Never leak an
		// unmatched release to a SurfaceView listener that saw no down.
		DispatchRobloxTextKey(ev.KeyCode, ev.KeyPressed)
	case x11KeyOwnerGameActivity:
		if ev.KeyCode > 0 {
			dispatchGameActivityKey(ev.KeyCode, ev.ScanCode, ev.KeyPressed, ev.RepeatCount)
		}
	case x11KeyOwnerDirect:
		dispatchRobloxDirectKey(ev.ScanCode, ev.KeyCode, ev.KeyPressed, ev.RepeatCount)
	case x11KeyOwnerBoth:
		if ev.KeyCode > 0 {
			dispatchGameActivityKey(ev.KeyCode, ev.ScanCode, ev.KeyPressed, ev.RepeatCount)
		}
		dispatchRobloxDirectKey(ev.ScanCode, ev.KeyCode, ev.KeyPressed, ev.RepeatCount)
	}
}

// handleX11InputEvent is the x11→GameActivity conversion for one captured
// real X11 event. Split from bindX11InputBridge so tests can drive the
// production mapping directly (X→x, Y→y must stay distinct end to end;
// the axis-slot bug class regresses here first).
func handleX11InputEvent(ev x11.InputEvent) {
	switch ev.Kind {
	case x11.InputFocus:
		// Pads share the window focus gate: quiet while unfocused, UP
		// synthesis + zero axes on focus loss. Keyboard/mouse/text arms
		// below are untouched.
		gamepadNoteFocus(ev.FocusGained)
		if !ev.FocusGained {
			// Host focus loss already ungrabs in X11. Do not send an explicit
			// unlock here: that would clear the sticky first-person grab so
			// returning to the window could not recapture without a click.
			if ev.FocusAltHeld || altToggleConsumed.Load() {
				// Alt-Tab chord evidence: the Alt press belonged to the window-
				// manager chord, not a standalone capture toggle. Restore the
				// pre-press snapshot so Alt-Tab stays capture-neutral.
				//
				// A consumed LeftAlt gesture that is still open at a focus
				// boundary is the same evidence arriving late: the window
				// manager grabbed the keyboard for its switcher, so the
				// matching release will never reach Tipsy and FocusAltHeld can
				// already read false by the time the focus-out lands. Without
				// this a swallowed Alt leaves capture released for the rest of
				// the session, which is how a measured run lost every scroll
				// probe it took.
				released, sticky := loadPointerCaptureAltSnapshot()
				pointerCaptureReleased.Store(released)
				pointerLockSticky.Store(sticky)
				rmbPointerFallback.Store(false)
				persistentPointerCapture.Store(false)
				altToggleConsumed.Store(false)
				ClearRobloxDirectPointerFallback()
				// Reapply the restored cursor now: the window keeps its cursor
				// setting while unfocused, and focus gain reaffirms it below.
				if released {
					pointerCursorSetter(true)
				} else {
					pointerCursorSetter(false)
				}
			} else {
				// Ordinary focus loss preserves the operator Alt toggle and the
				// sticky centered request; only transient anchored streams drop.
				rmbPointerFallback.Store(false)
				persistentPointerCapture.Store(false)
				altToggleConsumed.Store(false)
				ClearRobloxDirectPointerFallback()
			}
		} else if pointerCaptureReleased.Load() {
			// Intentional operator release: stay free and keep the host cursor
			// visible even if a sticky centered request is pending. Toggling
			// back re-arms capture on the next motion.
			pointerCursorSetter(true)
		} else if engineAnchorActive() {
			// The engine still holds LockCurrentPosition, so X11's focus-loss
			// ungrab is re-acquired here, anchored at the frozen origin.
			engageEngineAnchorLock("focus re-acquire", 0, 0, false)
		} else if pointerLockSticky.Load() {
			rmbPointerFallback.Store(false)
			persistentPointerCapture.Store(false)
			pointerCursorSetter(false)
			_, _ = pointerLockSetter(true)
		} else {
			pointerCursorSetter(false)
			obs := observeEngineLock()
			switch {
			case obs.wantsCenter():
				pointerLockSticky.Store(true)
				_, _ = pointerLockSetter(true)
			case obs.wantsAnchor():
				engageEngineAnchorLock("focus re-acquire", 0, 0, false)
			}
		}
		DispatchGameActivityFocus(ev.FocusGained)
	case x11.InputKey:
		// LeftAlt is the desktop persistent-capture toggle while the feature
		// is live in a joined experience. The press toggles every host grab
		// and the host cursor; repeats and the release of that same physical
		// gesture are swallowed so Alt never sticks in the engine. On Home,
		// or with the feature opted out, Alt routes like any other key.
		if ev.KeyCode == leftAltKeyCode {
			if persistentAltActive() {
				if ev.KeyPressed && ev.RepeatCount == 0 {
					// Drop any stale pre-activation route, snapshot for a
					// possible Alt-Tab chord, then toggle.
					takeX11KeyOwner(ev.ScanCode)
					savePointerCaptureAltSnapshot(pointerCaptureReleased.Load(), pointerLockSticky.Load())
					if pointerCaptureReleased.Load() {
						pointerCaptureReleased.Store(false)
						pointerCursorSetter(false)
						logging.Logger(logging.CatJNI).Info("[jni] pointer capture re-armed by Alt toggle")
						// The engine outranks every heuristic: if MouseBehavior
						// is still LockCenter, re-establish the centered sticky
						// grab on the toggle itself rather than leaving the
						// engine unheeded until the next motion sample. The
						// state is available because the release suppressed
						// grabbing, never observing. LockCurrentPosition is
						// honoured the same way, anchored at the frozen origin.
						obs := observeEngineLock()
						switch {
						case obs.wantsCenter():
							pointerLockSticky.Store(true)
							_, _ = pointerLockSetter(true)
							noteEngineStickyEngaged("alt re-arm", false)
						case obs.wantsAnchor():
							engageEngineAnchorLock("alt re-arm", 0, 0, false)
						}
					} else {
						pointerCaptureReleased.Store(true)
						pointerLockSticky.Store(false)
						rmbPointerFallback.Store(false)
						persistentPointerCapture.Store(false)
						// engineAnchorLock is deliberately kept: the engine still
						// holds LockCurrentPosition, and the toggle suppresses the
						// grab, never the engine's own state. The re-arm above
						// re-acquires it on the toggle itself.
						ClearRobloxDirectPointerFallback()
						_, _ = pointerLockSetter(false)
						pointerCursorSetter(true)
						logging.Logger(logging.CatJNI).Info("[jni] pointer capture released by Alt toggle")
					}
					altToggleConsumed.Store(true)
					return
				}
				if altToggleConsumed.Load() {
					if !ev.KeyPressed {
						altToggleConsumed.Store(false)
					}
					takeX11KeyOwner(ev.ScanCode)
					return
				}
				// Repeat/up of a gesture that started before activation (Alt
				// held across the Home→experience join): fall through to the
				// normal owner routing so the engine's down gets its up.
			} else if altToggleConsumed.Load() {
				// Gesture started active, feature deactivated mid-hold (left
				// the experience while Alt was down): swallow the unmatched
				// release instead of forwarding a down the engine never saw.
				if !ev.KeyPressed {
					altToggleConsumed.Store(false)
				}
				takeX11KeyOwner(ev.ScanCode)
				return
			}
		}
		// Repeat downs and the final up stay on the listener selected by the
		// initial physical down. A non-repeat down always starts/replaces a
		// gesture, recovering honestly if an earlier release was lost.
		if ev.KeyPressed && ev.RepeatCount > 0 {
			if owner, ok := rememberedX11KeyOwner(ev.ScanCode); ok {
				dispatchX11KeyToOwner(owner, ev)
				return
			}
		} else if !ev.KeyPressed {
			if owner, ok := takeX11KeyOwner(ev.ScanCode); ok {
				dispatchX11KeyToOwner(owner, ev)
				return
			}
		}
		// A genuine engine showKeyboard call transfers focus to the APK's
		// RbxKeyboard editor. Preserve X11's physical edge as its own event,
		// but give that focused editor first refusal so Backspace/arrows/Enter
		// edit the textbox and do not also reach the SurfaceView listener.
		// With no engine-owned textbox session, the existing physical path is
		// byte-for-byte unchanged.
		if DispatchRobloxTextKey(ev.KeyCode, ev.KeyPressed) {
			if ev.KeyPressed {
				rememberX11KeyOwner(ev.ScanCode, x11KeyOwnerEditor)
			}
			return
		}
		owner := selectedX11SurfaceKeyOwner()
		if ev.KeyPressed {
			// Store before the native call: slash down may synchronously invoke
			// showKeyboard and change editor focus before this call returns.
			rememberX11KeyOwner(ev.ScanCode, owner)
		}
		// The supplied APK's final Roblox listener calls the direct key
		// native. GameActivity remains a control path; `both` is solely a
		// diagnostic to distinguish target wiring from listener selection.
		dispatchX11KeyToOwner(owner, ev)
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
			switch {
			case ev.PointerAction == x11.PointerMove || ev.Relative:
				// Captured relative motion is always a MOVE for the APK
				// listener. The C ring uses a=3 for that path; decoding it as
				// PointerDown would drop every camera delta as an unsupported
				// button while the host grab still held the cursor.
				//
				// Leaving-experience safety: a persistent grab held across the
				// experience→Home transition releases here. A relative sample
				// from the dying grab is consumed (its anchor coordinates are
				// not a real position); an absolute move falls through to the
				// normal getter/absolute handling below.
				if persistentPointerCapture.Load() && !inExperience() {
					persistentPointerCapture.Store(false)
					rmbPointerFallback.Store(false)
					ClearRobloxDirectPointerFallback()
					pointerLockSticky.Store(false)
					storeEngineAnchorLock(false, 0, 0)
					_, _ = pointerLockSetter(false)
					if ev.Relative {
						return
					}
				}
				obs := observeEngineLock()
				if !pointerCaptureReleased.Load() && obs.available {
					switch {
					case obs.wantsCenter():
						// Getter-true converts any anchored mode (held-RMB or
						// persistent) to the centered sticky grab. The persistent
						// origin is preserved so centered deltas continue from the
						// established logical cursor.
						converting := false
						if persistentPointerCapture.Swap(false) {
							ClearRobloxDirectPointerFallbackKeepLast()
							converting = true
						}
						if rmbPointerFallback.Swap(false) {
							ClearRobloxDirectPointerFallback()
							converting = true
						}
						if !ev.Relative {
							// This is the official generic-listener order: observe the
							// true getter, request capture, consume this transition move,
							// then deliver later captured relative-axis events.
							engaged := !pointerLockSticky.Swap(true)
							_, _ = pointerLockSetter(true)
							pointerCursorSetter(false)
							if engaged {
								noteEngineStickyEngaged("absolute move", converting)
							}
							return
						}
						if converting {
							engaged := !pointerLockSticky.Swap(true)
							_, _ = pointerLockSetter(true)
							pointerCursorSetter(false)
							if engaged {
								noteEngineStickyEngaged("captured motion", converting)
							}
						}
						DispatchRobloxDirectPointerDelta(ev.X, ev.Y, ev.DeltaX, ev.DeltaY)
						return
					case obs.wantsAnchor():
						// LockCurrentPosition: the engine froze its cursor where it
						// already was, so the grab is anchored at that point and the
						// captured stream continues from it. Nothing is
						// center-converted; warping here would desync the engine's
						// frozen origin from the host pointer.
						edge := "captured motion"
						if !ev.Relative {
							// The transition sample is consumed exactly like the
							// official capture request above.
							engageEngineAnchorLock("absolute move", ev.X, ev.Y, true)
							return
						}
						if !engineAnchorActive() {
							engageEngineAnchorLock(edge, 0, 0, false)
						} else if _, _, held := RobloxDirectFallbackPosition(); !held {
							// The integrator was cleared (focus loss, a target
							// re-wire) while the engine's state survived it; re-seed
							// it at the frozen origin so the stream keeps integrating.
							if x, y, ok := engineAnchorPoint(0, 0, false); ok {
								BeginRobloxDirectPointerFallback(x, y)
							}
						}
						// The anchored stream advances the same unbounded logical
						// pair the held-RMB fallback does, so the engine cursor
						// stays where the engine froze it while look continues. The
						// remembered re-acquire point is deliberately left alone:
						// the X11 grab's anchor does not move when the integrator
						// drifts, so a focus flap or an Alt re-arm must keep taking
						// the grab where the host pointer is, and the frozen origin
						// is fixed by the engine for the whole lock.
						DispatchRobloxDirectPointerFallbackDelta(ev.DeltaX, ev.DeltaY)
						return
					case engineAnchorActive():
						// The engine left LockCurrentPosition for Default and the edge
						// that ended it did not release synchronously, so the release
						// lands on this transition sample, which is consumed. Where the
						// cursor belongs depends on which gesture ended: a first-person
						// zoom-out centers (the wheel path marks it), and everything
						// else -- an RMB camera-look release above all -- restores the
						// engine's own frozen origin, because that is where its cursor
						// reappears. Centering unconditionally here teleported the
						// pointer to the middle of the window on the first pixel of
						// motion after every RMB release.
						if engineAnchorReleasesAtCenter() {
							releaseEngineLockAtCenter("captured motion")
						} else {
							releaseEngineLockAtOrigin("captured motion")
						}
						return
					}
				}
				if ev.Relative && rmbPointerFallback.Load() {
					if pointerCaptureReleased.Load() {
						// Alt-toggled mid-gesture: the toggle already ungrabs;
						// this sample is stale.
						rmbPointerFallback.Store(false)
						ClearRobloxDirectPointerFallback()
						return
					}
					// The native getter was measured false after this held RMB
					// began, but the host fallback has an acquired grab. Relative
					// X11 movement is therefore real camera motion, not a stale
					// generic event. Mirror the APK listener's cached logical
					// coordinate: integrate axes 27/28 before the descriptor-exact
					// native call, while preserving their exact dx/dy arguments.
					DispatchRobloxDirectPointerFallbackDelta(ev.DeltaX, ev.DeltaY)
					return
				}
				if ev.Relative && persistentPointerCapture.Load() {
					if pointerCaptureReleased.Load() || !inExperience() {
						persistentPointerCapture.Store(false)
						ClearRobloxDirectPointerFallback()
						resetZoomLock()
						return
					}
					if zoomLockNoteMotion() && !pointerCaptureAlways() {
						// The unlock completed: under the zoom policy the pointer
						// goes free again, sitting at the center where the engine
						// cursor reappeared. This relative sample is the
						// transition and is consumed like the acquire sample.
						releasePersistentCaptureAtCenter()
						return
					}
					DispatchRobloxDirectPointerFallbackDelta(ev.DeltaX, ev.DeltaY)
					return
				}
				if ev.Relative {
					// The APK's captured-pointer listener checks the getter before
					// dispatch and releases/consumes the event when it turns false.
					rmbPointerFallback.Store(false)
					persistentPointerCapture.Store(false)
					ClearRobloxDirectPointerFallback()
					pointerLockSticky.Store(false)
					_, _ = pointerLockSetter(false)
					return
				}
				// Absolute move with no centered lock: acquire the persistent
				// grab in a joined experience when the policy wants it (always,
				// or zoom lock armed after a focus flap / Alt re-arm). The
				// transition sample is consumed exactly like the official
				// capture request.
				if persistentAcquireWanted() && !rmbPointerFallback.Load() &&
					!persistentPointerCapture.Load() && !pointerLockSticky.Load() {
					if acquirePersistentCapture(ev.X, ev.Y) {
						return
					}
					// Grab rejected: fall through to absolute delivery. Begin
					// was never called, so no integrator state exists to clear;
					// a full clear here would wipe the ordinary dispatcher's
					// last origin and break move continuity.
				}
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
			case ev.PointerAction == x11.PointerDown && ev.Button == 3:
				// While persistent-captured, RMB keeps the old held-RMB contract
				// in logical space (LockCurrentPosition): the press pins the
				// logical cursor on-view and anchors it, the drag integrates
				// from that point, and the release restores it so the engine
				// cursor comes back to where the operator clicked. No second
				// grab; the stream is retained. The engine's own anchored lock
				// is the same captured stream, so it takes the same contract.
				if persistentPointerCapture.Load() || engineAnchorActive() {
					if _, _, ok := BeginRobloxDirectCapturedSecondary(); ok {
						DispatchRobloxDirectButtonCaptured(ev.PointerAction, ev.Button)
					} else {
						DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
					}
					obs := observeEngineLock()
					if !pointerCaptureReleased.Load() {
						switch {
						case obs.wantsCenter():
							if persistentPointerCapture.Swap(false) {
								ClearRobloxDirectPointerFallbackKeepLast()
							}
							rmbPointerFallback.Store(false)
							pointerLockSticky.Store(true)
							_, _ = pointerLockSetter(true)
							pointerCursorSetter(false)
						case obs.wantsAnchor():
							// The engine already froze the cursor at the press
							// point, so the persistent stream simply becomes
							// engine-owned. The remembered secondary anchor is the
							// frozen origin and is preserved.
							rmbPointerFallback.Store(false)
							adoptEngineAnchorLock("secondary down")
						}
					}
					break
				}
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
				obs := observeEngineLock()
				logging.Logger(logging.CatJNI).Info("[jni] pointer lock after secondary down",
					"available", obs.available, "locked", obs.locked)
				switch {
				case obs.wantsCenter() && !pointerCaptureReleased.Load():
					rmbPointerFallback.Store(false)
					pointerLockSticky.Store(true)
					_, _ = pointerLockSetter(true)
					pointerCursorSetter(false)
				case obs.wantsAnchor() && !pointerCaptureReleased.Load():
					// The engine already froze the cursor at the click, so no
					// held-RMB host fallback is needed: that would be a guess on
					// top of a fact. The click is also pinned as the captured
					// secondary's press anchor, so the matching release lands back
					// at it exactly as it does inside a captured stream.
					rmbPointerFallback.Store(false)
					if engageEngineAnchorLock("secondary down", ev.X, ev.Y, true) {
						BeginRobloxDirectCapturedSecondary()
					}
				case obs.available && !obs.locked && !pointerCaptureReleased.Load():
					// The observed in-experience client leaves the exact lock getter
					// false for ordinary RMB camera look. Deliver the edge first,
					// then use one held-RMB host capture anchored at the click so
					// the desktop cursor does not teleport to the window center.
					// This grab is not sticky: Alt-Tab already synthesizes RMB up.
					// A grab failure is only diagnostic: the already-delivered
					// button edge remains live.
					changed, err := pointerLockAtCursorSetter(true)
					if err == nil && changed {
						BeginRobloxDirectPointerFallback(ev.X, ev.Y)
						// Publish fallback only after its virtual origin is ready. The
						// X11 bridge normally serializes this stream, but this ordering
						// also makes a concurrent drain unable to see an active fallback
						// with no logical accumulator.
						rmbPointerFallback.Store(true)
						pointerCursorSetter(false)
						logging.Logger(logging.CatJNI).Info("[jni] held-RMB pointer-lock fallback acquired")
					}
				}
			case ev.PointerAction == x11.PointerUp && ev.Button == 3:
				// While persistent-captured, the release lands at the press
				// anchor and re-seeds the integrator there (the old fallback's
				// release-at-grab-anchor, in logical space); the stream
				// continues. Only a getter-true edge converts it to centered.
				if persistentPointerCapture.Load() || engineAnchorActive() {
					if _, _, ok := EndRobloxDirectCapturedSecondary(); ok {
						DispatchRobloxDirectButtonCaptured(ev.PointerAction, ev.Button)
					} else if _, _, ok := RobloxDirectFallbackPosition(); ok {
						DispatchRobloxDirectButtonCaptured(ev.PointerAction, ev.Button)
					} else {
						DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
					}
					obs := observeEngineLock()
					if !pointerCaptureReleased.Load() {
						switch {
						case obs.wantsCenter():
							if persistentPointerCapture.Swap(false) {
								ClearRobloxDirectPointerFallbackKeepLast()
							}
							pointerLockSticky.Store(true)
							_, _ = pointerLockSetter(true)
							pointerCursorSetter(false)
						case obs.wantsAnchor():
							// The engine still owns the frozen cursor; the release
							// is an ordinary button edge inside the anchored stream.
							adoptEngineAnchorLock("secondary up")
						case engineAnchorActive():
							// The engine left LockCurrentPosition for Default on
							// the release itself, which is the gesture its own
							// RMB camera look ties the lock to. Both cursors go
							// back to the engage-time frozen origin on this edge;
							// deferring the release to the next motion sample
							// would instead release at the center.
							releaseEngineLockAtOrigin("secondary up")
						}
					}
					break
				}
				// Discard the virtual captured origin before seeding the ordinary
				// direct dispatcher from this real anchored release. This makes the
				// next two physical post-release motions start from actual X11
				// coordinates, not an accumulated camera-look coordinate.
				ClearRobloxDirectPointerFallback()
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
				if pointerCaptureReleased.Load() {
					// Alt-toggled mid-hold: the toggle already ungrabs; stay
					// free and keep the host cursor visible.
					rmbPointerFallback.Store(false)
					pointerLockSticky.Store(false)
					_, _ = pointerLockSetter(false)
					pointerCursorSetter(true)
					break
				}
				// Re-read after delivering the edge, matching the engine-owned
				// handshake. Ordinary RMB camera look turns false and releases at
				// the anchor; first-person/shift-lock remains captured.
				obs := observeEngineLock()
				rmbPointerFallback.Store(false)
				switch {
				case obs.wantsCenter():
					pointerCursorSetter(false)
				case obs.wantsAnchor():
					engageEngineAnchorLock("secondary up", ev.X, ev.Y, true)
				case engineAnchorActive():
					// The engine left LockCurrentPosition for Default on this edge.
					// Its cursor reappears at the frozen origin, so both cursors are
					// restored there, not at the viewport center.
					releaseEngineLockAtOrigin("secondary up")
				default:
					pointerLockSticky.Store(false)
					_, _ = pointerLockSetter(false)
				}
			default:
				// Captured primary (and other) button edges land at the logical
				// cursor so in-experience UI clicks agree with the software
				// cursor. A getter-true edge converts to centered immediately
				// instead of waiting for the next motion.
				if persistentPointerCapture.Load() &&
					(ev.PointerAction == x11.PointerDown || ev.PointerAction == x11.PointerUp) {
					if _, _, ok := RobloxDirectFallbackPosition(); ok {
						DispatchRobloxDirectButtonCaptured(ev.PointerAction, ev.Button)
					} else {
						DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
					}
					obs := observeEngineLock()
					if !pointerCaptureReleased.Load() {
						switch {
						case obs.wantsCenter():
							if persistentPointerCapture.Swap(false) {
								ClearRobloxDirectPointerFallbackKeepLast()
							}
							pointerLockSticky.Store(true)
							_, _ = pointerLockSetter(true)
							pointerCursorSetter(false)
						case obs.wantsAnchor():
							adoptEngineAnchorLock("captured button")
						}
					}
					break
				}
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
			}
		}
	case x11.InputScroll:
		// The final APK mouse listener sends ACTION_SCROLL only through the
		// direct NativeInputInterface wheel native. GameActivity's replaced
		// touch listener has no faithful wheel equivalent.
		path := pointerDeliveryPath()
		if path != PointerPathDirect && path != PointerPathBoth {
			break
		}
		// Captured wheel detents report the logical cursor, not X11's fixed
		// grab anchor, so zoomed-out UI hover/scroll agrees with the software
		// cursor. Centered LockCenter detents report the look origin. Under
		// persistent capture the dead-center heuristic may re-seed the logical
		// cursor first (see zoomLock); the detent is then delivered there.
		//
		// The heuristic is gated on the enum authority being *unavailable*.
		// While the engine is answering for real, Tipsy never guesses: the
		// wheel run is retired, not deleted, so an experience whose behavior
		// the authority cannot read at all still gets it as the honest
		// fallback.
		wx, wy := ev.X, ev.Y
		if persistentPointerCapture.Load() || rmbPointerFallback.Load() {
			if fx, fy, ok := RobloxDirectFallbackPosition(); ok {
				wx, wy = fx, fy
			}
		} else if pointerLockSticky.Load() {
			if lx, ly, ok := robloxDirectLastPosition(); ok {
				wx, wy = lx, ly
			}
		}
		if !pointerLockSticky.Load() && !engineAnchorActive() &&
			!engineBehaviorAuthoritative() && persistentEngageAllowed() {
			if cx, cy, ok := zoomLockWheel(ev.X, ev.Y, ev.ScrollY); ok {
				wx, wy = cx, cy
			}
		}
		DispatchRobloxDirectScroll(wx, wy, ev.ScrollX, ev.ScrollY)
		// Exactly one engine-getter probe per delivered wheel event. A detent
		// that completes a first-person zoom converts to the centered sticky
		// grab on that same detent instead of waiting for the next motion;
		// a zoom-out detent that clears the getter releases the centered grab
		// on the detent itself. Getter-false from an anchored stream preserves
		// that stream: persistent capture is the deliberate desktop fallback
		// and must stay confined even when the engine never reports LockCenter.
		// LockCurrentPosition is the same idea one level down: the engine froze
		// its cursor where it was, so the detent is already delivered at that
		// point and the grab is anchored there rather than centered.
		//
		// The probe runs before the LeftAlt gate on purpose. Suppressing the
		// *grab* while the operator has toggled capture off is correct;
		// suppressing the *observation* is not, because a real desktop Alt
		// (window switching) then costs every scroll probe in the session and
		// leaves the engine's authority untracked until the next motion. Only
		// the acting half below is gated.
		obs := observeEngineLock()
		if pointerCaptureReleased.Load() || !obs.available {
			break
		}
		switch {
		case obs.wantsCenter():
			if pointerLockSticky.Load() {
				break
			}
			if persistentPointerCapture.Swap(false) {
				ClearRobloxDirectPointerFallbackKeepLast()
				// The engine owns centering from here (LockCenter reported).
				resetZoomLock()
			}
			if rmbPointerFallback.Swap(false) {
				ClearRobloxDirectPointerFallback()
			}
			pointerLockSticky.Store(true)
			_, _ = pointerLockSetter(true)
			pointerCursorSetter(false)
			noteEngineStickyEngaged("wheel detent", true)
		case obs.wantsAnchor():
			// Treat the engine as locked, but anchored: the detent is already
			// delivered at wx,wy, which is the logical cursor the engine
			// froze, so no move and no centering is needed here at all.
			if engineAnchorActive() {
				if ev.ScrollY < 0 {
					// A zoom-out detent is the first-person exit gesture, but
					// the engine's value lags it, so this probe can still read
					// LockCurrentPosition and the real release lands on the
					// next motion sample. Mark it so that deferred release
					// centers exactly like the synchronous one below.
					markEngineAnchorReleaseAtCenter()
				}
				break
			}
			if persistentPointerCapture.Swap(false) {
				// The engine owns the lock from here; the wheel guess is
				// retired.
				resetZoomLock()
			}
			rmbPointerFallback.Store(false)
			engageEngineAnchorLock("wheel detent", wx, wy, true)
		case engineAnchorActive():
			// The engine left LockCurrentPosition for Default: its cursor
			// reappears at the center, so both cursors go back there together
			// on the detent itself.
			releaseEngineLockAtCenter("wheel detent")
		case pointerLockSticky.Load():
			// LockCenter back to Default. The engine cursor reappears at the
			// center, so both cursors go back there together rather than the
			// host pointer springing back to wherever the grab was taken.
			releaseEngineLockAtCenter("wheel detent")
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
	jvalueSetI(&sl[0], C.jint(a))
	jvalueSetI(&sl[1], C.jint(b))
	v, ok := vm.dispatch(idToJobject(objID), class, name, sig, &sl[0])
	return uintptr(v), ok
}

// testEventGetterIII packs three jint arguments
// (e.g. getHistoricalAxisValue(III)F).
func testEventGetterIII(vm *VM, objID int64, class, name, sig string, a, b, c int32) (uintptr, bool) {
	sl := make([]C.jvalue, 3)
	jvalueSetI(&sl[0], C.jint(a))
	jvalueSetI(&sl[1], C.jint(b))
	jvalueSetI(&sl[2], C.jint(c))
	v, ok := vm.dispatch(idToJobject(objID), class, name, sig, &sl[0])
	return uintptr(v), ok
}
