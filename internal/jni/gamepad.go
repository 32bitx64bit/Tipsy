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
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/gamepad"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Direct-only single-pad feed-in (simplified 2026-09-12): an own evdev pump
// goroutine feeds Android frames from internal/gamepad into
// handleGamepadFrame, bypassing the X11 ring. No libroblox .text patches
// (ADR 0010). Only the six DEX-proven dynsyms are resolved.
//
// Deleted vs controller v1 (honest list, see
// gamepad-simplify-2026-09-12.md): the TIPSY_GAMEPAD_PATH
// gameactivity/both branches (direct-only per Phase 0; the variable is
// parsed-but-ignored with one honest warn), players 2..4, and per-stick
// deadzone/invert calibration (one global floor, Y never inverted).

// Gamepad axis constants (AMOTION_EVENT_AXIS_*) beyond the X/Y pair. The
// engine pump reads exactly X/Y, Z/RZ, HAT_X/Y and L/RTRIGGER; RX/RY (12/13)
// are served as a mirror in the frame but only Z/RZ go on the wire.
const (
	motionAxisZ        = int32(11)
	motionAxisRX       = int32(12)
	motionAxisRY       = int32(13)
	motionAxisRZ       = int32(14)
	motionAxisHatX     = int32(15)
	motionAxisHatY     = int32(16)
	motionAxisLTrigger = int32(17)
	motionAxisRTrigger = int32(18)
)

// Truthful input sources for gamepad events. Keys report SOURCE_GAMEPAD
// (DPAD keys SOURCE_DPAD, never combined) and moves report SOURCE_JOYSTICK,
// exactly as the DEX source gate accepts either bit.
const (
	sourceGamepad  = int32(0x401)
	sourceDpad     = int32(0x201)
	sourceJoystick = int32(0x1000010)
)

var gamepadPathOnce sync.Once

// gamepadPathNote parses TIPSY_GAMEPAD_PATH once and honestly ignores it:
// pads are direct-only, so any non-direct value logs one warn and delivery
// still goes direct. Unset (or direct) is silent.
func gamepadPathNote() {
	gamepadPathOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("TIPSY_GAMEPAD_PATH"))
		if raw == "" || strings.EqualFold(raw, "direct") {
			return
		}
		logging.Logger(logging.CatJNI).Info("[jni] gamepad path ignored (direct-only lean build)",
			"TIPSY_GAMEPAD_PATH", raw)
	})
}

// GamepadInputPath reports the pad feed-in arm. Always direct; the selector
// is parsed-but-ignored (see gamepadPathNote).
func GamepadInputPath() string { gamepadPathNote(); return "direct" }

var gamepadEnabledOnce sync.Once
var gamepadEnabledValue = true

// gamepadEnabled is the TIPSY_GAMEPAD=0|off kill-switch (default on).
func gamepadEnabled() bool {
	gamepadEnabledOnce.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("TIPSY_GAMEPAD"))) {
		case "0", "off", "false", "no":
			gamepadEnabledValue = false
		default:
			gamepadEnabledValue = true
		}
	})
	return gamepadEnabledValue
}

var gamepadDebugOnce sync.Once
var gamepadDebugValue bool

// gamepadDebug gates per-event arg logging (TIPSY_GAMEPAD_DEBUG=1). Off by
// default: button/axis values are input content and never hit the default
// log.
func gamepadDebug() bool {
	gamepadDebugOnce.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("TIPSY_GAMEPAD_DEBUG"))) {
		case "1", "true", "on", "yes":
			gamepadDebugValue = true
		}
	})
	return gamepadDebugValue
}

// ResetGamepadInputPath makes the next path/enabled lookup re-read the
// environment and clears the pad delivery state. Test seam; production
// never changes paths within a process.
func ResetGamepadInputPath() {
	gamepadPathOnce = sync.Once{}
	gamepadEnabledOnce = sync.Once{}
	gamepadEnabledValue = true
	gamepadDebugOnce = sync.Once{}
	gamepadDebugValue = false
	gamepadCalOnce = sync.Once{}
	gamepadCalValue = gamepad.DefaultGamepadConfig()
	resetGamepadStateForTest()
}

var gamepadCalOnce sync.Once
var gamepadCalValue = gamepad.DefaultGamepadConfig()

// gamepadCalibration returns the cached effective calibration: the persisted
// "gamepad" section of the existing settings file (missing file/key =
// defaults) overlaid once with TIPSY_GAMEPAD_DEADZONE (env wins). An
// unreadable section falls back to defaults (logged once, content-free) so a
// bad file can never block launch. ResetGamepadInputPath clears the cache.
func gamepadCalibration() gamepad.GamepadConfig {
	gamepadCalOnce.Do(func() {
		cfg, err := gamepad.LoadGamepadConfigFile(config.Paths().ConfigFile)
		if err != nil {
			logging.Logger(logging.CatJNI).Info("[jni] gamepad config unreadable, using defaults",
				"err", logging.Redact(err.Error()))
			cfg = gamepad.DefaultGamepadConfig()
		}
		gamepadCalValue = cfg.WithEnv(os.LookupEnv)
	})
	return gamepadCalValue
}

