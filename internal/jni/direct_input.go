// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "direct_input.h"
*/
import "C"

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// PointerDeliveryPath selects which descriptor-proven pointer surface receives
// each real X11 event. The desktop default is the APK's final direct Roblox
// listener: it accepts ordinary mouse hover as well as button edges. The
// GameActivity touch listener remains available as an explicit A/B control.
type PointerDeliveryPath uint8

const (
	PointerPathGameActivity PointerDeliveryPath = iota
	PointerPathDirect
	PointerPathBoth
)

func (p PointerDeliveryPath) String() string {
	switch p {
	case PointerPathDirect:
		return "direct"
	case PointerPathBoth:
		return "both"
	default:
		return "gameactivity"
	}
}

var pointerPath struct {
	sync.Once
	value PointerDeliveryPath
}

func pointerDeliveryPath() PointerDeliveryPath {
	pointerPath.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("TIPSY_INPUT_PATH"))) {
		case "gameactivity":
			pointerPath.value = PointerPathGameActivity
		case "both":
			pointerPath.value = PointerPathBoth
		default:
			pointerPath.value = PointerPathDirect
		}
	})
	return pointerPath.value
}

// PointerInputPath reports the selected A/B arm.
func PointerInputPath() PointerDeliveryPath { return pointerDeliveryPath() }

// ResetPointerInputPath makes the next path lookup re-read TIPSY_INPUT_PATH.
// It is a test seam; production never changes paths within a process.
func ResetPointerInputPath() {
	pointerPath.Once = sync.Once{}
	pointerPath.value = PointerPathDirect
	rmbPointerFallback.Store(false)
	resetPointerCaptureState()
	ClearRobloxDirectPointerFallback()
}

// KeyboardDeliveryPath selects the event family for physical keyboard input.
// Its values intentionally mirror PointerDeliveryPath: the official APK's
// final Roblox listener is direct, while GameActivity remains a useful A/B
// control and `both` is diagnostics only.
type KeyboardDeliveryPath uint8

const (
	KeyboardPathGameActivity KeyboardDeliveryPath = iota
	KeyboardPathDirect
	KeyboardPathBoth
)

func (p KeyboardDeliveryPath) String() string {
	switch p {
	case KeyboardPathDirect:
		return "direct"
	case KeyboardPathBoth:
		return "both"
	default:
		return "gameactivity"
	}
}

var keyboardPath struct {
	sync.Once
	value KeyboardDeliveryPath
}

func keyboardDeliveryPath() KeyboardDeliveryPath {
	keyboardPath.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("TIPSY_KEY_INPUT_PATH"))) {
		case "direct":
			keyboardPath.value = KeyboardPathDirect
		case "both":
			keyboardPath.value = KeyboardPathBoth
		case "gameactivity":
			keyboardPath.value = KeyboardPathGameActivity
		default:
			// Keep keyboard and pointer on the same listener family unless a
			// deliberate keyboard-only A/B mode was requested. In particular,
			// TIPSY_INPUT_PATH=direct must not leave keys on the GameActivity
			// listener that the APK's Roblox listener replaces.
			switch pointerDeliveryPath() {
			case PointerPathDirect:
				keyboardPath.value = KeyboardPathDirect
			case PointerPathBoth:
				keyboardPath.value = KeyboardPathBoth
			default:
				keyboardPath.value = KeyboardPathGameActivity
			}
		}
	})
	return keyboardPath.value
}

// KeyboardInputPath reports the selected physical-keyboard A/B arm.
func KeyboardInputPath() KeyboardDeliveryPath { return keyboardDeliveryPath() }

// ResetKeyboardInputPath makes the next lookup re-read TIPSY_KEY_INPUT_PATH.
// It is a test seam; production never changes paths within a process.
func ResetKeyboardInputPath() {
	keyboardPath.Once = sync.Once{}
	keyboardPath.value = KeyboardPathDirect
}

// The direct input target is the exact JNI static-native identity from the APK:
// (JNIEnv*, NativeInputInterface jclass, native function pointers). It remains
// parked until the runtime resolves both matching named exports.
var directInputTarget struct {
	mu          sync.RWMutex
	env         uintptr
	class       uintptr
	buttonFn    uintptr
	moveFn      uintptr
	wheelFn     uintptr
	lockFn      uintptr
	havePointer bool
	lastX       float32
	lastY       float32
	// fallbackCaptured is only the host-held-RMB compatibility path. Getter-true
	// capture uses DispatchRobloxDirectPointerDelta, which accumulates the same
	// APK-style logical pair from lastX/lastY.
	fallbackCaptured bool
	fallbackX        float32
	fallbackY        float32
	// clampW/clampH pin captured points (button edges, wheel detents) to the
	// live surface so a minutes-long desktop grab cannot land clicks or zoom
	// off-view. Motion pairs are never pinned: a pair that stops changing can
	// read as no motion to the engine, so outward travel stays unbounded and
	// only travel back toward the surface re-enters from the edge (no dead
	// zone). Updated from the resize sink; never reset by target re-wires
	// because the viewport outlives them.
	clampW int32
	clampH int32
	// rmbAnchor is the LockCurrentPosition point of a secondary press taken
	// under persistent capture. The old held-RMB fallback grabbed at the click
	// and released the button at that same anchor, so the engine cursor came
	// back to where the operator clicked; the persistent stream reproduces
	// that by re-seeding the integrator to this point on release.
	rmbAnchorSet bool
	rmbAnchorX   float32
	rmbAnchorY   float32
}

