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
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/gamepad"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Gamepad axis constants (AMOTION_EVENT_AXIS_*) beyond the X/Y pair. Only
// Z/RZ, HAT_X/Y and L/RTRIGGER reach the wire; RX/RY stay a frame mirror.
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

// Input sources for gamepad events: keys report SOURCE_GAMEPAD (DPAD keys
// SOURCE_DPAD, never combined), moves report SOURCE_JOYSTICK.
const (
	sourceGamepad  = int32(0x401)
	sourceDpad     = int32(0x201)
	sourceJoystick = int32(0x1000010)
)

var gamepadPathOnce sync.Once

// gamepadPathNote parses TIPSY_GAMEPAD_PATH once and ignores it: pads are
// direct-only, so any non-direct value logs one warn and delivery still goes
// direct. Unset (or direct) is silent.
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

// gamepadLive serves the effective gamepad settings for the process: the
// persisted "gamepad" section of the settings file overlaid with the
// TIPSY_GAMEPAD / TIPSY_GAMEPAD_DEADZONE environment (env wins), re-checked on
// the frame path at most every gamepad.DefaultLiveConfigInterval and forced
// fresh on every launch and window-focus gain. It is deliberately NOT a
// process-lifetime cache: the GUI launches Roblox in the same process that owns
// the Settings page, so a once-only read never sees a later toggle.
var gamepadLive = newGamepadLive()

func newGamepadLive() *gamepad.LiveConfig {
	c := gamepad.NewLiveConfig(func() string { return config.Paths().ConfigFile }, os.LookupEnv)
	c.OnError = func(err error) {
		logging.Logger(logging.CatJNI).Info("[jni] gamepad config unreadable, keeping last good or defaults",
			"err", logging.Redact(err.Error()))
	}
	return c
}

// gamepadEnabled is the TIPSY_GAMEPAD=0|off kill-switch alone (default on).
// It does not include the persisted switch; see gamepadEffectiveEnabled.
func gamepadEnabled() bool { return !gamepadLive.Get().KillSwitch }

// gamepadFileEnabled reports the persisted gamepad.enabled switch alone. False
// means the engine sees zero pads even when TIPSY_GAMEPAD is unset.
func gamepadFileEnabled() bool { return gamepadLive.Get().Config.Enabled }

// gamepadCalibration returns the effective calibration for this instant: the
// persisted section (missing file/key = defaults) overlaid with
// TIPSY_GAMEPAD_DEADZONE (env wins). An unreadable section keeps the last good
// one (defaults on the first load) so a bad file can never block launch.
func gamepadCalibration() gamepad.GamepadConfig { return gamepadLive.Get().Config }

var gamepadDebugOnce sync.Once
var gamepadDebugValue bool

// gamepadDebug gates per-event arg logging (TIPSY_GAMEPAD_DEBUG=1). Off by
// default: button/axis values are input content and never hit the default log.
func gamepadDebug() bool {
	gamepadDebugOnce.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("TIPSY_GAMEPAD_DEBUG"))) {
		case "1", "true", "on", "yes":
			gamepadDebugValue = true
		}
	})
	return gamepadDebugValue
}

// ResetGamepadInputPath re-reads the environment and settings file on the next
// lookup and clears the pad delivery state. Test seam.
func ResetGamepadInputPath() {
	gamepadPathOnce = sync.Once{}
	gamepadDebugOnce = sync.Once{}
	gamepadDebugValue = false
	gamepadLive.Reset()
	gamepadLive.SetClock(nil)
	gamepadLive.SetInterval(gamepad.DefaultLiveConfigInterval)
	gamepadPump.mu.Lock()
	gamepadPump.armed = false
	gamepadPump.dir = ""
	gamepadPump.mu.Unlock()
	resetGamepadStateForTest()
}

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

// GamepadTypeForDevice is the connect-time ordinal: name first, then Xbox
// USB/BT vendor 0x045e → 3 so GuliKit/X-Box nodes without the substring
// "XBOX" still get the Xbox scheme the face-button layout uses.
func GamepadTypeForDevice(name string, vendor uint16) int {
	if t := GamepadTypeForName(name); t != 0 {
		return t
	}
	if vendor == 0x045e {
		return 3
	}
	return 0
}

