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

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// nativeHelperClass is the engine's Java callback surface
// (com/roblox/client/startup/NativeHelper). The engine resolves these
// methods with GetMethodID and calls them natively; Tipsy must provide
// the Java side of that contract.
const nativeHelperClass = "com/roblox/client/startup/NativeHelper"

// appReadyReceived counts the gameActivity_onAppReady announcements this
// process actually received from the engine. Tipsy never sets readiness
// itself — receiving and recording the engine's own statement is the whole
// implementation; nothing here fabricates readiness or login state.
var appReadyReceived uint64

var (
	appReadyLastMu   sync.Mutex
	appReadyLastStep string
)

// Orientation announcements received from the engine
// (gameActivity_onScreenOrientationChanged(IZ)V). Official Java
// (classes2.dex NativeHelper) logs the request and forwards it to
// Activity.setRequestedOrientation / requestOrientationAsDefault on the UI
// thread. X11 has no orientation surface — the window manager owns
// orientation and no rotation API exists — so receiving and recording the
// engine's announcement is the complete honest Tipsy-side behavior; the
// request itself is never faked as applied.
var (
	orientationReceivedMu    sync.Mutex
	orientationReceivedCount uint64
	orientationLastValue     int32
	orientationLastDefault   bool
)

// Game-loaded announcements received from the engine
// (gameActivity_onGameLoaded(J)V). The engine calls this every time a
// DataModel finishes loading, in the same experience-lifecycle batch as
// NativeGLJavaInterface.gameLoadedCallback(J)V, screenOrientationChanged,
// and onDataModelNotificationCallback. The J argument is the loaded place
// id: 0 for the Home/App DataModel at startup and after leaving an
// experience, and the public place id after an in-client join (live
// 2026-09-06 session: 0, 18667984660, 0, 8735521924 — each matching the
// Player log's `onGameLoaded: placeId:N` line for the same event; official
// Java is NativeHelper.gameActivity_onGameLoaded(long placeId)). Startup-only
// observation of J=0 was a Home DataModel announcement, not an opaque
// handle. Tipsy only records the engine's own statement and forwards the id
// to the presence listener; nothing fabricates loaded state.
var (
	gameLoadedMu          sync.Mutex
	gameLoadedCount       uint64
	gameLoadedLastPlaceID int64
)

// appReadyStepName makes the engine-provided app-step string safe for a
// log line: printable ASCII only, capped at 128 bytes, control bytes
// replaced with '?'. Official traces show the payload is the engine's own
// step identifier (e.g. "PlatformAccountRouter", "Startup", "Landing");
// it is never user text, and no other argument data is read or stored.
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

// dispatchNativeHelper serves the observed NativeHelper engine→Java
// callback contract. Only identities with local evidence are handled;
// everything else falls through to the honest stub path.
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
	case name == "gameActivity_onGameLoaded" && sig == "(J)V":
		// The single J slot carries the loaded place id (0 = Home); a nil
		// slot reads as 0, never a fabricated value.
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
	default:
		return jnull(), false
	}
	if o != nil {
		return idToJobject(o.id), true
	}
	return jnull(), true
}

// NativeHelperOrientationAnnouncements reports the
// gameActivity_onScreenOrientationChanged announcements received from the
// engine: count, the most recent orientation value, and its
// requestDefault flag. A count of 0 means the engine has not announced an
// orientation to the Java side — never a fabricated value.
func NativeHelperOrientationAnnouncements() (count uint64, orientation int32, requestDefault bool) {
	orientationReceivedMu.Lock()
	defer orientationReceivedMu.Unlock()
	return orientationReceivedCount, orientationLastValue, orientationLastDefault
}

// NativeHelperAppReady reports the gameActivity_onAppReady announcements
// received from the engine: the count and the most recent sanitized step
// name. A count of 0 means the engine has not announced readiness to the
// Java side — never a fabricated value.
func NativeHelperAppReady() (count uint64, lastStep string) {
	count = atomic.LoadUint64(&appReadyReceived)
	appReadyLastMu.Lock()
	lastStep = appReadyLastStep
	appReadyLastMu.Unlock()
	return count, lastStep
}

// NativeHelperGameLoaded reports the gameActivity_onGameLoaded
// announcements received from the engine: the count and the most recent
// place id (0 = Home). A count of 0 means the engine has not announced
// game-loaded to the Java side — never a fabricated value.
func NativeHelperGameLoaded() (count uint64, placeID int64) {
	gameLoadedMu.Lock()
	defer gameLoadedMu.Unlock()
	return gameLoadedCount, gameLoadedLastPlaceID
}

// ResetGameLoadedForTest clears the recorded onGameLoaded announcements to
// the pre-launch state (count 0, Home place 0). Test seam: production
// announcements are engine statements and are never cleared. Input tests
// call it so an onGameLoaded dispatched by an earlier test cannot leak an
// in-experience signal into a later Home-state test.
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