func init() {
	// Match the VM's constructor display default and the initial X11 client
	// geometry. The first surface resize publishes the real bounds.
	directInputTarget.clampW = 1280
	directInputTarget.clampH = 720
}

// SetPointerClampViewport publishes the live surface bounds for captured
// points (button edges, wheel detents). The resize sink calls it alongside
// SetDisplaySize so the clamp always agrees with the DisplayMetrics the
// engine sees. Non-positive sizes are ignored defensively; production never
// sends them.
func SetPointerClampViewport(w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	directInputTarget.mu.Lock()
	directInputTarget.clampW = int32(w)
	directInputTarget.clampH = int32(h)
	directInputTarget.mu.Unlock()
}

// ResetPointerClampViewportForTest restores the default clamp bounds. Test
// seam; production bounds only move forward from surface resizes.
func ResetPointerClampViewportForTest() {
	directInputTarget.mu.Lock()
	directInputTarget.clampW = 1280
	directInputTarget.clampH = 720
	directInputTarget.mu.Unlock()
}

// PointerClampViewport reports the live captured-logical bounds. The resize
// sink test pins fan-out through it.
func PointerClampViewport() (w, h int) {
	directInputTarget.mu.RLock()
	defer directInputTarget.mu.RUnlock()
	return int(directInputTarget.clampW), int(directInputTarget.clampH)
}

// clampPointerLogical pins a captured point to the live surface. Caller must
// hold directInputTarget.mu (either mode; it only reads the bounds). Absolute
// X11 coordinates span [0,W-1]x[0,H-1]; the pinned point matches that domain
// so a click or wheel detent always lands where the engine can see it. Never
// call on motion pairs: a pair that stops changing can read as no motion to
// the engine and the camera stops at the edge.
func clampPointerLogical(x, y float32) (float32, float32) {
	maxX := float32(directInputTarget.clampW - 1)
	maxY := float32(directInputTarget.clampH - 1)
	if maxX < 0 || maxY < 0 {
		return x, y
	}
	if x < 0 {
		x = 0
	} else if x > maxX {
		x = maxX
	}
	if y < 0 {
		y = 0
	} else if y > maxY {
		y = maxY
	}
	return x, y
}

// advancePointerLogical moves a captured motion pair by an exact delta.
// Caller must hold directInputTarget.mu. Outward travel past the live surface
// stays unbounded so every sample keeps changing position; travel back toward
// the surface first snaps the pair to the crossed edge so the engine cursor
// reappears the moment the operator reverses instead of after replaying the
// whole overshoot. The delta itself is never altered.
func advancePointerLogical(x, y, dx, dy float32) (float32, float32) {
	maxX := float32(directInputTarget.clampW - 1)
	maxY := float32(directInputTarget.clampH - 1)
	if maxX >= 0 {
		if x > maxX && dx < 0 {
			x = maxX
		} else if x < 0 && dx > 0 {
			x = 0
		}
	}
	if maxY >= 0 {
		if y > maxY && dy < 0 {
			y = maxY
		} else if y < 0 && dy > 0 {
			y = 0
		}
	}
	return x + dx, y + dy
}

// SetRobloxDirectInputTarget wires the direct mouse methods used by the
// official client listener. Exact DEX descriptors are (FFZI)V, (FFFF)V,
// (FFF)V, and ()Z respectively. The last method is the engine-owned lock
// handshake; Tipsy never invents its state.
func SetRobloxDirectInputTarget(env, class, buttonFn, moveFn, wheelFn, lockFn uintptr) bool {
	directInputTarget.mu.Lock()
	directInputTarget.env = env
	directInputTarget.class = class
	directInputTarget.buttonFn = buttonFn
	directInputTarget.moveFn = moveFn
	directInputTarget.wheelFn = wheelFn
	directInputTarget.lockFn = lockFn
	directInputTarget.havePointer = false
	directInputTarget.lastX = 0
	directInputTarget.lastY = 0
	directInputTarget.fallbackCaptured = false
	directInputTarget.fallbackX = 0
	directInputTarget.fallbackY = 0
	directInputTarget.rmbAnchorSet = false
	ready := env != 0 && class != 0 && buttonFn != 0 && moveFn != 0 && wheelFn != 0 && lockFn != 0
	directInputTarget.mu.Unlock()

	logging.Logger(logging.CatJNI).Info("[jni] pointer delivery path",
		"mode", pointerDeliveryPath().String(),
		"directReady", ready,
		"mouseButton", fmt.Sprintf("%#x", buttonFn),
		"mouseMove", fmt.Sprintf("%#x", moveFn),
		"mouseWheel", fmt.Sprintf("%#x", wheelFn))
	logging.Logger(logging.CatJNI).Info("[jni] pointer lock handshake",
		"ready", lockFn != 0, "mouseLockedGetter", fmt.Sprintf("%#x", lockFn))
	return ready
}