// The direct gamepad target is the JNI static-native identity for the pad
// feed: (JNIEnv*, NativeInputInterface jclass, six native function pointers).
// It stays parked until all six are resolved.
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

// SetRobloxDirectGamepadTarget wires the six direct gamepad methods
// (descriptors (IIFFF)V, (III)V, (II)V, (I)V, (IIZI)V, (IIIZI)V).
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

	// A new target is a new engine instance: nothing it has not been told about
	// may carry over from a previous launch in this process (a stale announced
	// pad would swallow the new instance's connect event).
	resetGamepadDeliveryState()

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

func directGamepadTargetLive() bool {
	directGamepadTarget.mu.RLock()
	defer directGamepadTarget.mu.RUnlock()
	return directGamepadTarget.env != 0 && directGamepadTarget.class != 0 &&
		directGamepadTarget.axisFn != 0 && directGamepadTarget.buttonFn != 0 &&
		directGamepadTarget.connectFn != 0 && directGamepadTarget.disconnectFn != 0 &&
		directGamepadTarget.setKeyFn != 0 && directGamepadTarget.setMotionFn != 0
}

// GamepadStats counts direct-pad deliveries and drops; the direct methods
// return void, so nothing else exists.
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
// through nativeGamepadAxisEvent(IIFFF)V. The three floats are a pass-through
// packed by handleGamepadFrame.
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

// engineProbeKeys is the connect-time probed key set: A/B/X/Y, DPAD 19–22,
// R1/L1, THUMBL/R, SELECT/START. C/Z/L2/R2/MODE/DPAD_CENTER are never probed.
var engineProbeKeys = []int{96, 97, 99, 100, 19, 20, 21, 22, 103, 102, 106, 107, 109, 108}

// gamepadExtraKeys are advertised TRUE only when physically present:
// digital L2/R2 edges alongside the analog trigger axes, plus MODE.
var gamepadExtraKeys = []int{104, 105, 110}

// engineProbeMotions seeds the motion map. GAS/BRAKE (22/23) are probed as
// trigger fallbacks and reported FALSE since triggers ride 17/18.
var engineProbeMotions = []int{0, 1, 11, 14, 15, 16, 17, 18, 22, 23}

// AdvertiseGamepadCapabilities replays the connect-time capability sequence:
// every probed key and motion with its supported bit, plus the extra
// (dev, 15|16, 1, sup, type) hat calls. RX/RY are never advertised.
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

// gamepadEngineAxes is the emission order: HAT_Y, HAT_X, RTRIGGER, LTRIGGER,
// RZ, Z, Y, X. Stick pairs pack as (x, -y, 0) / (z, -rz, 0) on both axis ids
// of the pair; hats and triggers stay singles (0, 0, v) with HAT_Y negated.
// See packGamepadAxis.
var gamepadEngineAxes = []int32{16, 15, 18, 17, 14, 11, 1, 0}

type axisSample struct{ f1, f2, f3 float32 }

func (s axisSample) zero() bool { return s.f1 == 0 && s.f2 == 0 && s.f3 == 0 }

// packGamepadAxis is the engine packing for nativeGamepadAxisEvent.
// Hats/triggers: (0, 0, v) with HAT_Y negated. Left stick: (X, -Y, 0) on both
// AXIS_X and AXIS_Y. Right stick: (Z, -RZ, 0) on both AXIS_Z and AXIS_RZ.
// Sticks sent as (0, 0, v) are ignored by the engine.
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

// gamepadAnnounce is the connect payload for one pad: everything needed to
// replay the capability sequence and the typed connect event to the engine.
type gamepadAnnounce struct {
	deviceID    int32
	gamepadType int32
	keys        []int
	motions     []int
}

func newGamepadAnnounce(deviceID, gamepadType int32, keys, motions []int) gamepadAnnounce {
	return gamepadAnnounce{
		deviceID:    deviceID,
		gamepadType: gamepadType,
		keys:        append([]int(nil), keys...),
		motions:     append([]int(nil), motions...),
	}
}

