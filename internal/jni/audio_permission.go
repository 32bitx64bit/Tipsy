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
	"os"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/mic"
)

// Android PackageManager permission results (SDK 26).
const (
	permissionGranted int32 = 0
	permissionDenied  int32 = -1
)

const recordAudioPermissionName = "android.permission.RECORD_AUDIO"

const (
	checkSelfPermissionSig        = "(Ljava/lang/String;)I"
	checkPermissionSig            = "(Ljava/lang/String;Ljava/lang/String;)I"
	requestPermissionsSig         = "([Ljava/lang/String;I)V"
	onRequestPermissionsResultSig = "(I[Ljava/lang/String;[I)V"
)

var (
	missingPermissionLogged sync.Map
	recordAudioLogged       sync.Map
)

// microphoneDoorOpen is the process-wide RECORD_AUDIO / hasSystemFeature
// microphone door. Canonical Go probe is mic.Allowed() after file then env
// (file < env). Unreadable config falls back to defaults, same as CLI.
// Capture still lazy-opens in OpenSL, not here. OpenSL C getenv remains a
// native kill-switch: file-off without env denies JNI even if the recorder
// could still open when the engine ignores permission.
func microphoneDoorOpen() bool {
	cfg, err := mic.LoadMicrophoneConfigFile(config.Paths().ConfigFile)
	if err != nil {
		cfg = mic.DefaultMicrophoneConfig()
	}
	return cfg.WithEnv(os.LookupEnv).Allowed()
}

func evaluatePermission(name string) int32 {
	if name == recordAudioPermissionName {
		if microphoneDoorOpen() {
			logRecordAudioOnce(true)
			return permissionGranted
		}
		logRecordAudioOnce(false)
		return permissionDenied
	}
	if name != "" {
		logMissingPermissionOnce(name)
	}
	return permissionDenied
}

func logRecordAudioOnce(granted bool) {
	key := "denied"
	if granted {
		key = "granted"
	}
	if _, dup := recordAudioLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	if granted {
		logging.Logger(logging.CatJNI).Info("[jni] RECORD_AUDIO granted")
		return
	}
	logging.Logger(logging.CatJNI).Info("[jni] RECORD_AUDIO denied",
		"consent", "Settings microphone toggle is the consent surface",
		"killSwitch", "TIPSY_DISABLE_MICROPHONE or TIPSY_MICROPHONE")
}