// gamepadFileEnabled reports the persisted Enabled switch from the cached
// effective config. False means the engine sees zero pads even when the
// TIPSY_GAMEPAD kill-switch is unset.
func gamepadFileEnabled() bool { return gamepadCalibration().Enabled }

// GamepadTypeForName classifies the engine-consumed gamepadType ordinal
// from the evdev device name (XBOX→3, DUALSENSE|PS5→2,
// DUALSHOCK|PS4|PLAYSTATION→1, else 0; uppercased comparison).
func GamepadTypeForName(name string) int {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "XBOX"):
		return 3
	case strings.Contains(upper, "DUALSENSE") || strings.Contains(upper, "PS5"):
		return 2
	case strings.Contains(upper, "DUALSHOCK") || strings.Contains(upper, "PS4") ||
		strings.Contains(upper, "PLAYSTATION"):
		return 1
	default:
		return 0
	}
}

// GamepadTypeForDevice is the connect-time ordinal: name first (engine
// classifier), then Xbox USB/BT vendor 0x045e → 3 so GuliKit/X-Box nodes
// that hid-microsoft names without the substring "XBOX" still get the
// Xbox scheme the face-button layout uses.
func GamepadTypeForDevice(name string, vendor uint16) int {
	if t := GamepadTypeForName(name); t != 0 {
		return t
	}
	if vendor == 0x045e {
		return 3
	}
	return 0
}

// The direct gamepad target is the exact JNI static-native identity from
// the APK: (JNIEnv*, NativeInputInterface jclass, native function
// pointers). It remains parked until the runtime resolves all six matching
// named exports.
var directGamepadTarget struct {
	mu           sync.RWMutex
	env          uintptr
	class        uintptr
	axisFn       uintptr
	buttonFn     uintptr
	connectFn    uintptr
	disconnectFn uintptr
	setKeyFn     uintptr
	setMotionFn  uintptr
}

// SetRobloxDirectGamepadTarget wires the six DEX-proven direct gamepad
// methods (descriptors (IIFFF)V, (III)V, (II)V, (I)V, (IIZI)V, (IIIZI)V).
func SetRobloxDirectGamepadTarget(env, class, axisFn, buttonFn, connectFn, disconnectFn, setKeyFn, setMotionFn uintptr) bool {
	directGamepadTarget.mu.Lock()
	directGamepadTarget.env = env
	directGamepadTarget.class = class
	directGamepadTarget.axisFn = axisFn
	directGamepadTarget.buttonFn = buttonFn
	directGamepadTarget.connectFn = connectFn
	directGamepadTarget.disconnectFn = disconnectFn
	directGamepadTarget.setKeyFn = setKeyFn
	directGamepadTarget.setMotionFn = setMotionFn
	ready := env != 0 && class != 0 && axisFn != 0 && buttonFn != 0 &&
		connectFn != 0 && disconnectFn != 0 && setKeyFn != 0 && setMotionFn != 0
	directGamepadTarget.mu.Unlock()

	logging.Logger(logging.CatJNI).Info("[jni] gamepad delivery path",
		"mode", GamepadInputPath(),
		"directReady", ready,
		"axisEvent", fmt.Sprintf("%#x", axisFn),
		"buttonEvent", fmt.Sprintf("%#x", buttonFn),
		"connectEvent", fmt.Sprintf("%#x", connectFn),
		"disconnectEvent", fmt.Sprintf("%#x", disconnectFn))
	return ready
}

// ClearRobloxDirectGamepadTarget parks direct pad delivery before libroblox
// is unmapped and stops the evdev pump.
func ClearRobloxDirectGamepadTarget() {
	directGamepadTarget.mu.Lock()
	directGamepadTarget.env = 0
	directGamepadTarget.class = 0
	directGamepadTarget.axisFn = 0
	directGamepadTarget.buttonFn = 0
	directGamepadTarget.connectFn = 0
	directGamepadTarget.disconnectFn = 0
	directGamepadTarget.setKeyFn = 0
	directGamepadTarget.setMotionFn = 0
	directGamepadTarget.mu.Unlock()
	StopRobloxDirectGamepadPump()
}

// directGamepadTargetLive reports whether the six gamepad natives are wired.
func directGamepadTargetLive() bool {
	directGamepadTarget.mu.RLock()
	defer directGamepadTarget.mu.RUnlock()
	return directGamepadTarget.env != 0 && directGamepadTarget.class != 0 &&
		directGamepadTarget.axisFn != 0 && directGamepadTarget.buttonFn != 0 &&
		directGamepadTarget.connectFn != 0 && directGamepadTarget.disconnectFn != 0 &&
		directGamepadTarget.setKeyFn != 0 && directGamepadTarget.setMotionFn != 0
}