// ClearRobloxDirectInputTarget parks direct input before libroblox is unmapped.
func ClearRobloxDirectInputTarget() {
	directInputTarget.mu.Lock()
	directInputTarget.env = 0
	directInputTarget.class = 0
	directInputTarget.buttonFn = 0
	directInputTarget.moveFn = 0
	directInputTarget.wheelFn = 0
	directInputTarget.lockFn = 0
	directInputTarget.havePointer = false
	directInputTarget.lastX = 0
	directInputTarget.lastY = 0
	directInputTarget.fallbackCaptured = false
	directInputTarget.fallbackX = 0
	directInputTarget.fallbackY = 0
	directInputTarget.rmbAnchorSet = false
	directInputTarget.mu.Unlock()
	// Delivery is dead without a target; drop any anchored stream so a
	// re-wired target never inherits a stale grab. The Alt toggle (operator
	// state) and the sticky centered request survive; the next motion after
	// re-wire re-acquires honestly.
	rmbPointerFallback.Store(false)
	persistentPointerCapture.Store(false)
	altToggleConsumed.Store(false)
}

// The direct keyboard target is a separate APK identity from the mouse
// target: classes2.dex declares nativePassKeyEvent(ZIIZ)V on
// NativeGLInterface, not NativeInputInterface.
var directKeyTarget struct {
	mu    sync.RWMutex
	env   uintptr
	class uintptr
	fn    uintptr
}

// SetRobloxDirectKeyTarget wires the exact public static native keyboard
// method in client 2.734.917:
// NativeGLInterface.nativePassKeyEvent(ZIIZ)V. `scanCode` and `keyCode` are
// kept distinct because the APK passes KeyEvent.getScanCode() first and
// KeyEvent.getKeyCode() second.
func SetRobloxDirectKeyTarget(env, class, fn uintptr) bool {
	directKeyTarget.mu.Lock()
	directKeyTarget.env = env
	directKeyTarget.class = class
	directKeyTarget.fn = fn
	ready := env != 0 && class != 0 && fn != 0
	directKeyTarget.mu.Unlock()

	logging.Logger(logging.CatJNI).Info("[jni] keyboard delivery path",
		"mode", keyboardDeliveryPath().String(),
		"directReady", ready,
		"keyEvent", fmt.Sprintf("%#x", fn))
	return ready
}

// ClearRobloxDirectKeyTarget parks direct keyboard delivery before
// libroblox is unmapped.
func ClearRobloxDirectKeyTarget() {
	directKeyTarget.mu.Lock()
	directKeyTarget.env = 0
	directKeyTarget.class = 0
	directKeyTarget.fn = 0
	directKeyTarget.mu.Unlock()
}

// DirectInputStats is separate from GameActivity's consumed/delivered counts:
// the direct methods return void, so only honest deliveries and drops exist.
type DirectInputStats struct {
	ButtonDelivered uint64
	MoveDelivered   uint64
	WheelDelivered  uint64
	KeyDelivered    uint64
	Dropped         uint64
	LockQueries     uint64
	LockTrue        uint64
}

var directInputStats DirectInputStats

// RobloxDirectInputStats returns a snapshot of direct-path counters.
func RobloxDirectInputStats() DirectInputStats {
	return DirectInputStats{
		ButtonDelivered: atomic.LoadUint64(&directInputStats.ButtonDelivered),
		MoveDelivered:   atomic.LoadUint64(&directInputStats.MoveDelivered),
		WheelDelivered:  atomic.LoadUint64(&directInputStats.WheelDelivered),
		KeyDelivered:    atomic.LoadUint64(&directInputStats.KeyDelivered),
		Dropped:         atomic.LoadUint64(&directInputStats.Dropped),
		LockQueries:     atomic.LoadUint64(&directInputStats.LockQueries),
		LockTrue:        atomic.LoadUint64(&directInputStats.LockTrue),
	}
}

// RobloxMainWindowMouseLocked reads the exact APK-declared and exported
// NativeInputInterface.nativeGetMainWindowIsMouseLockedCenter()Z handshake.
// The official generic-motion listener requests View pointer capture only
// after this getter becomes true, and its captured-pointer listener releases
// capture when it becomes false.
func RobloxMainWindowMouseLocked() (locked bool, available bool) {
	// Snapshot-unlock-call: teardown takes the write lock, so the engine
	// getter must run after this lock is dropped (dispatchRobloxDirectKey
	// and the pointer dispatchers already follow the same pattern).
	directInputTarget.mu.RLock()
	env, class, lockFn := directInputTarget.env, directInputTarget.class, directInputTarget.lockFn
	directInputTarget.mu.RUnlock()
	if env == 0 || class == 0 || lockFn == 0 {
		return false, false
	}
	locked = C.tipsy_direct_mouse_locked(unsafe.Pointer(lockFn),
		C.uintptr_t(env), C.uintptr_t(class)) != 0
	atomic.AddUint64(&directInputStats.LockQueries, 1)
	if locked {
		atomic.AddUint64(&directInputStats.LockTrue, 1)
	}
	return locked, true
}

