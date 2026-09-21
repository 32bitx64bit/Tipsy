// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// nativeHelperClass is the engine's Java callback surface
// (com/roblox/client/startup/NativeHelper); Tipsy provides the Java side of
// that contract.
const nativeHelperClass = "com/roblox/client/startup/NativeHelper"

// appReadyReceived counts the gameActivity_onAppReady announcements this
// process actually received from the engine. Nothing here fabricates readiness
// or login state.
var appReadyReceived uint64

var (
	appReadyLastMu   sync.Mutex
	appReadyLastStep string
)

// Orientation announcements received from the engine
// (gameActivity_onScreenOrientationChanged(IZ)V). X11 has no orientation
// surface — the window manager owns orientation — so the announcement is
// recorded and the request is never faked as applied.
var (
	orientationReceivedMu    sync.Mutex
	orientationReceivedCount uint64
	orientationLastValue     int32
	orientationLastDefault   bool
)

// Game-loaded announcements received from the engine
// (gameActivity_onGameLoaded(J)V), in the same experience-lifecycle batch as
// NativeGLJavaInterface.gameLoadedCallback(J)V. The J argument is the loaded
// place id (0 = Home). Tipsy only records the engine's own statement and
// forwards the id to the presence listener.
var (
	gameLoadedMu          sync.Mutex
	gameLoadedCount       uint64
	gameLoadedLastPlaceID int64
)

// appReadyStepName makes the engine-provided app-step string safe for a log
// line: printable ASCII only, capped at 128 bytes, control bytes replaced with
// '?'. It is never user text, and no other argument data is read or stored.
func appReadyStepName(s string) string {
	const maxStep = 128
	cap := len(s)
	if cap > maxStep {
		cap = maxStep
	}
	b := make([]byte, 0, cap)
	for i := 0; i < len(s) && len(b) < maxStep; i++ {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			b = append(b, c)
		} else {
			b = append(b, '?')
		}
	}
	return string(b)
}

// dispatchNativeHelper serves the NativeHelper engine→Java callback contract.
// Only identities with local evidence are handled; everything else falls
// through to the stub path. Login JSON is forwarded to the NativeUser snapshot;
// lifecycle events are only recorded and fanned out to explicit subscribers,
// never turned into a guessed route.
func (vm *VM) dispatchNativeHelper(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if class != nativeHelperClass {
		return jnull(), false
	}
	switch {
	case name == "gameActivity_onAppReady" && sig == "(Ljava/lang/String;)V":
		step := appReadyStepName(vm.stringFromArg(args, 0))
		atomic.AddUint64(&appReadyReceived, 1)
		appReadyLastMu.Lock()
		appReadyLastStep = step
		appReadyLastMu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] onAppReady", "step", step)
		// The `Home` transition is the only AppReady value established as the
		// Home route. HomeContainer and RootSwitchNavigator also appear after a
		// Games return, so neither can stand in for it. The subscription
		// receives only a fixed enum; this engine string is never forwarded or
		// retained by the startup seam.
		if step == "Home" {
			noteStartupHomeReady()
		}
	case name == "gameActivity_onScreenOrientationChanged" && sig == "(IZ)V":
		// jvalueIAt treats nil args as zero registers (CallVoidMethod always
		// passes the full register array for this 2-arg signature).
		orient := jvalueIAt(args, 0)
		def := jvalueIAt(args, 1) != 0
		orientationReceivedMu.Lock()
		orientationReceivedCount++
		orientationLastValue, orientationLastDefault = orient, def
		orientationReceivedMu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] onScreenOrientationChanged",
			"orientation", orient, "requestDefault", def)
	case name == "gameActivity_onExperienceStart" && sig == "()V":
		// The paired experience-session start callback. The platform owner
		// receives only this exact signal here.
		logging.Logger(logging.CatJNI).Info("[jni] onExperienceStart")
		noteNativeHelperLifecycle(NativeHelperLifecycleEvent{Kind: NativeHelperExperienceStarted})
	case name == "gameActivity_onExperienceStop" && sig == "(D)V":
		// The D argument is the engine-reported duration, carried through
		// unchanged for the platform owner and never logged as session
		// telemetry.
		duration := jvalueD(args)
		logging.Logger(logging.CatJNI).Info("[jni] onExperienceStop")
		noteNativeHelperLifecycle(NativeHelperLifecycleEvent{
			Kind: NativeHelperExperienceStopped, DurationSeconds: duration,
		})
	case name == "gameActivity_onLuaAppDidReturn" && sig == "()V":
		// Only the default orientation is restored. X11 has no equivalent
		// orientation request, so expose the exact event without claiming that
		// an orientation or a route was applied.
		logging.Logger(logging.CatJNI).Info("[jni] onLuaAppDidReturn")
		noteNativeHelperLifecycle(NativeHelperLifecycleEvent{Kind: NativeHelperLuaAppDidReturn})
	case name == "gameActivity_onGameLoaded" && sig == "(J)V":
		// The single J slot carries the loaded place id (0 = Home); a nil slot
		// reads as 0, never a fabricated value.
		var placeID int64
		if args != nil {
			placeID = int64(jvalueJ(args))
		}
		gameLoadedMu.Lock()
		gameLoadedCount++
		gameLoadedLastPlaceID = placeID
		gameLoadedMu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] onGameLoaded", "placeId", placeID)
		noteGameLoadedPlaceID(placeID)
		noteNativeHelperLifecycle(NativeHelperLifecycleEvent{
			Kind: NativeHelperGameLoadedEvent, PlaceID: placeID,
		})
	case name == "gameActivity_onDidLogInReceived" && sig == "(Ljava/lang/String;)V":
		// DID_LOG_IN JSON. The string is read once and parsed into the
		// NativeUser snapshot; the payload is never logged.
		applyNativeUserLoginJSON(vm.stringFromArg(args, 0))
	default:
		return jnull(), false
	}
	if o != nil {
		return idToJobject(o.id), true
	}
	return jnull(), true
}