// GamepadStats counts direct-pad deliveries and honest drops. The direct
// methods return void, so only deliveries and drops exist.
type GamepadStats struct {
	ButtonDelivered     uint64
	AxisDelivered       uint64
	ConnectDelivered    uint64
	DisconnectDelivered uint64
	CapabilityDelivered uint64
	Dropped             uint64
}

var gamepadStats GamepadStats

// RobloxDirectGamepadStats returns a snapshot of direct-pad counters.
func RobloxDirectGamepadStats() GamepadStats {
	return GamepadStats{
		ButtonDelivered:     atomic.LoadUint64(&gamepadStats.ButtonDelivered),
		AxisDelivered:       atomic.LoadUint64(&gamepadStats.AxisDelivered),
		ConnectDelivered:    atomic.LoadUint64(&gamepadStats.ConnectDelivered),
		DisconnectDelivered: atomic.LoadUint64(&gamepadStats.DisconnectDelivered),
		CapabilityDelivered: atomic.LoadUint64(&gamepadStats.CapabilityDelivered),
		Dropped:             atomic.LoadUint64(&gamepadStats.Dropped),
	}
}

func gpDrop(reason string) {
	atomic.AddUint64(&gamepadStats.Dropped, 1)
	logging.Logger(logging.CatJNI).Debug("[jni] gamepad dropped", "reason", reason)
}

// DispatchRobloxDirectGamepadButton delivers one pad button edge through
// nativeGamepadButtonEvent(III)V. Pad buttons never enter the keyboard
// direct/nativePassKeyEvent route: ButtonEvent-only by construction.
func DispatchRobloxDirectGamepadButton(deviceID, keyCode int32, pressed bool) bool {
	directGamepadTarget.mu.RLock()
	env, class, fn := directGamepadTarget.env, directGamepadTarget.class, directGamepadTarget.buttonFn
	directGamepadTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		gpDrop("button: no direct gamepad target wired")
		return false
	}
	down := C.int(0)
	if pressed {
		down = 1
	}
	C.tipsy_direct_gamepad_button(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		C.int(deviceID), C.int(keyCode), down)
	atomic.AddUint64(&gamepadStats.ButtonDelivered, 1)
	if gamepadDebug() {
		logging.Logger(logging.CatJNI).Debug("[jni] gamepad button",
			"device", deviceID, "key", keyCode, "down", pressed)
	}
	return true
}

// DispatchRobloxDirectGamepadAxis delivers one stick/trigger/hat sample
// through nativeGamepadAxisEvent(IIFFF)V. The three floats are a pass-through:
// handleGamepadFrame packs them (stick pairs vs hat/trigger singles).
func DispatchRobloxDirectGamepadAxis(deviceID, axisID int32, f1, f2, f3 float32) bool {
	directGamepadTarget.mu.RLock()
	env, class, fn := directGamepadTarget.env, directGamepadTarget.class, directGamepadTarget.axisFn
	directGamepadTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		gpDrop("axis: no direct gamepad target wired")
		return false
	}
	C.tipsy_direct_gamepad_axis(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		C.int(deviceID), C.int(axisID), C.float(f1), C.float(f2), C.float(f3))
	atomic.AddUint64(&gamepadStats.AxisDelivered, 1)
	if gamepadDebug() {
		logging.Logger(logging.CatJNI).Debug("[jni] gamepad axis",
			"device", deviceID, "axis", axisID, "f1", f1, "f2", f2, "f3", f3)
	}
	return true
}

// DispatchRobloxDirectGamepadConnect announces one pad through
// nativeGamepadConnectEventWithGamepadType(II)V.
func DispatchRobloxDirectGamepadConnect(deviceID, gamepadType int32) bool {
	directGamepadTarget.mu.RLock()
	env, class, fn := directGamepadTarget.env, directGamepadTarget.class, directGamepadTarget.connectFn
	directGamepadTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		gpDrop("connect: no direct gamepad target wired")
		return false
	}
	C.tipsy_direct_gamepad_connect(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		C.int(deviceID), C.int(gamepadType))
	atomic.AddUint64(&gamepadStats.ConnectDelivered, 1)
	return true
}

// DispatchRobloxDirectGamepadDisconnect withdraws one pad through
// nativeGamepadDisconnectEvent(I)V.
func DispatchRobloxDirectGamepadDisconnect(deviceID int32) bool {
	directGamepadTarget.mu.RLock()
	env, class, fn := directGamepadTarget.env, directGamepadTarget.class, directGamepadTarget.disconnectFn
	directGamepadTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		gpDrop("disconnect: no direct gamepad target wired")
		return false
	}
	C.tipsy_direct_gamepad_disconnect(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		C.int(deviceID))
	atomic.AddUint64(&gamepadStats.DisconnectDelivered, 1)
	return true
}