// DispatchRobloxDirectScroll maps a core-X11 vertical wheel detent to the
// supplied APK's exact nativePassMouseWheel(FFF)V method. The APK's generic
// mouse listener passes its cached logical x/y and MotionEvent AXIS_VSCROLL
// (axis 9) as the third float, so Button4/Button5 map to +1/-1 without pixel
// scaling. Its ACTION_SCROLL branch never reads AXIS_HSCROLL; horizontal
// Button6/Button7 events are rejected honestly instead of being reinterpreted
// as an unrelated touch-pan gesture.
func DispatchRobloxDirectScroll(x, y, deltaX, deltaY float32) bool {
	if deltaY == 0 {
		dropDirectEvent("scroll: APK listener has no horizontal wheel route")
		return false
	}
	if deltaX != 0 {
		dropDirectEvent("scroll: simultaneous horizontal wheel component")
		return false
	}
	directInputTarget.mu.RLock()
	env, class, fn := directInputTarget.env, directInputTarget.class, directInputTarget.wheelFn
	// A wheel detent is a point, not motion: pin the captured logical to the
	// live surface so zoom always hit-tests on-view, even after the unbounded
	// motion pair has drifted far off-viewport. Absolute wheel (already
	// on-view) is unaffected.
	x, y = clampPointerLogical(x, y)
	directInputTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		dropDirectEvent("scroll: no direct wheel target wired")
		return false
	}
	C.tipsy_direct_mouse_wheel(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		C.float(x), C.float(y), C.float(deltaY))
	atomic.AddUint64(&directInputStats.WheelDelivered, 1)
	return true
}

func dropDirectEvent(reason string) {
	atomic.AddUint64(&directInputStats.Dropped, 1)
	// High-frequency honest drops must not flood the default Info log; the
	// counter remains the authoritative diagnostic.
	logging.Logger(logging.CatJNI).Debug("[jni] direct input dropped", "reason", reason)
}

func directButtonIndex(x11Button int32) (int32, bool) {
	// APK caller: MotionEvent.getActionButton() - 1. Android's primary and
	// secondary button constants are 1 and 2; X11 names them Button1/Button3.
	switch x11Button {
	case 1:
		return 0, true
	case 3:
		return 1, true
	default:
		return 0, false
	}
}

// DispatchRobloxDirectPointer maps one real X11 pointer event to exactly one
// APK-proven native call. DOWN/UP map to nativePassMouseButton(x,y,pressed,
// buttonIndex); MOVE maps to nativePassMouseMove(x,y,dx,dy). Calls are
// synchronous, preserving the X11 event order and button edges. Descriptors do
// not carry timestamps, so no timestamp is fabricated.
//
// Density: the APK divides MotionEvent pixels by DisplayMetrics.density
// before calling these methods. Tipsy presents Android/X11 coordinates at
// density 1.0 (DisplayMetrics.density=1, PlatformParams.dpiScale=1), so X11
// window pixels pass through unchanged with no per-call scale factor. If the
// presented density ever changes, scale once at the coordinate source — never
// scatter magic divisors through this dispatcher.
func DispatchRobloxDirectPointer(action int32, x, y float32, x11Button int32) bool {
	directInputTarget.mu.Lock()
	env, class := directInputTarget.env, directInputTarget.class
	buttonFn, moveFn := directInputTarget.buttonFn, directInputTarget.moveFn
	if env == 0 || class == 0 || buttonFn == 0 || moveFn == 0 {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: no complete direct target wired")
		return false
	}

	switch action {
	case motionActionDown, motionActionUp:
		button, ok := directButtonIndex(x11Button)
		if !ok {
			directInputTarget.mu.Unlock()
			dropDirectEvent("pointer: unsupported X11 button")
			return false
		}
		// X11 button events carry their real window-relative coordinates.
		// Retain them only as the origin for a subsequent drag delta.
		directInputTarget.havePointer = true
		directInputTarget.lastX = x
		directInputTarget.lastY = y
		directInputTarget.mu.Unlock()
		pressed := C.uchar(0)
		if action == motionActionDown {
			pressed = 1
		}
		C.tipsy_direct_mouse_button(unsafe.Pointer(buttonFn), C.uintptr_t(env), C.uintptr_t(class),
			C.float(x), C.float(y), pressed, C.int(button))
		atomic.AddUint64(&directInputStats.ButtonDelivered, 1)
		return true

	case motionActionMove:
		dx, dy := float32(0), float32(0)
		if directInputTarget.havePointer {
			dx = x - directInputTarget.lastX
			dy = y - directInputTarget.lastY
		}
		directInputTarget.havePointer = true
		directInputTarget.lastX = x
		directInputTarget.lastY = y
		directInputTarget.mu.Unlock()
		C.tipsy_direct_mouse_move(unsafe.Pointer(moveFn), C.uintptr_t(env), C.uintptr_t(class),
			C.float(x), C.float(y), C.float(dx), C.float(dy))
		atomic.AddUint64(&directInputStats.MoveDelivered, 1)
		return true
	default:
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: unsupported action")
		return false
	}
}

