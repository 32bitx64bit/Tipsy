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
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// argCaptureLogged dedupes the observation-only argument diagnostics: one
// line per unique capture identity (approved method + value) per process.
var argCaptureLogged sync.Map

// packJlong packs a 64-bit jvalue argument slot (test and capture helper).
func packJlong(v int64) *C.jvalue {
	sl := make([]C.jvalue, 1)
	C.tipsy_jvalue_set_j(&sl[0], C.jlong(v))
	return &sl[0]
}

// logFindClassName records the class-name argument the engine passes to the
// dispatched java/lang/ClassLoader.findClass path. Observation-only: the
// captured string is exactly the binary name findClassByName resolves, so
// the next launch proves which classes the ClassLoader path is asked for.
// No other argument data is captured.
func logFindClassName(name string) {
	if !diagnosticsEnabled() || name == "" {
		return
	}
	key := "findClass|" + name
	if _, dup := argCaptureLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Info("[jni] findClass-name", "class", name)
}

// orientationAnnounces counts the engine's gameActivity_onScreenOrientationChanged
// announcements observed at stub-dispatch time (observation-only).
var orientationAnnounces uint64

// OrientationAnnouncements reports how many gameActivity_onScreenOrientationChanged
// calls the engine actually made (observed on the stub path; once the
// receiver is dispatched this counter stays at its pre-receiver value and
// NativeHelperOrientationAnnouncements takes over).
func OrientationAnnouncements() uint64 {
	return atomic.LoadUint64(&orientationAnnounces)
}

// logLifecycleStubArgs records the raw J-handle argument of the only two
// approved lifecycle identities, at real stub-dispatch time. The identity
// filter is exact (class AND name AND sig), so nothing else is ever
// captured: in particular no NativeUserJavaInterface data, no object
// references, and no strings. Values repeat verbatim what native passed.
func logLifecycleStubArgs(class, name, sig string, args *C.jvalue) {
	if !diagnosticsEnabled() {
		return
	}
	// NativeHelper.gameActivity_onScreenOrientationChanged(IZ)V: the
	// engine pushes its orientation request to Java here. Observation
	// only: the orientation enum int and the boolean flag are safe
	// aggregates (no text, no coordinates); they never alter dispatch.
	if class == nativeHelperClass && name == "gameActivity_onScreenOrientationChanged" && sig == "(IZ)V" {
		if args == nil {
			return
		}
		orient := jvalueIAt(args, 0)
		flag := jvalueIAt(args, 1) != 0
		atomic.AddUint64(&orientationAnnounces, 1)
		key := "orientationAnnounce|" + strconv.Itoa(int(orient)) + "|" + strconv.FormatBool(flag)
		if _, dup := argCaptureLogged.LoadOrStore(key, struct{}{}); dup {
			return
		}
		logging.Logger(logging.CatJNI).Info("[jni] orientation-announce",
			"orientation", orient, "requestDefault", flag)
		return
	}
	var method string
	switch {
	case class == "com/roblox/engine/jni/NativeGLJavaInterface" &&
		name == "gameLoadedCallback" && sig == "(J)V":
		method = "com/roblox/engine/jni/NativeGLJavaInterface.gameLoadedCallback"
	case class == "com/roblox/client/startup/NativeHelper" &&
		name == "gameActivity_onGameLoaded" && sig == "(J)V":
		method = "com/roblox/client/startup/NativeHelper.gameActivity_onGameLoaded"
	default:
		return
	}
	if args == nil {
		return
	}
	v := int64(C.tipsy_jvalue_j(args))
	key := method + "|" + strconv.FormatInt(v, 10)
	if _, dup := argCaptureLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Info("[jni] lifecycle-handle",
		"method", method, "handle", v)
}