// DispatchRobloxDirectGamepadSetKey advertises one key capability through
// nativeSetGamepadSupportedKeyWithGamepadType(IIZI)V.
func DispatchRobloxDirectGamepadSetKey(deviceID, keyCode int32, supported bool, gamepadType int32) bool {
	directGamepadTarget.mu.RLock()
	env, class, fn := directGamepadTarget.env, directGamepadTarget.class, directGamepadTarget.setKeyFn
	directGamepadTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		gpDrop("setkey: no direct gamepad target wired")
		return false
	}
	sup := C.uchar(0)
	if supported {
		sup = 1
	}
	C.tipsy_direct_gamepad_set_key(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		C.int(deviceID), C.int(keyCode), sup, C.int(gamepadType))
	atomic.AddUint64(&gamepadStats.CapabilityDelivered, 1)
	return true
}

// DispatchRobloxDirectGamepadSetMotion advertises one motion capability
// through nativeSetGamepadSupportedMotionWithGamepadType(IIIZI)V.
func DispatchRobloxDirectGamepadSetMotion(deviceID, axisID, arg int32, supported bool, gamepadType int32) bool {
	directGamepadTarget.mu.RLock()
	env, class, fn := directGamepadTarget.env, directGamepadTarget.class, directGamepadTarget.setMotionFn
	directGamepadTarget.mu.RUnlock()
	if env == 0 || class == 0 || fn == 0 {
		gpDrop("setmotion: no direct gamepad target wired")
		return false
	}
	sup := C.uchar(0)
	if supported {
		sup = 1
	}
	C.tipsy_direct_gamepad_set_motion(unsafe.Pointer(fn), C.uintptr_t(env), C.uintptr_t(class),
		C.int(deviceID), C.int(axisID), C.int(arg), sup, C.int(gamepadType))
	atomic.AddUint64(&gamepadStats.CapabilityDelivered, 1)
	return true
}

// engineProbeKeys is the exact H[] set the engine's E() probe passes to
// hasKeys: A/B/X/Y, DPAD 19–22, R1/L1, THUMBL/R, SELECT/START. Absent from
// the probe: C/Z/L2/R2/MODE/DPAD_CENTER.
var engineProbeKeys = []int{96, 97, 99, 100, 19, 20, 21, 22, 103, 102, 106, 107, 109, 108}

// gamepadExtraKeys are advertised TRUE only when physically present:
// digital L2/R2 edges alongside the analog trigger axes, plus MODE.
var gamepadExtraKeys = []int{104, 105, 110}

// engineProbeMotions seeds the E() motion map. GAS/BRAKE (22/23) are probed
// as max() fallbacks for the triggers; a Tipsy pad reports them FALSE
// honestly since its triggers ride 17/18.
var engineProbeMotions = []int{0, 1, 11, 14, 15, 16, 17, 18, 22, 23}

// AdvertiseGamepadCapabilities replays the engine's connect-time E()
// sequence through the capability setters: every probed key and motion with
// its honest supported bit, plus the extra (dev, 15|16, 1, sup, type) hat
// calls. RX/RY are never advertised: the engine never probes them.
func AdvertiseGamepadCapabilities(deviceID, gamepadType int32, keys []int, motions []int) bool {
	if !directGamepadTargetLive() {
		gpDrop("capabilities: no direct gamepad target wired")
		return false
	}
	keySet := make(map[int]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}
	motionSet := make(map[int]bool, len(motions))
	for _, a := range motions {
		motionSet[a] = true
	}
	ok := true
	for _, k := range engineProbeKeys {
		ok = DispatchRobloxDirectGamepadSetKey(deviceID, int32(k), keySet[k], gamepadType) && ok
	}
	for _, k := range gamepadExtraKeys {
		if keySet[k] {
			ok = DispatchRobloxDirectGamepadSetKey(deviceID, int32(k), true, gamepadType) && ok
		}
	}
	for _, a := range engineProbeMotions {
		ok = DispatchRobloxDirectGamepadSetMotion(deviceID, int32(a), -1, motionSet[a], gamepadType) && ok
	}
	for _, a := range []int{15, 16} {
		ok = DispatchRobloxDirectGamepadSetMotion(deviceID, int32(a), 1, motionSet[a], gamepadType) && ok
	}
	return ok
}