// DispatchRobloxDirectPointerDelta delivers one captured-pointer move through
// the APK's axis-27/28 path. The host cursor stays at the X11 grab anchor;
// the engine call uses the official listener's cached logical pair, which
// accumulates density-normalized relative axes before nativePassMouseMove.
// Routing through the absolute-position differencer would report a zero delta
// because recentering intentionally keeps the host x/y unchanged.
func DispatchRobloxDirectPointerDelta(x, y, dx, dy float32) bool {
	directInputTarget.mu.Lock()
	env, class := directInputTarget.env, directInputTarget.class
	moveFn := directInputTarget.moveFn
	if env == 0 || class == 0 || moveFn == 0 {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: no captured-motion target wired")
		return false
	}
	if math.IsNaN(float64(dx)) || math.IsNaN(float64(dy)) ||
		math.IsInf(float64(dx), 0) || math.IsInf(float64(dy), 0) {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: non-finite captured delta")
		return false
	}
	originX, originY := x, y
	if directInputTarget.havePointer {
		originX = directInputTarget.lastX
		originY = directInputTarget.lastY
	}
	x = originX + dx
	y = originY + dy
	if math.IsInf(float64(x), 0) || math.IsInf(float64(y), 0) {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: captured logical coordinate overflow")
		return false
	}
	// Deliberately unbounded, matching the official captured listener's
	// accumulated pair: the cursor is hidden while centered, and a pair that
	// stops changing can read as no motion. Points clamp at their own sites.
	directInputTarget.havePointer = true
	directInputTarget.lastX = x
	directInputTarget.lastY = y
	directInputTarget.mu.Unlock()
	C.tipsy_direct_mouse_move(unsafe.Pointer(moveFn), C.uintptr_t(env), C.uintptr_t(class),
		C.float(x), C.float(y), C.float(dx), C.float(dy))
	atomic.AddUint64(&directInputStats.MoveDelivered, 1)
	return true
}

// BeginRobloxDirectPointerFallback starts the narrow host-captured fallback
// used only when a real held secondary-button gesture has acquired an X11
// grab while the APK's own lock getter is false. The official listener keeps a
// cached logical pair and advances it from AXIS_RELATIVE_X/Y when its lock
// branch permits. The fallback must do the same: a permanently anchored x/y
// pair can make the engine discard otherwise-real deltas as no logical motion.
//
// The APK's cached pair is a logical pointer coordinate, not a View bounds
// check: its captured listener accumulates the density-normalized axes without
// clamping them back to the captured View. Tipsy presents density 1.0, and X11
// supplies finite integer-pixel deltas. Motion is never pinned: a pair that
// stops changing can read as no motion to the engine (the reason this
// integrator exists). Outward travel stays unbounded; travel back re-enters
// from the crossed edge so a visible cursor has no dead zone. Buttons and
// wheel are points, not motion, and clamp to the live surface at their own
// dispatch sites so clicks and zoom always land on-view.
func BeginRobloxDirectPointerFallback(x, y float32) {
	directInputTarget.mu.Lock()
	directInputTarget.fallbackCaptured = true
	directInputTarget.fallbackX = x
	directInputTarget.fallbackY = y
	// Keep the ordinary direct dispatcher coherent if an edge arrives after
	// host capture has already been acquired.
	directInputTarget.havePointer = true
	directInputTarget.lastX = x
	directInputTarget.lastY = y
	directInputTarget.mu.Unlock()
}

// ClearRobloxDirectPointerFallback discards a fallback-era virtual coordinate.
// The caller follows an ordinary RMB release with DispatchRobloxDirectPointer,
// which seeds the real anchored physical coordinate. Focus/teardown may lack
// that final physical edge, so clearing also drops the old virtual origin and
// makes the next normal absolute motion establish a fresh origin.
func ClearRobloxDirectPointerFallback() {
	directInputTarget.mu.Lock()
	directInputTarget.fallbackCaptured = false
	directInputTarget.fallbackX = 0
	directInputTarget.fallbackY = 0
	directInputTarget.rmbAnchorSet = false
	directInputTarget.havePointer = false
	directInputTarget.lastX = 0
	directInputTarget.lastY = 0
	directInputTarget.mu.Unlock()
}