// Last-sent pad state for the single pad: the diff baseline for
// change-driven AxisEvent emission and the source of UP synthesis + zeroed
// axes on focus loss and unplug. Lock order is always gamepadState.mu →
// directGamepadTarget.mu (via the dispatchers); the dispatchers alone never
// take gamepadState.mu.
//
// withheld is a pad that is physically present but hidden from the engine
// because the controller switch is off (it was withdrawn from a running
// session, or hot-plugged while off). Re-enabling re-announces it; it is
// dropped when the pad unplugs or the pump stops, never replayed as a ghost.
var gamepadState struct {
	mu         sync.Mutex
	focused    bool
	announced  bool
	deviceID   int32
	padInfo    gamepadAnnounce  // payload of the announced pad (re-announce replay)
	withheld   *gamepadAnnounce // pad present on the host but hidden by the switch
	buttons    map[int]bool
	axes       map[int32]axisSample
	keyScratch []int // reusable per-frame union, protected by mu
}

func init() {
	gamepadState.focused = true
	gamepadState.buttons = make(map[int]bool)
	gamepadState.axes = make(map[int32]axisSample)
}

// resetGamepadDeliveryState forgets everything the engine was told: a new
// engine instance (SetRobloxDirectGamepadTarget) starts with no pad and no held
// state. Focus is host state and survives.
func resetGamepadDeliveryState() {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	gamepadState.announced = false
	gamepadState.deviceID = 0
	gamepadState.padInfo = gamepadAnnounce{}
	gamepadState.withheld = nil
	gamepadState.buttons = make(map[int]bool)
	gamepadState.axes = make(map[int32]axisSample)
}

// resetGamepadStateForTest clears the pad delivery state. Test seam.
func resetGamepadStateForTest() {
	resetGamepadDeliveryState()
	gamepadState.mu.Lock()
	gamepadState.focused = true
	gamepadState.mu.Unlock()
}