// gamepadEngineAxes is the DEX onGenericMotion emission order (packed-switch
// i=0..7): HAT_Y, HAT_X, RTRIGGER, LTRIGGER, RZ, Z, Y, X. Stick pairs are
// packed as (x, -y, 0) / (z, -rz, 0) on both axis ids of the pair; hats and
// triggers stay singles (0, 0, v) with HAT_Y negated. See packGamepadAxis.
var gamepadEngineAxes = []int32{16, 15, 18, 17, 14, 11, 1, 0}

// axisSample is one nativeGamepadAxisEvent f-slot triple.
type axisSample struct{ f1, f2, f3 float32 }

func (s axisSample) zero() bool { return s.f1 == 0 && s.f2 == 0 && s.f3 == 0 }

// packGamepadAxis is the 2.738.1397 DEX packing for nativeGamepadAxisEvent
// (tk/e$e.onGenericMotion packed-switch). Hats/triggers: (0, 0, v) with
// HAT_Y negated. Left stick: (X, -Y, 0) on both AXIS_X and AXIS_Y. Right
// stick: (Z, -RZ, 0) on both AXIS_Z and AXIS_RZ. Sending sticks as
// (0, 0, v) is ignored by the engine (website nav + in-game move die).
func packGamepadAxis(axis int32, axes map[int]float32) (axisSample, bool) {
	switch axis {
	case motionAxisX, motionAxisY:
		_, hasX := axes[int(motionAxisX)]
		_, hasY := axes[int(motionAxisY)]
		if !hasX && !hasY {
			return axisSample{}, false
		}
		return axisSample{axes[int(motionAxisX)], -axes[int(motionAxisY)], 0}, true
	case motionAxisZ, motionAxisRZ:
		_, hasZ := axes[int(motionAxisZ)]
		_, hasRZ := axes[int(motionAxisRZ)]
		if !hasZ && !hasRZ {
			return axisSample{}, false
		}
		return axisSample{axes[int(motionAxisZ)], -axes[int(motionAxisRZ)], 0}, true
	case motionAxisHatY:
		v, ok := axes[int(motionAxisHatY)]
		return axisSample{0, 0, -v}, ok
	case motionAxisHatX, motionAxisLTrigger, motionAxisRTrigger:
		v, ok := axes[int(axis)]
		return axisSample{0, 0, v}, ok
	default:
		return axisSample{}, false
	}
}

// Last-sent pad state for the single pad: the diff baseline for
// change-driven AxisEvent emission and the source of UP synthesis + zeroed
// axes on focus loss and unplug, so the engine never keeps a stuck button.
// Lock order is always gamepadState.mu → directGamepadTarget.mu (via the
// dispatchers); the dispatchers alone never take gamepadState.mu.
var gamepadState struct {
	mu        sync.Mutex
	focused   bool
	announced bool
	deviceID  int32
	buttons   map[int]bool
	axes      map[int32]axisSample
}

func init() {
	gamepadState.focused = true
	gamepadState.buttons = make(map[int]bool)
	gamepadState.axes = make(map[int32]axisSample)
}

// resetGamepadStateForTest clears the pad delivery state. Test seam.
func resetGamepadStateForTest() {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	gamepadState.focused = true
	gamepadState.announced = false
	gamepadState.deviceID = 0
	gamepadState.buttons = make(map[int]bool)
	gamepadState.axes = make(map[int32]axisSample)
}

// gamepadNoteFocus records the X11 window focus transition for the pad gate.
// The pad goes quiet while the window is unfocused, and focus loss
// synthesizes UP for every held button plus zeroed axes in the same beat (no
// stuck-button), keeping the pad announced so refocus sends DOWNs only for
// still-held physical state (no ghost presses).
func gamepadNoteFocus(gained bool) {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	if gained {
		gamepadState.focused = true
		return
	}
	gamepadState.focused = false
	if !directGamepadTargetLive() {
		clearGamepadStateLocked()
		return
	}
	synthesizeGamepadReleaseLocked()
}

// clearGamepadStateLocked empties the diff baseline but keeps the announced
// pad: refocus re-announces fresh DOWN edges for still-held physical state.
// Caller holds mu.
func clearGamepadStateLocked() {
	gamepadState.buttons = make(map[int]bool)
	gamepadState.axes = make(map[int32]axisSample)
}

// synthesizeGamepadReleaseLocked emits UP for every held button and zero for
// every nonzero axis in deterministic order. Caller holds mu. The pad stays
// announced: the next frame for still-held physical state re-emits fresh
// DOWN edges (no ghost presses, no stuck buttons).
func synthesizeGamepadReleaseLocked() {
	var keys []int
	for k := range gamepadState.buttons {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		if DispatchRobloxDirectGamepadButton(gamepadState.deviceID, int32(k), false) {
			delete(gamepadState.buttons, k)
		}
	}
	var axes []int32
	for a, v := range gamepadState.axes {
		if !v.zero() {
			axes = append(axes, a)
		}
	}
	sort.Slice(axes, func(i, j int) bool { return axes[i] < axes[j] })
	for _, a := range axes {
		if DispatchRobloxDirectGamepadAxis(gamepadState.deviceID, a, 0, 0, 0) {
			gamepadState.axes[a] = axisSample{}
		}
	}
}