// DispatchRobloxDirectPointerFallbackDelta invokes the same descriptor-exact
// native as captured pointer delivery, but with the evolving logical x/y pair
// required by the measured getter-false RMB compatibility case. It deliberately
// does not query or override the APK's getter; callers select it only after the
// already-delivered RMB edge and a successful host fallback acquisition.
func DispatchRobloxDirectPointerFallbackDelta(dx, dy float32) bool {
	directInputTarget.mu.Lock()
	env, class := directInputTarget.env, directInputTarget.class
	moveFn := directInputTarget.moveFn
	if env == 0 || class == 0 || moveFn == 0 || !directInputTarget.fallbackCaptured {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: no captured fallback target wired")
		return false
	}
	// X11's relative producer uses integer event coordinates, so non-finite
	// values cannot arise in production. Reject a corrupt synthetic/source
	// value rather than poison the cached pair and later normal mouse deltas.
	if math.IsNaN(float64(dx)) || math.IsNaN(float64(dy)) ||
		math.IsInf(float64(dx), 0) || math.IsInf(float64(dy), 0) {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: non-finite captured fallback delta")
		return false
	}
	x := directInputTarget.fallbackX
	y := directInputTarget.fallbackY
	x, y = advancePointerLogical(x, y, dx, dy)
	if math.IsInf(float64(x), 0) || math.IsInf(float64(y), 0) {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: captured fallback logical coordinate overflow")
		return false
	}
	directInputTarget.fallbackX = x
	directInputTarget.fallbackY = y
	directInputTarget.havePointer = true
	directInputTarget.lastX = x
	directInputTarget.lastY = y
	directInputTarget.mu.Unlock()
	C.tipsy_direct_mouse_move(unsafe.Pointer(moveFn), C.uintptr_t(env), C.uintptr_t(class),
		C.float(x), C.float(y), C.float(dx), C.float(dy))
	atomic.AddUint64(&directInputStats.MoveDelivered, 1)
	return true
}

// robloxDirectMoveTargetLive reports whether the direct mouse-move native is
// wired. Persistent desktop capture engages only after this is true, so no
// host grab is acquired before the engine listener exists (startup/Home).
func robloxDirectMoveTargetLive() bool {
	directInputTarget.mu.RLock()
	defer directInputTarget.mu.RUnlock()
	return directInputTarget.env != 0 && directInputTarget.class != 0 && directInputTarget.moveFn != 0
}

// RobloxDirectFallbackPosition returns the evolving captured logical cursor
// position. Captured button and wheel events must use it — not X11's fixed
// grab anchor — so the engine's software cursor and the delivered click or
// scroll agree. ok is false when no fallback/persistent capture holds the
// integrator.
func RobloxDirectFallbackPosition() (x, y float32, ok bool) {
	directInputTarget.mu.RLock()
	defer directInputTarget.mu.RUnlock()
	if !directInputTarget.fallbackCaptured {
		return 0, 0, false
	}
	return directInputTarget.fallbackX, directInputTarget.fallbackY, true
}

// RobloxDirectFallbackOffView reports whether the captured logical pair has
// drifted outside the live viewport, i.e. the engine's software cursor (when
// shown) is currently invisible. ok is false when no fallback/persistent
// capture holds the integrator.
func RobloxDirectFallbackOffView() (off, ok bool) {
	directInputTarget.mu.RLock()
	defer directInputTarget.mu.RUnlock()
	if !directInputTarget.fallbackCaptured {
		return false, false
	}
	cx, cy := clampPointerLogical(directInputTarget.fallbackX, directInputTarget.fallbackY)
	return cx != directInputTarget.fallbackX || cy != directInputTarget.fallbackY, true
}

// SnapRobloxDirectPointerFallbackToCenter re-seeds the captured logical pair
// to the live viewport center and reports that point. The wheel-driven
// dead-center heuristic (zoomLock in gameactivity_input.go) uses it so a
// first-person lock begins and ends with the engine cursor at the center:
// motion between detents still integrates exact dx/dy from there, the pair is
// never pinned. ok is false when no fallback/persistent capture holds the
// integrator.
func SnapRobloxDirectPointerFallbackToCenter() (x, y float32, ok bool) {
	directInputTarget.mu.Lock()
	defer directInputTarget.mu.Unlock()
	if !directInputTarget.fallbackCaptured {
		return 0, 0, false
	}
	x = float32(directInputTarget.clampW / 2)
	y = float32(directInputTarget.clampH / 2)
	directInputTarget.fallbackX, directInputTarget.fallbackY = x, y
	directInputTarget.havePointer = true
	directInputTarget.lastX, directInputTarget.lastY = x, y
	return x, y, true
}

// robloxDirectLastPosition returns the ordinary direct dispatcher's last
// delivered origin. The centered LockCenter path accumulates from it; a
// wheel detent while centered reports it so zoom UI follows the look cursor.
func robloxDirectLastPosition() (x, y float32, ok bool) {
	directInputTarget.mu.RLock()
	defer directInputTarget.mu.RUnlock()
	if !directInputTarget.havePointer {
		return 0, 0, false
	}
	return directInputTarget.lastX, directInputTarget.lastY, true
}

