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
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// nativeUserClass is the engine's Java user-snapshot surface.
// Official Android getPlatformName is ""; Computer-only Playable Devices
// need Enum.Platform.Windows (owner-licensed spoof). Other NativeUser
// getters stay on the generic stub.
const nativeUserClass = "com/roblox/engine/jni/user/NativeUserJavaInterface"

const nativeUserPlatformName = "Windows"

var (
	nativeUserLogged           sync.Map
	immortalNativeUserPlatform *Object
)

func (vm *VM) dispatchNativeUser(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	_ = o
	_ = args
	if class != nativeUserClass || name != "getPlatformName" || sig != "()Ljava/lang/String;" {
		return jnull(), false
	}
	if _, dup := nativeUserLogged.LoadOrStore(name, struct{}{}); !dup {
		logging.Logger(logging.CatJNI).Info("[jni] native-user",
			"method", name, "nonEmpty", true, "spoof", "pc")
	}
	env := unsafe.Pointer(nil)
	if vm != nil {
		env = vm.envRaw
	}
	return vm.internString(env, &immortalNativeUserPlatform, nativeUserPlatformName), true
}
