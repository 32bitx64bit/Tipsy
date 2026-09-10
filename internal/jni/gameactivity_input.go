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
// must recapture without waiting for another click.
var pointerLockSticky atomic.Bool

// pointerLockSetter is a test seam for the host boundary. Production never
// replaces it; retaining the call behind this seam lets tests prove that a
// rejected fallback preserves Roblox's button edges. First-person / shift-lock
// uses the centered grab; held-RMB camera look uses the cursor-anchor grab.
var pointerLockSetter = x11.SetPointerLock
var pointerLockAtCursorSetter = x11.SetPointerLockAtCursor

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
		if !ev.FocusGained {
			// Host focus loss already ungrabs in X11. Do not send an explicit
			// unlock here: that would clear the sticky first-person grab so
			// returning to the window could not recapture without a click.
			rmbPointerFallback.Store(false)
			ClearRobloxDirectPointerFallback()
		} else if pointerLockSticky.Load() {
			rmbPointerFallback.Store(false)
			_, _ = pointerLockSetter(true)
		} else {
			locked, available := RobloxMainWindowMouseLocked()
			if available && locked {
				pointerLockSticky.Store(true)
				_, _ = pointerLockSetter(true)
			}
		}
		DispatchGameActivityFocus(ev.FocusGained)
	case x11.InputKey:
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
				locked, available := RobloxMainWindowMouseLocked()
				if available && locked {
					if rmbPointerFallback.Swap(false) {
						ClearRobloxDirectPointerFallback()
					}
					if !ev.Relative {
						// This is the official generic-listener order: observe the
						// true getter, request capture, consume this transition move,
						// then deliver later captured relative-axis events.
						pointerLockSticky.Store(true)
						_, _ = pointerLockSetter(true)
						return
					}
					DispatchRobloxDirectPointerDelta(ev.X, ev.Y, ev.DeltaX, ev.DeltaY)
					return
				}
				if ev.Relative && rmbPointerFallback.Load() {
					// The native getter was measured false after this held RMB
					// began, but the host fallback has an acquired grab. Relative
					// X11 movement is therefore real camera motion, not a stale
					// generic event. Mirror the APK listener's cached logical
					// coordinate: integrate axes 27/28 before the descriptor-exact
					// native call, while preserving their exact dx/dy arguments.
					DispatchRobloxDirectPointerFallbackDelta(ev.DeltaX, ev.DeltaY)
					return
				}
				if ev.Relative {
					// The APK's captured-pointer listener checks the getter before
					// dispatch and releases/consumes the event when it turns false.
					rmbPointerFallback.Store(false)
					ClearRobloxDirectPointerFallback()
					pointerLockSticky.Store(false)
					_, _ = pointerLockSetter(false)
					return
				}
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
			case ev.PointerAction == x11.PointerDown && ev.Button == 3:
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
				locked, available := RobloxMainWindowMouseLocked()
				logging.Logger(logging.CatJNI).Info("[jni] pointer lock after secondary down",
					"available", available, "locked", locked)
				if available && locked {
					rmbPointerFallback.Store(false)
					pointerLockSticky.Store(true)
					_, _ = pointerLockSetter(true)
				} else if available {
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
						logging.Logger(logging.CatJNI).Info("[jni] held-RMB pointer-lock fallback acquired")
					}
				}
			case ev.PointerAction == x11.PointerUp && ev.Button == 3:
				// Discard the virtual captured origin before seeding the ordinary
				// direct dispatcher from this real anchored release. This makes the
				// next two physical post-release motions start from actual X11
				// coordinates, not an accumulated camera-look coordinate.
				ClearRobloxDirectPointerFallback()
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
				// Re-read after delivering the edge, matching the engine-owned
				// handshake. Ordinary RMB camera look turns false and releases at
				// the anchor; first-person/shift-lock remains captured.
				locked, available := RobloxMainWindowMouseLocked()
				rmbPointerFallback.Store(false)
				if !available || !locked {
					pointerLockSticky.Store(false)
					_, _ = pointerLockSetter(false)
				}
			default:
				DispatchRobloxDirectPointer(ev.PointerAction, ev.X, ev.Y, ev.Button)
			}
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