// BeginRobloxDirectCapturedSecondary marks a secondary-button press taken
// under persistent capture. It pins the logical cursor on-view, re-seeds the
// integrator there so the drag starts from the delivered press point, and
// remembers that point as the LockCurrentPosition anchor. ok is false when no
// persistent/fallback stream holds the integrator.
func BeginRobloxDirectCapturedSecondary() (x, y float32, ok bool) {
	directInputTarget.mu.Lock()
	defer directInputTarget.mu.Unlock()
	if !directInputTarget.fallbackCaptured {
		return 0, 0, false
	}
	x, y = clampPointerLogical(directInputTarget.fallbackX, directInputTarget.fallbackY)
	directInputTarget.fallbackX, directInputTarget.fallbackY = x, y
	directInputTarget.havePointer = true
	directInputTarget.lastX, directInputTarget.lastY = x, y
	directInputTarget.rmbAnchorSet = true
	directInputTarget.rmbAnchorX, directInputTarget.rmbAnchorY = x, y
	return x, y, true
}

// EndRobloxDirectCapturedSecondary restores the integrator to the remembered
// secondary-press anchor so the matching release lands there and later motion
// continues from it: the engine cursor comes back to the click point, exactly
// as the held-RMB fallback's release at the grab anchor did. ok is false when
// no press is remembered (the press predates the stream, or a focus/convert
// reset dropped it); the caller then releases at the live logical cursor.
func EndRobloxDirectCapturedSecondary() (x, y float32, ok bool) {
	directInputTarget.mu.Lock()
	defer directInputTarget.mu.Unlock()
	if !directInputTarget.rmbAnchorSet || !directInputTarget.fallbackCaptured {
		directInputTarget.rmbAnchorSet = false
		return 0, 0, false
	}
	x, y = directInputTarget.rmbAnchorX, directInputTarget.rmbAnchorY
	directInputTarget.rmbAnchorSet = false
	directInputTarget.fallbackX, directInputTarget.fallbackY = x, y
	directInputTarget.havePointer = true
	directInputTarget.lastX, directInputTarget.lastY = x, y
	return x, y, true
}

// DispatchRobloxDirectButtonCaptured delivers a button edge at the captured
// logical cursor instead of a physical X11 coordinate. The host pointer is
// confined at the grab anchor while captured, so the physical coordinate is
// the anchor — not where the engine's software cursor is. The integrator is
// otherwise untouched: a click does not move the logical cursor.
func DispatchRobloxDirectButtonCaptured(action int32, x11Button int32) bool {
	directInputTarget.mu.Lock()
	env, class := directInputTarget.env, directInputTarget.class
	buttonFn := directInputTarget.buttonFn
	if env == 0 || class == 0 || buttonFn == 0 || !directInputTarget.fallbackCaptured {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: no captured button target wired")
		return false
	}
	button, ok := directButtonIndex(x11Button)
	if !ok {
		directInputTarget.mu.Unlock()
		dropDirectEvent("pointer: unsupported X11 button")
		return false
	}
	x, y := directInputTarget.fallbackX, directInputTarget.fallbackY
	// A click is a point, not motion: pin to the live surface so it always
	// hit-tests on-view, even after the unbounded motion pair has drifted
	// far off-viewport. The stored integrator is untouched.
	x, y = clampPointerLogical(x, y)
	directInputTarget.mu.Unlock()
	pressed := C.uchar(0)
	if action == motionActionDown {
		pressed = 1
	}
	C.tipsy_direct_mouse_button(unsafe.Pointer(buttonFn), C.uintptr_t(env), C.uintptr_t(class),
		C.float(x), C.float(y), pressed, C.int(button))
	atomic.AddUint64(&directInputStats.ButtonDelivered, 1)
	return true
}

// ClearRobloxDirectPointerFallbackKeepLast drops the fallback-captured flag
// while preserving the ordinary dispatcher's last origin. The anchored to
// centered LockCenter conversion uses it so the first centered deltas
// continue from the established logical cursor instead of restarting at the
// window-center anchor.
func ClearRobloxDirectPointerFallbackKeepLast() {
	directInputTarget.mu.Lock()
	directInputTarget.fallbackCaptured = false
	directInputTarget.fallbackX = 0
	directInputTarget.fallbackY = 0
	directInputTarget.rmbAnchorSet = false
	directInputTarget.mu.Unlock()
}

const (
	// Xorg's installed evdev XKB keycodes are the Linux evdev scan codes plus
	// eight: for example, <ESC>=9 while KEY_ESC=1 and <AD01>=24 while
	// KEY_Q=16. The official Android listener passes KeyEvent.getScanCode()
	// to nativePassKeyEvent, and this native method's physical-code parameter
	// therefore receives the original evdev value, not an Android keycode.
	x11EvdevKeycodeOffset int32 = 8
	x11MaximumKeycode     int32 = 255
)

