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

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// argCaptureKey identifies one observation-only argument capture without
// formatting a key string: kind distinguishes the capture, name carries the
// method/class identity, and value/flag carry the approved aggregate.
type argCaptureKey struct {
	kind  uint8
	name  string
	value int64
	flag  bool
}

const (
	argCaptureFindClass   uint8 = 1
	argCaptureOrientation uint8 = 2
	argCaptureLifecycle   uint8 = 3
)

// argCaptureLogged dedupes the observation-only argument diagnostics: one
// line per unique capture identity (approved method + value) per process.
var argCaptureLogged sync.Map

// packJlong packs a 64-bit jvalue argument slot (test and capture helper).
func packJlong(v int64) *C.jvalue {
	sl := make([]C.jvalue, 1)
	jvalueSetJ(&sl[0], C.jlong(v))
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
	key := argCaptureKey{kind: argCaptureFindClass, name: name}
	if _, dup := argCaptureLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Info("[jni] findClass-name", "class", name)
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
		key := argCaptureKey{kind: argCaptureOrientation, value: int64(orient), flag: flag}
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
	v := int64(jvalueJ(args))
	key := argCaptureKey{kind: argCaptureLifecycle, name: method, value: v}
	if _, dup := argCaptureLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Info("[jni] lifecycle-handle",
		"method", method, "handle", v)
}