// gamepadNoteFocus records the X11 window focus transition for the pad gate.
// The pad goes quiet while unfocused; focus loss synthesizes UP for every held
// button plus zeroed axes, keeping the pad announced so refocus sends DOWNs
// only for still-held physical state.
//
// Focus gain is also the live-switch reconcile point: it force-reads the
// persisted controller switch (no stat interval), withdraws the pad from the
// engine at once when it is off even if the pad is idle, re-announces a
// withheld pad when it is back on, and parks or (re)starts the evdev pump to
// match. Window focus is a rare human-paced event, so this adds no polling.
func gamepadNoteFocus(gained bool) {
	if gained {
		snap := gamepadLive.Reload()
		gamepadState.mu.Lock()
		gamepadState.focused = true
		reconcileGamepadSwitchLocked(snap)
		gamepadState.mu.Unlock()
		gamepadApplySwitchToPump(snap)
		return
	}
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
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
// announced: the next frame for still-held physical state re-emits fresh DOWN
// edges.
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

// announceGamepadLocked runs the connect sequence for a: the capability
// replay, then the typed connect event. Caller holds mu and has checked the
// switch. On success the pad is announced and no longer withheld.
func announceGamepadLocked(a gamepadAnnounce) bool {
	if !AdvertiseGamepadCapabilities(a.deviceID, a.gamepadType, a.keys, a.motions) {
		return false
	}
	if !DispatchRobloxDirectGamepadConnect(a.deviceID, a.gamepadType) {
		return false
	}
	gamepadState.announced = true
	gamepadState.deviceID = a.deviceID
	gamepadState.padInfo = a
	gamepadState.withheld = nil
	return true
}

// withdrawGamepadForSwitchLocked takes the announced pad away from the engine
// because the controller switch went off: UP for every held button, zeroed
// axes, then the disconnect event, exactly the unplug sequence. The pad is
// remembered as withheld so turning the switch back on re-announces it
// without a replug. Idempotent. Caller holds mu.
func withdrawGamepadForSwitchLocked(snap gamepad.LiveSnapshot) {
	if !gamepadState.announced {
		return
	}
	dev := gamepadState.deviceID
	if directGamepadTargetLive() {
		synthesizeGamepadReleaseLocked()
		DispatchRobloxDirectGamepadDisconnect(dev)
	}
	clearGamepadStateLocked()
	held := gamepadState.padInfo
	gamepadState.withheld = &held
	gamepadState.announced = false
	gamepadState.deviceID = 0
	reason := "gamepad.enabled=false"
	if snap.KillSwitch {
		reason = "TIPSY_GAMEPAD"
	}
	logging.Logger(logging.CatJNI).Info("[jni] gamepad withdrawn (controller input off)",
		"device", dev, "reason", reason)
}

// reconcileGamepadSwitchLocked brings the engine-visible pad in line with the
// controller switch and reports whether frames may flow. Off: withdraw the
// announced pad (idempotent). On: re-announce a withheld pad once the window
// is focused and the target is wired. Caller holds mu.
func reconcileGamepadSwitchLocked(snap gamepad.LiveSnapshot) bool {
	if !snap.Enabled() {
		withdrawGamepadForSwitchLocked(snap)
		return false
	}
	if w := gamepadState.withheld; w != nil && !gamepadState.announced &&
		gamepadState.focused && directGamepadTargetLive() {
		if announceGamepadLocked(*w) {
			logging.Logger(logging.CatJNI).Info("[jni] gamepad re-announced (controller input on)",
				"device", gamepadState.deviceID)
		}
	}
	return true
}

// GamepadConnected runs the connect sequence for the single pad: the
// capability replay, then the connect event with the gamepad type. A
// re-announce of the same device re-probes capabilities without a duplicate
// connect. A second simultaneous device id is ignored (one pad is served).
// While the controller switch is off the pad is remembered but never shown to
// the engine; it is announced when the switch comes back on.
func GamepadConnected(deviceID, gamepadType int32, keys []int, motions []int) bool {
	gamepadPathNote()
	snap := gamepadLive.Get()
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	if gamepadState.announced {
		if gamepadState.deviceID != deviceID {
			gpDrop("gamepad: second pad ignored (single-pad lean build)")
			return false
		}
		if !snap.Enabled() {
			withdrawGamepadForSwitchLocked(snap)
			return false
		}
		a := newGamepadAnnounce(deviceID, gamepadType, keys, motions)
		AdvertiseGamepadCapabilities(deviceID, gamepadType, keys, motions)
		gamepadState.padInfo = a
		return true
	}
	a := newGamepadAnnounce(deviceID, gamepadType, keys, motions)
	if !snap.Enabled() {
		gamepadState.withheld = &a
		gpDrop("gamepad: connect withheld (controller input off)")
		return false
	}
	if !announceGamepadLocked(a) {
		return false
	}
	logging.Logger(logging.CatJNI).Info("[jni] gamepad connected",
		"device", deviceID, "type", gamepadType,
		"keys", len(keys), "motions", len(motions))
	return true
}

// GamepadDisconnected withdraws the single pad: UP synthesis + zeroed axes,
// then the disconnect event. Unknown or stale ids are ignored. A pad withheld
// by the controller switch is simply forgotten.
func GamepadDisconnected(deviceID int32) bool {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	if w := gamepadState.withheld; w != nil && w.deviceID == deviceID {
		gamepadState.withheld = nil
	}
	if !gamepadState.announced || gamepadState.deviceID != deviceID {
		return false
	}
	if directGamepadTargetLive() {
		synthesizeGamepadReleaseLocked()
		DispatchRobloxDirectGamepadDisconnect(deviceID)
	}
	gamepadState.announced = false
	gamepadState.deviceID = 0
	gamepadState.padInfo = gamepadAnnounce{}
	logging.Logger(logging.CatJNI).Info("[jni] gamepad disconnected", "device", deviceID)
	return true
}

// handleGamepadFrame turns one normalized Android frame into direct
// ButtonEvents (buttons/DPAD, L2/R2 key duality included) and change-driven
// AxisEvents with the stick-pair packing (ACTION_MOVE). Same focus gating as
// keys: quiet while unfocused, UP synthesis + zero axes on focus loss (via
// gamepadNoteFocus) and on unplug. The controller switch is re-evaluated here
// (rate-limited stat, see gamepadLive): turning it off withdraws the pad
// mid-session and drops frames; turning it back on re-announces it.
func handleGamepadFrame(af gamepad.AndroidFrame) {
	gamepadPathNote()
	handleGamepadFrameSnap(gamepadLive.Get(), af)
}

func handleGamepadFrameSnap(snap gamepad.LiveSnapshot, af gamepad.AndroidFrame) {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	if !reconcileGamepadSwitchLocked(snap) {
		if w := gamepadState.withheld; w != nil && af.Disconnect &&
			(af.DeviceID == 0 || int32(af.DeviceID) == w.deviceID) {
			gamepadState.withheld = nil
		}
		// Counter only: a pad that keeps moving while the switch is off must
		// not spam the log.
		atomic.AddUint64(&gamepadStats.Dropped, 1)
		return
	}
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
	// Buttons first (edges), deterministic order over the union of held and
	// pressed so releases are never missed.
	allKeys := collectGamepadKeys(gamepadState.keyScratch, gamepadState.buttons, af.Buttons)
	gamepadState.keyScratch = allKeys
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

// resetGamepadKeyEventLocked fills a gamepad KeyEvent object: per-pad device
// id and source (SOURCE_GAMEPAD, DPAD keys SOURCE_DPAD). scanCode carries the
// evdev code's hardware value where known.
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
// ACTION_MOVE sample: stable device id, SOURCE_JOYSTICK, and the full axis set
// (X/Y mirrored into the x/y fields; the rest ride the gamepadAxes map served
// by the extended getAxisValue cases).
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
// mirror, hats, triggers) from the per-object gamepadAxes map. Objects without
// the map — every pointer/key event — answer 0.
func (vm *VM) gamepadAxisValue(o *Object, axis int32) float32 {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	m, ok := o.fields["gamepadAxes"].(map[int32]float32)
	if !ok {
		return 0
	}
	return m[axis]
}

// Evdev pump: single-pad owner. It waits on evdev, inotify, and shutdown then
// feeds normalized frames into handleGamepadFrame. Deadzone comes from the
// evdev flat via the gamepad package; the single global TIPSY_GAMEPAD_DEADZONE
// floor is applied per Frame just before MapFrame.
//
// armed marks a launch session that asked for the pump (Start was called and
// no explicit Stop has torn it down). Only an armed session may have its pump
// (re)started when the controller switch comes back on mid-game; a stopped or
// never-started session never restarts one. dir is remembered for that
// restart so it opens the same node directory the launch did.
var gamepadPump struct {
	mu      sync.Mutex
	running bool
	armed   bool
	dir     string
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// StartRobloxDirectGamepadPump starts the evdev pump over the default
// /dev/input directory. It refuses to start under the TIPSY_GAMEPAD
// kill-switch or when the persisted gamepad.enabled switch is off; the switch
// is read fresh from disk on every call, never from an earlier launch's cache.
// A refused start still arms the session: if the switch is turned on while the
// game runs, regaining window focus starts the pump then (and only then does
// anything open /dev/input).
func StartRobloxDirectGamepadPump() bool {
	return StartRobloxDirectGamepadPumpDir(gamepad.InputNodeDir)
}

// StartRobloxDirectGamepadPumpDir starts the evdev pump over dir.
func StartRobloxDirectGamepadPumpDir(dir string) bool {
	if dir == "" {
		dir = gamepad.InputNodeDir
	}
	gamepadPump.mu.Lock()
	defer gamepadPump.mu.Unlock()
	gamepadPump.armed = true
	gamepadPump.dir = dir
	if gamepadPump.running {
		return true
	}
	gamepadPathNote()
	return startGamepadPumpLocked(dir, gamepadLive.Reload())
}

// startGamepadPumpLocked opens the manager and spawns the pump when snap
// allows pads. Caller holds gamepadPump.mu and has verified !running.
func startGamepadPumpLocked(dir string, snap gamepad.LiveSnapshot) bool {
	if snap.KillSwitch {
		logging.Logger(logging.CatJNI).Info("[jni] gamepad disabled", "TIPSY_GAMEPAD", "0|off")
		return false
	}
	if !snap.Config.Enabled {
		logging.Logger(logging.CatJNI).Info("[jni] gamepad disabled", "gamepad.enabled", false)
		return false
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

// StopRobloxDirectGamepadPump stops the evdev pump, disarms the session and
// forgets what the engine was told: the pad feed ends with the session it fed,
// so nothing (a focus event, a stale withheld pad) can later dispatch into an
// engine that is being torn down. Idempotent; safe to call with no pump running
// (teardown, tests).
func StopRobloxDirectGamepadPump() {
	stopGamepadPump(true)
	resetGamepadDeliveryState()
}

// stopGamepadPump stops the pump and reports whether one was running;
// disarm=false keeps the session armed so a later switch-on can start it
// again. Concurrent callers all wait for the loop to exit; only the first
// closes the stop channel.
func stopGamepadPump(disarm bool) (wasRunning bool) {
	gamepadPump.mu.Lock()
	if disarm {
		gamepadPump.armed = false
	}
	if !gamepadPump.running {
		gamepadPump.mu.Unlock()
		return false
	}
	stop, done := gamepadPump.stopCh, gamepadPump.doneCh
	if stop != nil {
		close(stop)
		gamepadPump.stopCh = nil
	}
	gamepadPump.mu.Unlock()
	<-done
	gamepadPump.mu.Lock()
	if gamepadPump.doneCh == done {
		gamepadPump.running = false
		gamepadPump.doneCh = nil
	}
	gamepadPump.mu.Unlock()
	return true
}

// gamepadApplySwitchToPump matches the evdev pump to the controller switch at
// a window-focus gain. Off: close the pad node (nothing reads /dev/input
// while the switch is off) and, if a pump was actually parked, forget the
// withheld pad, which the next start rediscovers. On: start the pump if this
// launch armed one that is not running (switch turned on mid-game) and its
// engine target is still wired.
// Never called with gamepadState.mu held: Stop waits on the pump goroutine,
// which takes that lock.
func gamepadApplySwitchToPump(snap gamepad.LiveSnapshot) {
	if !snap.Enabled() {
		if stopGamepadPump(false) {
			gamepadState.mu.Lock()
			gamepadState.withheld = nil
			gamepadState.mu.Unlock()
		}
		return
	}
	gamepadPump.mu.Lock()
	defer gamepadPump.mu.Unlock()
	if !gamepadPump.armed || gamepadPump.running || !directGamepadTargetLive() {
		return
	}
	if startGamepadPumpLocked(gamepadPump.dir, snap) {
		logging.Logger(logging.CatJNI).Info("[jni] gamepad pump started (controller input on)")
	}
}

func gamepadPumpLoop(mgr *gamepad.Manager, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	// Hotplug connect runs the capability replay plus the typed connect;
	// disconnect synthesizes releases and zeroed axes, then the disconnect
	// event.
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
	pump := gamepad.NewReadyPump(mgr)
	// One scratch AndroidFrame is reused across SYN_REPORT; handleGamepadFrame
	// copies edges into gamepadState and does not retain Buttons/Axes after it
	// returns.
	var mapped gamepad.AndroidFrame
	pump.OnFrame = func(pad gamepad.Pad, frame *gamepad.Frame) {
		// Invoked only at a real SYN_REPORT boundary, in source order. The
		// frame is owned by this callback until it returns.
		// One snapshot per frame: calibration and the controller switch come
		// from the same rate-limited, file-aware read, so a toggle or a
		// deadzone/layout change in Settings reaches a running game.
		snap := gamepadLive.Get()
		gamepad.ApplyCalibration(frame, pad.Mapping, snap.Config)
		handleGamepadFrameSnap(snap, gamepad.MapFrameInto(&mapped, frame, pad.DevID, pad.Mapping, pad.Info.Abs))
	}
	pump.OnRescanError = func(err error) {
		// EACCES carries the actionable input-group/Flatpak hint: log once per
		// denial streak at Info, then Debug.
		if !loggedDeny {
			logging.Logger(logging.CatJNI).Info("gamepad rescan", "err", err)
			loggedDeny = true
			return
		}
		logging.Logger(logging.CatJNI).Debug("gamepad rescan", "err", err)
	}
	if err := pump.Run(stop); err != nil {
		logging.Logger(logging.CatJNI).Info("gamepad readiness pump stopped", "err", err)
	}
}

// Test hooks for the gamepad recording natives. Test files cannot import C,
// so the C-typed calls happen here.
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