// jvalueD reads the D slot of the JNI union. A nil argument array is the
// same zero slot behavior used by the existing integer receivers; it never
// manufactures a duration.
func jvalueD(v *C.jvalue) float64 {
	if v == nil {
		return 0
	}
	return float64(*(*C.jdouble)(unsafe.Pointer(v)))
}

// NativeHelperOrientationAnnouncements reports the
// gameActivity_onScreenOrientationChanged announcements received from the
// engine: count, the most recent orientation value, and its requestDefault
// flag. A count of 0 means no orientation has been announced.
func NativeHelperOrientationAnnouncements() (count uint64, orientation int32, requestDefault bool) {
	orientationReceivedMu.Lock()
	defer orientationReceivedMu.Unlock()
	return orientationReceivedCount, orientationLastValue, orientationLastDefault
}

// NativeHelperAppReady reports the gameActivity_onAppReady announcements
// received from the engine: the count and the most recent sanitized step name.
// A count of 0 means readiness has not been announced.
func NativeHelperAppReady() (count uint64, lastStep string) {
	count = atomic.LoadUint64(&appReadyReceived)
	appReadyLastMu.Lock()
	lastStep = appReadyLastStep
	appReadyLastMu.Unlock()
	return count, lastStep
}

// NativeHelperGameLoaded reports the gameActivity_onGameLoaded
// announcements received from the engine: the count and the most recent place
// id (0 = Home). A count of 0 means game-loaded has not been announced.
func NativeHelperGameLoaded() (count uint64, placeID int64) {
	gameLoadedMu.Lock()
	defer gameLoadedMu.Unlock()
	return gameLoadedCount, gameLoadedLastPlaceID
}

// ResetGameLoadedForTest clears the recorded onGameLoaded announcements to the
// pre-launch state (count 0, Home place 0). Test seam.
func ResetGameLoadedForTest() {
	gameLoadedMu.Lock()
	gameLoadedCount = 0
	gameLoadedLastPlaceID = 0
	gameLoadedMu.Unlock()
}

// testPackObjectArg packs one jobject argument slot for tests (test files
// cannot import "C" in this package).
func testPackObjectArg(id int64) *C.jvalue {
	sl := make([]C.jvalue, 1)
	jvalueSetL(&sl[0], idToJobject(id))
	return &sl[0]
}

// testPackTwoInts packs two jint argument slots for tests (e.g. the
// (IZ)V orientation callback: I then Z).
func testPackTwoInts(a, b int32) *C.jvalue {
	sl := make([]C.jvalue, 2)
	jvalueSetI(&sl[0], C.jint(a))
	jvalueSetI(&sl[1], C.jint(b))
	return &sl[0]
}

// packJdouble packs the exact D argument slot for NativeHelper lifecycle
// tests. Test files cannot import C, so the cgo boundary stays here.
func packJdouble(v float64) *C.jvalue {
	sl := make([]C.jvalue, 1)
	*(*C.jdouble)(unsafe.Pointer(&sl[0])) = C.jdouble(v)
	return &sl[0]
}