func logMissingPermissionOnce(name string) {
	if _, dup := missingPermissionLogged.LoadOrStore(name, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Error("[jni] missing permission: " + name)
}

func audioPermissionContextClass(class string, o *Object) bool {
	switch class {
	case "android/content/Context",
		"android/content/ContextWrapper",
		"android/app/Activity",
		"android/app/Application",
		"com/google/androidgamesdk/GameActivity",
		"com/roblox/client/startup/MainGameActivity":
		return true
	}
	return classIs(o, "android/content/Context")
}

func audioPermissionPackageManagerClass(class string, o *Object) bool {
	if class == "android/content/pm/PackageManager" {
		return true
	}
	return classIs(o, "android/content/pm/PackageManager")
}

func audioPermissionActivityClass(class string, o *Object) bool {
	switch class {
	case "android/app/Activity",
		"com/google/androidgamesdk/GameActivity",
		"com/roblox/client/startup/MainGameActivity":
		return true
	}
	return classIs(o, "android/app/Activity")
}

func (vm *VM) dispatchAudioPermission(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	switch name + sig {
	case "checkSelfPermission" + checkSelfPermissionSig:
		if !audioPermissionContextClass(class, o) {
			return jnull(), false
		}
		return jniInt(evaluatePermission(vm.stringFromArg(args, 0))), true
	case "checkPermission" + checkPermissionSig:
		if !audioPermissionPackageManagerClass(class, o) {
			return jnull(), false
		}
		_ = vm.stringFromArg(args, 1) // package name; never logged
		return jniInt(evaluatePermission(vm.stringFromArg(args, 0))), true
	case "requestPermissions" + requestPermissionsSig:
		if !audioPermissionActivityClass(class, o) {
			return jnull(), false
		}
		vm.handleRequestPermissions(o, class, args)
		return jnull(), true
	case "onRequestPermissionsResult" + onRequestPermissionsResultSig:
		if !audioPermissionActivityClass(class, o) {
			return jnull(), false
		}
		vm.recordPermissionResult(o, args)
		return jnull(), true
	}
	return jnull(), false
}

func (vm *VM) objectArrayStrings(arr *Object) []string {
	if arr == nil {
		return nil
	}
	vm.mu.RLock()
	elems := append([]int64(nil), arr.elems...)
	vm.mu.RUnlock()
	names := make([]string, 0, len(elems))
	for _, id := range elems {
		name := ""
		if s := vm.get(id); s != nil {
			name = s.str
		}
		names = append(names, name)
	}
	return names
}

func (vm *VM) intArrayFromObject(arr *Object) []int32 {
	if arr == nil || arr.arrKind != int('I') {
		return nil
	}
	vm.mu.RLock()
	raw := append([]byte(nil), arr.bytes...)
	vm.mu.RUnlock()
	n := len(raw) / 4
	out := make([]int32, n)
	for i := 0; i < n; i++ {
		out[i] = int32(uint32(raw[i*4]) | uint32(raw[i*4+1])<<8 | uint32(raw[i*4+2])<<16 | uint32(raw[i*4+3])<<24)
	}
	return out
}

func (vm *VM) newIntArrayLocked(vals []int32) *Object {
	cls := vm.ensureClassLocked("[I")
	o := vm.newObjectLocked(cls)
	o.arrKind = int('I')
	buf := make([]byte, 4*len(vals))
	for i, n := range vals {
		u := uint32(n)
		buf[i*4] = byte(u)
		buf[i*4+1] = byte(u >> 8)
		buf[i*4+2] = byte(u >> 16)
		buf[i*4+3] = byte(u >> 24)
	}
	o.bytes = buf
	return o
}

func (vm *VM) handleRequestPermissions(o *Object, class string, args *C.jvalue) {
	var nameArr *Object
	if args != nil {
		nameArr = vm.get(jobjectToID(uintptr(jvalueLAt(args, 0))))
	}
	requestCode := jvalueIAt(args, 1)
	names := vm.objectArrayStrings(nameArr)
	grants := make([]int32, len(names))
	for i, name := range names {
		grants[i] = evaluatePermission(name)
	}

	vm.mu.Lock()
	results := vm.newIntArrayLocked(grants)
	var namesID, resultsID int64
	if nameArr != nil {
		namesID = nameArr.id
	}
	if results != nil {
		resultsID = results.id
	}
	if o != nil {
		o.fields["tipsy.permRequestCode"] = requestCode
		o.fields["tipsy.permNames"] = names
		o.fields["tipsy.permGrantResults"] = grants
		vm.storeFieldObjLocked(o, "tipsy.permNamesArray", namesID)
		vm.storeFieldObjLocked(o, "tipsy.permGrantArray", resultsID)
	}
	vm.mu.Unlock()

	// Invoke the Activity callback synchronously (Tipsy is the platform;
	// there is no UI prompt). Call the family handler directly: going through
	// vm.dispatch would create a package-init cycle with dispatchFamilies.
	cbClass := class
	if o != nil && o.class != nil && o.class.name != "" {
		cbClass = o.class.name
	}
	_, _ = vm.dispatchAudioPermission(o, cbClass, "onRequestPermissionsResult", onRequestPermissionsResultSig,
		testOnRequestPermissionsResultArgs(requestCode, namesID, resultsID))
}

func (vm *VM) recordPermissionResult(o *Object, args *C.jvalue) {
	if o == nil {
		return
	}
	code := jvalueIAt(args, 0)
	var nameArr, grantArr *Object
	if args != nil {
		nameArr = vm.get(jobjectToID(uintptr(jvalueLAt(args, 1))))
		grantArr = vm.get(jobjectToID(uintptr(jvalueLAt(args, 2))))
	}
	names := vm.objectArrayStrings(nameArr)
	grants := vm.intArrayFromObject(grantArr)
	vm.mu.Lock()
	o.fields["tipsy.permRequestCode"] = code
	o.fields["tipsy.permNames"] = names
	o.fields["tipsy.permGrantResults"] = grants
	vm.mu.Unlock()
}

func resetAudioPermissionLogsForTest() {
	missingPermissionLogged.Range(func(k, _ any) bool {
		missingPermissionLogged.Delete(k)
		return true
	})
	recordAudioLogged.Range(func(k, _ any) bool {
		recordAudioLogged.Delete(k)
		return true
	})
}

func testPermissionNameArgs(nameID int64) *C.jvalue {
	a := new([1]C.jvalue)
	jvalueSetL(&a[0], idToJobject(nameID))
	return &a[0]
}

func testCheckPermissionArgs(nameID, pkgID int64) *C.jvalue {
	a := new([2]C.jvalue)
	jvalueSetL(&a[0], idToJobject(nameID))
	jvalueSetL(&a[1], idToJobject(pkgID))
	return &a[0]
}

func testRequestPermissionsArgs(namesID int64, requestCode int32) *C.jvalue {
	a := new([2]C.jvalue)
	jvalueSetL(&a[0], idToJobject(namesID))
	jvalueSetI(&a[1], C.jint(requestCode))
	return &a[0]
}

func testOnRequestPermissionsResultArgs(code int32, namesID, resultsID int64) *C.jvalue {
	a := new([3]C.jvalue)
	jvalueSetI(&a[0], C.jint(code))
	jvalueSetL(&a[1], idToJobject(namesID))
	jvalueSetL(&a[2], idToJobject(resultsID))
	return &a[0]
}