// GamepadConnected runs the connect sequence for the single pad: the E()
// capability replay, then the connect event with the gamepad type. A
// re-announce of the same device re-probes capabilities without a duplicate
// connect. A second simultaneous device id is ignored honestly (the lean
// build serves one pad). Zero host pads means this is never called: no fake
// pad, and the engine keeps its honest empty enumeration.
func GamepadConnected(deviceID, gamepadType int32, keys []int, motions []int) bool {
	gamepadPathNote()
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	if gamepadState.announced {
		if gamepadState.deviceID != deviceID {
			gpDrop("gamepad: second pad ignored (single-pad lean build)")
			return false
		}
		AdvertiseGamepadCapabilities(deviceID, gamepadType, keys, motions)
		return true
	}
	if !AdvertiseGamepadCapabilities(deviceID, gamepadType, keys, motions) {
		return false
	}
	if !DispatchRobloxDirectGamepadConnect(deviceID, gamepadType) {
		return false
	}
	gamepadState.announced = true
	gamepadState.deviceID = deviceID
	logging.Logger(logging.CatJNI).Info("[jni] gamepad connected",
		"device", deviceID, "type", gamepadType,
		"keys", len(keys), "motions", len(motions))
	return true
}

// GamepadDisconnected withdraws the single pad: UP synthesis + zeroed axes,
// then the disconnect event. Unknown or stale ids are ignored honestly.
func GamepadDisconnected(deviceID int32) bool {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	if !gamepadState.announced || gamepadState.deviceID != deviceID {
		return false
	}
	if directGamepadTargetLive() {
		synthesizeGamepadReleaseLocked()
		DispatchRobloxDirectGamepadDisconnect(deviceID)
	}
	gamepadState.announced = false
	gamepadState.deviceID = 0
	logging.Logger(logging.CatJNI).Info("[jni] gamepad disconnected", "device", deviceID)
	return true
}

// handleGamepadFrame is the X11-ring bypass for the pad: one normalized
// Android frame becomes direct ButtonEvents (buttons/DPAD, L2/R2 key duality
// included) and change-driven AxisEvents with the DEX stick-pair packing
// (ACTION_MOVE). Same focus gating as keys: quiet while unfocused, UP
// synthesis + zero axes on focus loss (via gamepadNoteFocus) and on unplug.
func handleGamepadFrame(af gamepad.AndroidFrame) {
	gamepadPathNote()
	if !gamepadEnabled() {
		gpDrop("gamepad: disabled by TIPSY_GAMEPAD")
		return
	}
	if !gamepadFileEnabled() {
		gpDrop("gamepad: disabled by config file (gamepad.enabled=false)")
		return
	}
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	if !gamepadState.focused {
		gpDrop("gamepad: unfocused, frame quiet")
		return
	}
	if af.Disconnect {
		if gamepadState.announced && (af.DeviceID == 0 || int32(af.DeviceID) == gamepadState.deviceID) {
			synthesizeGamepadReleaseLocked()
			DispatchRobloxDirectGamepadDisconnect(gamepadState.deviceID)
			gamepadState.announced = false
			gamepadState.deviceID = 0
		}
		return
	}
	dev := int32(af.DeviceID)
	if !gamepadState.announced || dev != gamepadState.deviceID {
		gpDrop("gamepad: frame for unannounced pad")
		return
	}
	// Buttons first (edges), deterministic order over the union of held
	// and pressed so releases are never missed.
	keySet := make(map[int]bool, len(af.Buttons)+len(gamepadState.buttons))
	for k := range gamepadState.buttons {
		keySet[k] = true
	}
	for k, pressed := range af.Buttons {
		if pressed {
			keySet[k] = true
		}
	}
	var allKeys []int
	for k := range keySet {
		allKeys = append(allKeys, k)
	}
	sort.Ints(allKeys)
	for _, k := range allKeys {
		was := gamepadState.buttons[k]
		now := af.Buttons[k]
		if now == was {
			continue
		}
		if DispatchRobloxDirectGamepadButton(dev, int32(k), now) {
			if now {
				gamepadState.buttons[k] = true
			} else {
				delete(gamepadState.buttons, k)
			}
		}
	}
	// Axes on change only (ACTION_MOVE batching): rest frames repeat
	// bit-identical packed triples and refire nothing.
	for _, a := range gamepadEngineAxes {
		sample, present := packGamepadAxis(a, af.Axes)
		if !present {
			continue
		}
		if old, ok := gamepadState.axes[a]; ok && old == sample {
			continue
		}
		if DispatchRobloxDirectGamepadAxis(dev, a, sample.f1, sample.f2, sample.f3) {
			gamepadState.axes[a] = sample
		}
	}
}