// x11KeycodeToEvdev converts a raw core-X11 keycode to its evdev scan code
// under the system evdev XKB keycode map. Core X11 keycodes are an unsigned
// byte with the configured range [8,255]; 8 maps to evdev's reserved code 0
// and is not a physical keyboard key we can truthfully forward.
func x11KeycodeToEvdev(x11Keycode int32) (int32, bool) {
	if x11Keycode <= x11EvdevKeycodeOffset || x11Keycode > x11MaximumKeycode {
		return 0, false
	}
	return x11Keycode - x11EvdevKeycodeOffset, true
}

// DispatchRobloxDirectKey delivers one physical X11 key edge through the
// exact final Roblox-listener native in the supplied APK:
// NativeGLInterface.nativePassKeyEvent(ZIIZ)V. Its caller passes, in order,
// KeyEvent down/up, getScanCode(), getKeyCode(), and repeatCount > 0.
//
// `x11Keycode` is the real core-X11 keycode, converted once to an evdev scan
// code. `androidKeycode` remains the X11 layer's complete keysym-to-Android
// translation. They must never be substituted for one another: only the
// first uses the evdev vocabulary. This entry point represents a physical
// edge; the X11 event dispatcher also carries the observed repeat count.
func DispatchRobloxDirectKey(x11Keycode, androidKeycode int32, pressed bool) bool {
	return dispatchRobloxDirectKey(x11Keycode, androidKeycode, pressed, 0)
}

func dispatchRobloxDirectKey(x11Keycode, androidKeycode int32, pressed bool, repeatCount int32) bool {
	if androidKeycode <= 0 {
		dropDirectEvent("key: no Android keycode")
		return false
	}
	scanCode, ok := x11KeycodeToEvdev(x11Keycode)
	if !ok {
		dropDirectEvent("key: invalid X11 keycode")
		return false
	}

	directKeyTarget.mu.RLock()
	env, class, fn := directKeyTarget.env, directKeyTarget.class, directKeyTarget.fn
	directKeyTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		dropDirectEvent("key: no direct target wired")
		return false
	}
	down := C.uchar(0)
	if pressed {
		down = 1
	}
	repeat := C.uchar(0)
	if pressed && repeatCount > 0 {
		repeat = 1
	}
	C.tipsy_direct_key_event(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		down, C.int(scanCode), C.int(androidKeycode), repeat)
	atomic.AddUint64(&directInputStats.KeyDelivered, 1)
	return true
}

// Test hooks for the recording natives above. _test.go cannot import C.
func testDirectRecordButtonFn() uintptr { return uintptr(C.tipsy_direct_record_button_fn()) }
func testDirectRecordMoveFn() uintptr   { return uintptr(C.tipsy_direct_record_move_fn()) }
func testDirectRecordWheelFn() uintptr  { return uintptr(C.tipsy_direct_record_wheel_fn()) }
func testDirectRecordKeyFn() uintptr    { return uintptr(C.tipsy_direct_record_key_fn()) }
func testDirectRecordMouseLockedFn() uintptr {
	return uintptr(C.tipsy_direct_record_mouse_locked_fn())
}
func testDirectRecButtonID(i int) uintptr {
	return uintptr(C.tipsy_direct_rec_button_id(C.int(i)))
}
func testDirectRecButtonFloat(i int) float32 {
	return float32(C.tipsy_direct_rec_button_float(C.int(i)))
}
func testDirectRecButtonPressed() bool { return C.tipsy_direct_rec_button_bool() != 0 }
func testDirectRecButtonIndex() int32  { return int32(C.tipsy_direct_rec_button_int()) }
func testDirectRecMoveID(i int) uintptr {
	return uintptr(C.tipsy_direct_rec_move_id(C.int(i)))
}
func testDirectRecMoveFloat(i int) float32 {
	return float32(C.tipsy_direct_rec_move_float(C.int(i)))
}
func testDirectRecWheelID(i int) uintptr {
	return uintptr(C.tipsy_direct_rec_wheel_id(C.int(i)))
}
func testDirectRecWheelFloat(i int) float32 {
	return float32(C.tipsy_direct_rec_wheel_float(C.int(i)))
}
func testDirectRecKeyID(i int) uintptr {
	return uintptr(C.tipsy_direct_rec_key_id(C.int(i)))
}
func testDirectRecKeyInt(i int) int32 { return int32(C.tipsy_direct_rec_key_int(C.int(i))) }
func testDirectRecReset()             { C.tipsy_direct_rec_reset() }
func testDirectRecSetMouseLocked(locked bool) {
	value := C.int(0)
	if locked {
		value = 1
	}
	C.tipsy_direct_rec_set_mouse_locked(value)
}
func testDirectRecButtonSequence() int { return int(C.tipsy_direct_rec_button_sequence_value()) }
func testDirectRecLockSequence() int   { return int(C.tipsy_direct_rec_lock_sequence_value()) }