// resetGamepadKeyEventLocked fills a gamepad KeyEvent object: truthful
// per-pad device id and source (SOURCE_GAMEPAD, DPAD keys SOURCE_DPAD).
// scanCode carries the evdev code's honest hardware value where known.
func (vm *VM) resetGamepadKeyEventLocked(o *Object, keyCode, deviceID, source int32, pressed bool, downTime, eventTime int64, scanCode int32) {
	if pressed {
		o.fields["action"] = keyEventActionDown
	} else {
		o.fields["action"] = keyEventActionUp
	}
	o.fields["keyCode"] = keyCode
	o.fields["deviceId"] = deviceID
	o.fields["source"] = source
	o.fields["repeatCount"] = int32(0)
	o.fields["metaState"] = int32(0)
	o.fields["flags"] = int32(0)
	o.fields["scanCode"] = scanCode
	o.fields["downTime"] = downTime
	o.fields["eventTime"] = eventTime
	o.fields["unicodeChar"] = int32(0)
}

// resetGamepadMotionEventLocked fills a gamepad MotionEvent object for one
// ACTION_MOVE sample: stable device id, SOURCE_JOYSTICK, and the full axis
// set (X/Y mirrored into the x/y fields; the rest ride the gamepadAxes map
// served by the extended getAxisValue cases).
func (vm *VM) resetGamepadMotionEventLocked(o *Object, deviceID, source int32, axes map[int32]float32, downTime, eventTime int64) {
	o.fields["action"] = motionActionMove
	o.fields["deviceId"] = deviceID
	o.fields["source"] = source
	o.fields["edgeFlags"] = int32(0)
	o.fields["flags"] = int32(0)
	o.fields["metaState"] = int32(0)
	o.fields["downTime"] = downTime
	o.fields["eventTime"] = eventTime
	o.fields["pointerCount"] = int32(1)
	o.fields["pointerId0"] = int32(0)
	o.fields["toolType0"] = int32(0)
	o.fields["x"] = axes[motionAxisX]
	o.fields["y"] = axes[motionAxisY]
	o.fields["xPrecision"] = float32(1)
	o.fields["yPrecision"] = float32(1)
	cp := make(map[int32]float32, len(axes))
	for a, v := range axes {
		cp[a] = v
	}
	o.fields["gamepadAxes"] = cp
}

// gamepadAxisValue answers the extended getAxisValue cases (Z/RZ, RX/RY
// mirror, hats, triggers) from the per-object gamepadAxes map. Objects
// without the map — every pointer/key event — answer 0, exactly as before.
func (vm *VM) gamepadAxisValue(o *Object, axis int32) float32 {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	m, ok := o.fields["gamepadAxes"].(map[int32]float32)
	if !ok {
		return 0
	}
	return m[axis]
}

// Evdev pump: single-pad owner. It bypasses the X11 ring: inotify + rescan
// over /dev/input/event* plus nonblocking reads feed normalized frames into
// handleGamepadFrame. Deadzone comes from the evdev flat via the gamepad
// package (flat==0 fallback logged once per device); the single global
// TIPSY_GAMEPAD_DEADZONE floor is applied per Frame just before MapFrame.
var gamepadPump struct {
	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
}

const (
	gamepadPollInterval = 5 * time.Millisecond
	// gamepadRescanEvery polls per rescan (~1 s at the poll interval).
	gamepadRescanEvery = 200
)

// StartRobloxDirectGamepadPump starts the evdev pump over the default
// /dev/input directory. It refuses to start under the TIPSY_GAMEPAD
// kill-switch or when the persisted gamepad.enabled switch is off.
func StartRobloxDirectGamepadPump() bool {
	return StartRobloxDirectGamepadPumpDir(gamepad.InputNodeDir)
}

// StartRobloxDirectGamepadPumpDir starts the evdev pump over dir. The dir
// seam lets the no-pad test prove zero devices stay silent.
func StartRobloxDirectGamepadPumpDir(dir string) bool {
	gamepadPump.mu.Lock()
	defer gamepadPump.mu.Unlock()
	if gamepadPump.running {
		return true
	}
	gamepadPathNote()
	if !gamepadEnabled() {
		logging.Logger(logging.CatJNI).Info("[jni] gamepad disabled", "TIPSY_GAMEPAD", "0|off")
		return false
	}
	if !gamepadFileEnabled() {
		logging.Logger(logging.CatJNI).Info("[jni] gamepad disabled", "gamepad.enabled", false)
		return false
	}
	if dir == "" {
		dir = gamepad.InputNodeDir
	}
	mgr := gamepad.NewManager(dir, func(msg string) {
		logging.Logger(logging.CatJNI).Info(msg)
	})
	stop := make(chan struct{})
	done := make(chan struct{})
	gamepadPump.running = true
	gamepadPump.stopCh = stop
	gamepadPump.doneCh = done
	go gamepadPumpLoop(mgr, stop, done)
	return true
}

// StopRobloxDirectGamepadPump stops the evdev pump. Idempotent; safe to
// call with no pump running (teardown, tests).
func StopRobloxDirectGamepadPump() {
	gamepadPump.mu.Lock()
	if !gamepadPump.running {
		gamepadPump.mu.Unlock()
		return
	}
	stop, done := gamepadPump.stopCh, gamepadPump.doneCh
	gamepadPump.mu.Unlock()
	close(stop)
	<-done
	gamepadPump.mu.Lock()
	gamepadPump.running = false
	gamepadPump.mu.Unlock()
}

func gamepadPumpLoop(mgr *gamepad.Manager, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer mgr.Close()
	// InputDeviceListener-equivalent wiring: hotplug connect runs the E()
	// capability replay + typed connect; disconnect synthesizes UPs +
	// zeroed axes in the same beat, then the disconnect event.
	mgr.OnConnect = func(info gamepad.DeviceInfo, devID int) {
		pad, _, r, ok := mgr.Slot(devID)
		if !ok {
			return
		}
		if r != nil && r.Warn == nil {
			r.Warn = func(msg string) {
				logging.Logger(logging.CatJNI).Info(msg)
			}
		}
		GamepadConnected(int32(devID), int32(GamepadTypeForDevice(info.Name, info.ID.Vendor)),
			gamepad.SupportedKeysForDevice(info),
			gamepad.SupportedMotions(pad.Mapping, info.HasAbs))
	}
	mgr.OnDisconnect = func(devID int) {
		GamepadDisconnected(int32(devID))
	}
	loggedDeny := false
	rescan := func() {
		changed, err := mgr.Rescan()
		if err != nil {
			// EACCES carries the actionable input-group/Flatpak hint.
			// Log it once per denial streak at Info, then Debug.
			if !loggedDeny {
				logging.Logger(logging.CatJNI).Info("gamepad rescan", "err", err)
				loggedDeny = true
			} else {
				logging.Logger(logging.CatJNI).Debug("gamepad rescan", "err", err)
			}
			return
		}
		loggedDeny = false
		_ = changed
	}
	rescan()
	// Inotify hotplug: directory creates/deletes/moves trigger an immediate
	// rescan through the listener wiring above. When the watch cannot be
	// installed the 1 s periodic rescan below remains the fallback.
	if err := mgr.StartWatch(stop); err != nil {
		logging.Logger(logging.CatJNI).Debug("gamepad watch unavailable; periodic rescan only", "err", err)
	}
	ticker := time.NewTicker(gamepadPollInterval)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		ticks++
		if ticks%gamepadRescanEvery == 0 {
			rescan()
		}
		for _, devID := range mgr.DeviceIDs() {
			pad, cur, r, ok := mgr.Slot(devID)
			if !ok || cur == nil || r == nil {
				// A vanished node surfaces on the next rescan; never a fake.
				continue
			}
			evs, err := cur.ReadAvailable()
			if err != nil || len(evs) == 0 {
				continue
			}
			info := pad.Info
			mapping := pad.Mapping
			for _, ev := range evs {
				if f := r.Feed(ev); f != nil {
					gamepad.ApplyCalibration(f, mapping, gamepadCalibration())
					handleGamepadFrame(gamepad.MapFrame(f, devID, mapping, info.Abs))
				}
			}
		}
	}
}

// Test hooks for the gamepad recording natives (see direct_input.c). _test
// files cannot import C, so the C-typed calls happen here. Kinds mirror
// the TIPSY_GP_* enum.
const (
	gamepadRecAxis       = 1
	gamepadRecButton     = 2
	gamepadRecConnect    = 3
	gamepadRecDisconnect = 4
	gamepadRecSetKey     = 5
	gamepadRecSetMotion  = 6
)

func testDirectGamepadRecFn(kind int) uintptr {
	return uintptr(C.tipsy_direct_gamepad_rec_fn_ptr(C.int(kind)))
}
func testDirectGamepadRecCount() int { return int(C.tipsy_direct_gamepad_rec_count()) }
func testDirectGamepadRecKind(i int) int {
	return int(C.tipsy_direct_gamepad_rec_kind(C.int(i)))
}
func testDirectGamepadRecInt(i, j int) int {
	return int(C.tipsy_direct_gamepad_rec_int(C.int(i), C.int(j)))
}
func testDirectGamepadRecFloat(i, j int) float32 {
	return float32(C.tipsy_direct_gamepad_rec_float(C.int(i), C.int(j)))
}
func testDirectGamepadRecReset() { C.tipsy_direct_gamepad_rec_reset() }
