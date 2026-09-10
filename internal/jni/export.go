// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#include "jni_bridge.h"
#include <stdlib.h>
#include <string.h>
*/
import "C"

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

func logf(msg string) {
	logging.Logger(logging.CatJNI).Info(msg)
}

func logMissingMethod(class, name, sig string) {
	logging.Logger(logging.CatJNI).Error("[jni] missing method: " + methodLogName(class, name, sig))
}

// stubDispatchKey identifies one dispatch-time stub fallback without
// formatting a key string on every fallback call.
type stubDispatchKey struct {
	class, name, sig string
	retKind          rune
}

// stubDispatchLogged dedupes the dispatch-time stub-fallback diagnostic:
// one log per unique class.name+sig|retKind for the life of the process.
var stubDispatchLogged sync.Map

// logStubDispatch records that the JNIEnv dispatch path is about to answer
// an unresolved method through vm.stubCall. Observation-only: it fires after
// dispatch returned handled=false and before stubCall runs, so every fallback
// return and side effect is preserved exactly. It never fires for methods
// vm.dispatch handles (implemented paths, <init>, field getters).
func logStubDispatch(class, name, sig string, retKind rune) {
	key := stubDispatchKey{class: class, name: name, sig: sig, retKind: retKind}
	if _, dup := stubDispatchLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Error("[jni] stub-dispatch",
		"method", methodLogName(class, name, sig),
		"retKind", string(retKind))
}

// callDispatchOrStub runs vm.dispatch and, when it does not handle the
// method, applies the vm.stubCall fallback with the deduped diagnostic.
// Returns (value, handled); behavior is identical to the inline fallback it
// replaced. logLifecycleStubArgs adds an observation-only diagnostic for the
// two approved lifecycle identities after logStubDispatch and before
// stubCall: no dispatch decision, return value, or side effect changes.
func callDispatchOrStub(vm *VM, obj C.jobject, class, name, sig string, args *C.jvalue, retKind rune) (C.jobject, bool) {
	env := unsafe.Pointer(nil)
	if vm != nil {
		env = vm.envRaw
	}
	return callDispatchOrStubEnv(vm, env, obj, class, name, sig, args, retKind)
}

func callDispatchOrStubEnv(vm *VM, env unsafe.Pointer, obj C.jobject, class, name, sig string, args *C.jvalue, retKind rune) (C.jobject, bool) {
	v, handled, bound := vm.resolveDispatch(env, obj, class, name, sig, args)
	if handled {
		return v, true
	}
	return bound(vm, env, obj, args, retKind)
}

// findClassByName is java/lang/ClassLoader.findClass, the engine's
// ClassLoader-based class resolution (GetMethodID'd during init, called from
// nativePostClientSettingsLoadedInitialization3 right after
// FindClass(ApplicationExitInfoCpp)). It resolves from the same class map
// GoJNI_FindClass serves and returns the canonical Class object
// (fields["name"] carries the binary name, so classNameOf attributes later
// GetMethodID/RegisterNatives to the real class instead of the stub's
// nameless java/lang/Class object). Unknown names mirror FindClass's
// auto-create with the same diagnostic: one class-resolution policy for
// both JNIEnv paths. An empty or absent name resolves nothing.
func (vm *VM) findClassByName(env unsafe.Pointer, name string) C.jobject {
	if name == "" {
		return jnull()
	}
	vm.mu.Lock()
	cls := vm.classes[name]
	if cls == nil || cls.obj == nil {
		logging.Logger(logging.CatJNI).Error("[jni] auto-class: " + name)
		cls = vm.ensureClassLocked(name)
	}
	if cls != nil && cls.obj != nil {
		vm.addLocalOnLocked(env, cls.obj.id)
	}
	vm.mu.Unlock()
	if cls == nil || cls.obj == nil {
		return jnull()
	}
	return idToJobject(cls.obj.id)
}

func classNameOf(vm *VM, cls C.jclass) string {
	id := jobjectToID(uintptr(asJobjectFromClass(cls)))
	if id == 0 {
		return ""
	}
	o := vm.get(id)
	if o == nil {
		return ""
	}
	if o.class != nil && o.class.name == "java/lang/Class" {
		vm.mu.RLock()
		n, ok := o.fields["name"].(string)
		vm.mu.RUnlock()
		if ok {
			return n
		}
	}
	if o.class != nil {
		return o.class.name
	}
	return ""
}

//export GoJNI_GetVersion
func GoJNI_GetVersion(env *C.JNIEnv) C.jint {
	_ = env
	return C.jint(JNIVersion16)
}

//export GoJNI_FindClass
func GoJNI_FindClass(env *C.JNIEnv, name *C.char) C.jclass {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || name == nil {
		return jclassNull()
	}
	n := C.GoString(name)
	vm.mu.Lock()
	cls, ok := vm.classes[n]
	if !ok || cls == nil || cls.obj == nil {
		logf("[jni] FindClass: " + n)
		logging.Logger(logging.CatJNI).Error("[jni] auto-class: " + n)
		cls = vm.ensureClassLocked(n)
	}
	if cls != nil && cls.obj != nil {
		vm.addLocalOnLocked(unsafe.Pointer(env), cls.obj.id)
	}
	vm.mu.Unlock()
	if cls == nil || cls.obj == nil {
		return jclassNull()
	}
	return jclassOf(idToJobject(cls.obj.id))
}

//export GoJNI_DefineClass
func GoJNI_DefineClass(env *C.JNIEnv, name *C.char, loader C.jobject, buf *C.jbyte, len C.jsize) C.jclass {
	_ = loader
	_ = buf
	_ = len
	return GoJNI_FindClass(env, name)
}

//export GoJNI_GetSuperclass
func GoJNI_GetSuperclass(env *C.JNIEnv, sub C.jclass) C.jclass {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jclassNull()
	}
	n := classNameOf(vm, sub)
	vm.mu.Lock()
	cls := vm.classes[n]
	if cls != nil && cls.super != nil && cls.super.obj != nil {
		vm.addLocalOnLocked(unsafe.Pointer(env), cls.super.obj.id)
	}
	vm.mu.Unlock()
	if cls == nil || cls.super == nil || cls.super.obj == nil {
		return jclassNull()
	}
	return jclassOf(idToJobject(cls.super.obj.id))
}

//export GoJNI_IsAssignableFrom
func GoJNI_IsAssignableFrom(env *C.JNIEnv, sub, sup C.jclass) C.jboolean {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_FALSE
	}
	subN := classNameOf(vm, sub)
	supN := classNameOf(vm, sup)
	vm.mu.RLock()
	subC, supC := vm.classes[subN], vm.classes[supN]
	vm.mu.RUnlock()
	if supC != nil && supC.isAssignable(subC) {
		return C.JNI_TRUE
	}
	return C.JNI_FALSE
}

//export GoJNI_Throw
func GoJNI_Throw(env *C.JNIEnv, obj C.jthrowable) C.jint {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_ERR
	}
	vm.setPending(unsafe.Pointer(env), jobjectToID(uintptr(asJobjectFromThrow(obj))))
	return C.JNI_OK
}

//export GoJNI_ThrowNew
func GoJNI_ThrowNew(env *C.JNIEnv, clazz C.jclass, msg *C.char) C.jint {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_ERR
	}
	n := classNameOf(vm, clazz)
	vm.mu.Lock()
	cls := vm.classes[n]
	if cls == nil {
		cls = vm.classes["java/lang/Throwable"]
	}
	o := vm.newObjectOn(unsafe.Pointer(env), cls)
	if msg != nil {
		o.str = C.GoString(msg)
	}
	vm.mu.Unlock()
	vm.setPending(unsafe.Pointer(env), o.id)
	return C.JNI_OK
}

//export GoJNI_ExceptionOccurred
func GoJNI_ExceptionOccurred(env *C.JNIEnv) C.jthrowable {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jthrowableOf(jnull())
	}
	id := vm.pending(unsafe.Pointer(env))
	if id != 0 {
		vm.addLocal(unsafe.Pointer(env), id)
	}
	return jthrowableOf(idToJobject(id))
}

//export GoJNI_ExceptionDescribe
func GoJNI_ExceptionDescribe(env *C.JNIEnv) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	id := vm.pending(unsafe.Pointer(env))
	if id != 0 {
		logf("[jni] pending exception")
	}
}

//export GoJNI_ExceptionClear
func GoJNI_ExceptionClear(env *C.JNIEnv) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	vm.setPending(unsafe.Pointer(env), 0)
}

//export GoJNI_FatalError
func GoJNI_FatalError(env *C.JNIEnv, msg *C.char) {
	_ = env
	logf("[jni] FatalError: " + C.GoString(msg))
}

//export GoJNI_IsSameObject
func GoJNI_IsSameObject(env *C.JNIEnv, obj1, obj2 C.jobject) C.jboolean {
	_ = env
	if jobjectToID(uintptr(obj1)) == jobjectToID(uintptr(obj2)) {
		return C.JNI_TRUE
	}
	return C.JNI_FALSE
}

//export GoJNI_EnsureLocalCapacity
func GoJNI_EnsureLocalCapacity(env *C.JNIEnv, capacity C.jint) C.jint {
	_ = env
	_ = capacity
	return C.JNI_OK
}

//export GoJNI_AllocObject
func GoJNI_AllocObject(env *C.JNIEnv, clazz C.jclass) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jnull()
	}
	n := classNameOf(vm, clazz)
	vm.mu.Lock()
	cls := vm.classes[n]
	if cls == nil {
		vm.mu.Unlock()
		return jnull()
	}
	o := vm.newObjectOn(unsafe.Pointer(env), cls)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

//export GoJNI_NewObjectA
func GoJNI_NewObjectA(env *C.JNIEnv, clazz C.jclass, methodID C.jmethodID, args *C.jvalue) C.jobject {
	obj := GoJNI_AllocObject(env, clazz)
	if vm := vmFromEnv(unsafe.Pointer(env)); vm != nil {
		if info, ok := lookupMethod(methodID); ok && info.class == nativeTextBoxInfoClass && info.name == "<init>" {
			seedNativeTextBoxInfoConstructor(vm, obj, info.sig, args)
		}
	}
	var out C.jvalue
	GoJNI_CallA(env, obj, clazz, methodID, args, 0, 'V', &out)
	return obj
}

//export GoJNI_GetObjectClass
func GoJNI_GetObjectClass(env *C.JNIEnv, obj C.jobject) C.jclass {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jclassNull()
	}
	o := vm.get(jobjectToID(uintptr(obj)))
	if o == nil || o.class == nil || o.class.obj == nil {
		return jclassNull()
	}
	id := o.class.obj.id
	vm.addLocal(unsafe.Pointer(env), id)
	return jclassOf(idToJobject(id))
}

//export GoJNI_IsInstanceOf
func GoJNI_IsInstanceOf(env *C.JNIEnv, obj C.jobject, clazz C.jclass) C.jboolean {
	if jobjectToID(uintptr(obj)) == 0 {
		return C.JNI_FALSE
	}
	return GoJNI_IsAssignableFrom(env, GoJNI_GetObjectClass(env, obj), clazz)
}

//export GoJNI_GetMethodID
func GoJNI_GetMethodID(env *C.JNIEnv, clazz C.jclass, name, sig *C.char, isStatic C.jint) C.jmethodID {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || name == nil || sig == nil {
		return jmethodNull()
	}
	cn := classNameOf(vm, clazz)
	n := C.GoString(name)
	s := C.GoString(sig)
	id, first := internMethod(cn, n, s, isStatic != 0)
	if first && n != "<init>" && !isImplementedMethod(n, s) {
		logMissingMethodOnce(cn, n, s)
	}
	return id
}

//export GoJNI_CallA
func GoJNI_CallA(env *C.JNIEnv, obj C.jobject, clazz C.jclass, methodID C.jmethodID, args *C.jvalue, isStatic C.jint, retKind C.jint, out *C.jvalue) {
	jvalueZero(out)
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	raw := unsafe.Pointer(env)
	info, ok := lookupMethod(methodID)
	if !ok {
		class, name, sig, _, parsed := parseMethod(methodID)
		if !parsed {
			logf("[jni] missing method: <invalid>")
			return
		}
		if class == "" {
			if isStatic != 0 {
				class = classNameOf(vm, clazz)
			} else {
				o := vm.get(jobjectToID(uintptr(obj)))
				if o != nil && o.class != nil {
					class = o.class.name
				}
			}
		}
		v, _ := callDispatchOrStubEnv(vm, raw, obj, class, name, sig, args, rune(retKind))
		packCallResult(out, retKind, v)
		return
	}
	if h := info.loadHandler(); h != nil {
		v, _ := h(vm, raw, obj, args, rune(retKind))
		packCallResult(out, retKind, v)
		return
	}
	class := resolveCallClass(vm, info, obj, clazz, isStatic)
	name, sig := info.name, info.sig
	v, handled, bound := vm.resolveDispatch(raw, obj, class, name, sig, args)
	if !handled {
		v, _ = bound(vm, raw, obj, args, rune(retKind))
	}
	info.storeHandler(bound)
	packCallResult(out, retKind, v)
}

// newDeviceStaticParams is NativeGLJavaInterface.getDeviceStaticParams.
// DEX instance fields: testDeviceName, appBuildVariant, appVersion,
// cpu64Bit, deviceName, deviceSku, manufacturer, osVersion. osVersion
// "26" matches SDK_INT; nativeSetDeviceInfo copies it to BSS 0x7a0e548
// which Graphics strtol's as Android API (empty auto-stub was 0).
func (vm *VM) newDeviceStaticParams() C.jobject {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	cls := vm.ensureClassLocked("com/roblox/engine/jni/model/DeviceStaticParams")
	o := vm.newObjectLocked(cls)
	o.fields["testDeviceName"] = ""
	o.fields["appBuildVariant"] = "GooglePlay"
	o.fields["appVersion"] = vm.appVersion
	o.fields["cpu64Bit"] = true
	o.fields["deviceName"] = "tipsy"
	o.fields["deviceSku"] = "tipsy"
	o.fields["manufacturer"] = "Tipsy"
	o.fields["osVersion"] = "26"
	return idToJobject(o.id)
}

func jvalueIAt(args *C.jvalue, i int) int32 {
	return int32(jvalueI(jvalueSlot(args, i)))
}

func (vm *VM) fieldGetter(o *Object, name, sig string) (C.jobject, bool) {
	if o == nil || !strings.HasPrefix(sig, "()") {
		return jnull(), false
	}
	vm.mu.RLock()
	val, ok := o.fields[name]
	vm.mu.RUnlock()
	if !ok {
		return jnull(), false
	}
	ret := jniReturnType(sig)
	switch ret {
	case "Z":
		b := false
		switch t := val.(type) {
		case bool:
			b = t
		case int32:
			b = t != 0
		case int:
			b = t != 0
		}
		if b {
			return C.jobject(unsafe.Pointer(uintptr(1))), true
		}
		return jnull(), true
	case "I":
		var n int32
		switch t := val.(type) {
		case int32:
			n = t
		case int:
			n = int32(t)
		}
		return C.jobject(unsafe.Pointer(uintptr(uint32(n)))), true
	case "J":
		var n int64
		switch t := val.(type) {
		case int64:
			n = t
		case int:
			n = int64(t)
		}
		className := ""
		if o.class != nil {
			className = o.class.name
		}
		noteStartGamePlaceID(className, name, n)
		return C.jobject(unsafe.Pointer(uintptr(n))), true
	case "F":
		var f float32
		switch t := val.(type) {
		case float32:
			f = t
		case float64:
			f = float32(t)
		}
		return C.jobject(unsafe.Pointer(uintptr(math.Float32bits(f)))), true
	}
	if strings.HasPrefix(ret, "L") {
		switch t := val.(type) {
		case int64:
			return idToJobject(t), true
		case string:
			vm.mu.Lock()
			s := vm.newStringLocked(t)
			vm.mu.Unlock()
			return idToJobject(s.id), true
		}
	}
	return jnull(), false
}

func (vm *VM) stringFromArg(args *C.jvalue, i int) string {
	if args == nil {
		return ""
	}
	obj := jvalueLAt(args, i)
	o := vm.get(jobjectToID(uintptr(obj)))
	if o == nil {
		return ""
	}
	return o.str
}

func jniReturnType(sig string) string {
	i := strings.LastIndex(sig, ")")
	if i < 0 || i+1 >= len(sig) {
		return "V"
	}
	return sig[i+1:]
}

func (vm *VM) stubCall(class, name, sig string, retKind C.jint) C.jobject {
	_ = class
	_ = name
	ret := jniReturnType(sig)
	switch rune(retKind) {
	case 'L':
		if strings.HasPrefix(ret, "[") {
			vm.mu.Lock()
			cls := vm.ensureClassLocked("java/lang/Object")
			o := vm.newObjectLocked(cls)
			o.elems = nil
			vm.mu.Unlock()
			return idToJobject(o.id)
		}
		clsName := "java/lang/Object"
		if len(ret) >= 2 && ret[0] == 'L' && ret[len(ret)-1] == ';' {
			clsName = ret[1 : len(ret)-1]
		}
		vm.mu.Lock()
		if clsName == "java/lang/String" {
			s := vm.newStringLocked("")
			vm.mu.Unlock()
			return idToJobject(s.id)
		}
		cls := vm.ensureClassLocked(clsName)
		o := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(o.id)
	default:
		return jnull()
	}
}

func (vm *VM) stubField(name, sig string, retKind C.jint, out *C.jvalue) {
	switch rune(retKind) {
	case 'I', 'B', 'C', 'S':
		v := C.jint(0)
		if name == "SDK_INT" {
			v = 26
		}
		if name == "widthPixels" {
			v = C.jint(vm.dispW)
		}
		if name == "heightPixels" {
			v = C.jint(vm.dispH)
		}
		if name == "densityDpi" {
			v = 160
		}
		jvalueSetI(out, v)
	case 'F':
		jvalueSetF(out, 1)
	case 'Z':
		jvalueSetZ(out, 0)
	case 'J':
		jvalueSetJ(out, 0)
	case 'L':
		if sig == "Ljava/lang/String;" || strings.HasPrefix(sig, "Ljava/lang/String;") {
			s := ""
			switch name {
			case "MODEL", "DEVICE", "PRODUCT", "HARDWARE":
				s = "tipsy"
			case "MANUFACTURER", "BRAND":
				s = "Tipsy"
			case "RELEASE":
				s = "8.0.0"
			case "SDK":
				s = "26"
			}
			vm.mu.Lock()
			o := vm.newStringLocked(s)
			vm.mu.Unlock()
			jvalueSetL(out, idToJobject(o.id))
		}
	}
}

//export GoJNI_GetFieldID
func GoJNI_GetFieldID(env *C.JNIEnv, clazz C.jclass, name, sig *C.char, isStatic C.jint) C.jfieldID {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || name == nil || sig == nil {
		return jfieldNull()
	}
	cn := classNameOf(vm, clazz)
	n := C.GoString(name)
	s := C.GoString(sig)
	return internField(cn, n, s, isStatic != 0)
}

//export GoJNI_GetField
func GoJNI_GetField(env *C.JNIEnv, obj C.jobject, clazz C.jclass, fieldID C.jfieldID, isStatic C.jint, retKind C.jint, out *C.jvalue) {
	jvalueZero(out)
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	class, name, sig, _, ok := parseField(fieldID)
	if !ok {
		return
	}
	var o *Object
	if isStatic != 0 {
		n := classNameOf(vm, clazz)
		vm.mu.RLock()
		if c := vm.classes[n]; c != nil {
			o = c.obj
		}
		vm.mu.RUnlock()
	} else {
		o = vm.get(jobjectToID(uintptr(obj)))
	}
	if o == nil {
		vm.stubField(name, sig, retKind, out)
		return
	}
	vm.mu.RLock()
	val, exists := o.fields[name]
	vm.mu.RUnlock()
	if !exists {
		vm.stubField(name, sig, retKind, out)
		return
	}
	switch rune(retKind) {
	case 'L':
		switch t := val.(type) {
		case int64:
			if vm.get(t) != nil {
				vm.addLocal(unsafe.Pointer(env), t)
			}
			jvalueSetL(out, idToJobject(t))
		case string:
			vm.mu.Lock()
			s := vm.newStringOn(unsafe.Pointer(env), t)
			vm.mu.Unlock()
			jvalueSetL(out, idToJobject(s.id))
		}
	case 'I':
		switch t := val.(type) {
		case int32:
			jvalueSetI(out, C.jint(t))
		case int:
			jvalueSetI(out, C.jint(t))
		}
	case 'F':
		if f, ok := val.(float32); ok {
			jvalueSetF(out, C.jfloat(f))
		}
	case 'J':
		if i, ok := val.(int64); ok {
			jvalueSetJ(out, C.jlong(i))
			noteStartGamePlaceID(class, name, i)
		}
	case 'Z':
		b := false
		switch t := val.(type) {
		case bool:
			b = t
		case int32:
			b = t != 0
		case int:
			b = t != 0
		}
		if b {
			jvalueSetZ(out, 1)
		} else {
			jvalueSetZ(out, 0)
		}
	}
}

//export GoJNI_SetField
func GoJNI_SetField(env *C.JNIEnv, obj C.jobject, clazz C.jclass, fieldID C.jfieldID, val C.jvalue, isStatic C.jint, retKind C.jint) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	class, name, _, _, ok := parseField(fieldID)
	if !ok {
		return
	}
	o := vm.get(jobjectToID(uintptr(obj)))
	if isStatic != 0 {
		n := classNameOf(vm, clazz)
		vm.mu.RLock()
		if c := vm.classes[n]; c != nil {
			o = c.obj
		}
		vm.mu.RUnlock()
	}
	if o == nil {
		return
	}
	var place int64
	sawPlace := false
	vm.mu.Lock()
	if o.fields == nil {
		o.fields = make(map[string]any)
	}
	switch rune(retKind) {
	case 'L':
		vm.storeFieldObjLocked(o, name, jobjectToID(uintptr(jvalueL(&val))))
	case 'I':
		o.fields[name] = int32(jvalueI(&val))
	case 'J':
		place = int64(jvalueJ(&val))
		o.fields[name] = place
		sawPlace = true
	case 'Z':
		o.fields[name] = jvalueI(&val) != 0
	}
	vm.mu.Unlock()
	if sawPlace {
		noteStartGamePlaceID(class, name, place)
	}
}

// maxGuestStringUnits bounds GoJNI_NewString's guest-provided length before
// allocation. 16 Mi UTF-16 units (32 MiB) is far beyond any string the
// official client constructs through this entry point, and keeps a bogus
// positive jsize from forcing a multi-gigabyte allocation.
const maxGuestStringUnits = 16 << 20

//export GoJNI_NewString
func GoJNI_NewString(env *C.JNIEnv, unicode *C.jchar, len C.jsize) C.jstring {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || len < 0 || len > maxGuestStringUnits {
		return jstringOf(jnull())
	}
	n := int(len)
	runes := make([]uint16, n)
	if unicode != nil && n > 0 {
		slice := unsafe.Slice((*uint16)(unsafe.Pointer(unicode)), n)
		copy(runes, slice)
	}
	s := string(utf16.Decode(runes))
	vm.mu.Lock()
	o := vm.newStringOn(unsafe.Pointer(env), s)
	vm.mu.Unlock()
	return jstringOf(idToJobject(o.id))
}

func utf16UnitCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

//export GoJNI_GetStringLength
func GoJNI_GetStringLength(env *C.JNIEnv, str C.jstring) C.jsize {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return 0
	}
	o := vm.get(jobjectToID(uintptr(str)))
	if o == nil {
		return 0
	}
	return C.jsize(utf16UnitCount(o.str))
}

//export GoJNI_GetStringChars
func GoJNI_GetStringChars(env *C.JNIEnv, str C.jstring, isCopy *C.jboolean) *C.jchar {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return nil
	}
	if isCopy != nil {
		*isCopy = C.JNI_TRUE
	}
	o := vm.get(jobjectToID(uintptr(str)))
	if o == nil {
		return nil
	}
	u := utf16.Encode([]rune(o.str))
	if len(u) == 0 {
		u = []uint16{0}
	} else {
		u = append(u, 0)
	}
	p := C.malloc(C.size_t(len(u) * 2))
	if p == nil {
		return nil
	}
	dst := unsafe.Slice((*uint16)(p), len(u))
	copy(dst, u)
	return (*C.jchar)(p)
}

//export GoJNI_ReleaseStringChars
func GoJNI_ReleaseStringChars(env *C.JNIEnv, str C.jstring, chars *C.jchar) {
	_ = env
	_ = str
	if chars != nil {
		C.free(unsafe.Pointer(chars))
	}
}

//export GoJNI_NewStringUTF
func GoJNI_NewStringUTF(env *C.JNIEnv, utf *C.char) C.jstring {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jstringOf(jnull())
	}
	s := ""
	if utf != nil {
		s = C.GoString(utf)
	}
	vm.mu.Lock()
	o := vm.newStringOn(unsafe.Pointer(env), s)
	vm.mu.Unlock()
	return jstringOf(idToJobject(o.id))
}

//export GoJNI_GetStringUTFLength
func GoJNI_GetStringUTFLength(env *C.JNIEnv, str C.jstring) C.jsize {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return 0
	}
	o := vm.get(jobjectToID(uintptr(str)))
	if o == nil {
		return 0
	}
	return C.jsize(len(o.str))
}

//export GoJNI_GetStringUTFChars
func GoJNI_GetStringUTFChars(env *C.JNIEnv, str C.jstring, isCopy *C.jboolean) *C.char {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return nil
	}
	if isCopy != nil {
		*isCopy = C.JNI_TRUE
	}
	o := vm.get(jobjectToID(uintptr(str)))
	if o == nil {
		return nil
	}
	return C.CString(o.str)
}

//export GoJNI_ReleaseStringUTFChars
func GoJNI_ReleaseStringUTFChars(env *C.JNIEnv, str C.jstring, chars *C.char) {
	_ = env
	_ = str
	if chars != nil {
		C.free(unsafe.Pointer(chars))
	}
}

// unitRegion validates a JNI string-region request and returns its start and
// length in UTF-16/code units. Negative start or length is invalid (never
// reach unsafe.Slice or slice indexing with a negative count). The
// `start > total || length > total-start` form cannot overflow on huge
// positive start/length the way `start+length > total` can.
func unitRegion(start, length, total int) (s, n int, ok bool) {
	if start < 0 || length < 0 || start > total || length > total-start {
		return 0, 0, false
	}
	return start, length, true
}

// byteRegion validates a JNI primitive-array-region request and returns its
// byte offset and byte length. Negative start or length is invalid; es is the
// element size in bytes. Bounds are checked in element units so a huge
// positive start/length cannot wrap start*es or length*es.
func byteRegion(start, length, total, es int) (off, n int, ok bool) {
	if start < 0 || length < 0 || es <= 0 || total < 0 {
		return 0, 0, false
	}
	units := total / es
	if start > units || length > units-start {
		return 0, 0, false
	}
	off = start * es
	n = length * es
	return off, n, true
}

//export GoJNI_GetStringRegion
func GoJNI_GetStringRegion(env *C.JNIEnv, str C.jstring, start, length C.jsize, buf *C.jchar) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || buf == nil {
		return
	}
	o := vm.get(jobjectToID(uintptr(str)))
	if o == nil {
		return
	}
	u := utf16.Encode([]rune(o.str))
	s, n, ok := unitRegion(int(start), int(length), len(u))
	if !ok {
		return
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(buf)), n)
	copy(dst, u[s:s+n])
}

//export GoJNI_GetStringUTFRegion
func GoJNI_GetStringUTFRegion(env *C.JNIEnv, str C.jstring, start, length C.jsize, buf *C.char) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || buf == nil {
		return
	}
	o := vm.get(jobjectToID(uintptr(str)))
	if o == nil {
		return
	}
	b := []byte(o.str)
	s, n, ok := unitRegion(int(start), int(length), len(b))
	if !ok {
		return
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(buf)), n)
	copy(dst, b[s:s+n])
}

//export GoJNI_GetArrayLength
func GoJNI_GetArrayLength(env *C.JNIEnv, array C.jarray) C.jsize {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return 0
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil {
		return 0
	}
	vm.mu.RLock()
	n := arrayLength(o)
	vm.mu.RUnlock()
	return C.jsize(n)
}

// arrayLength translates primitive backing bytes into JNI element counts.
// byte[] and boolean[] happen to have one-byte elements; every wider primitive
// must divide by its ABI size before GetArrayLength is exposed to native code.
func arrayLength(o *Object) int {
	if o == nil {
		return 0
	}
	if o.bytes != nil {
		size := elemSize(o.arrKind)
		if size <= 0 {
			return 0
		}
		return len(o.bytes) / size
	}
	return len(o.elems)
}

//export GoJNI_NewObjectArray
func GoJNI_NewObjectArray(env *C.JNIEnv, length C.jsize, clazz C.jclass, init C.jobject) C.jobjectArray {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jobjectArrayOf(jnull())
	}
	n := int(length)
	if n < 0 {
		n = 0
	}
	initID := jobjectToID(uintptr(init))
	vm.mu.Lock()
	cls := vm.classes["java/lang/Object"]
	o := vm.newObjectOn(unsafe.Pointer(env), cls)
	o.elems = make([]int64, n)
	for i := range o.elems {
		o.elems[i] = initID
		vm.replaceHeapEdgeLocked(o, 0, initID)
	}
	vm.mu.Unlock()
	_ = clazz
	return jobjectArrayOf(idToJobject(o.id))
}

//export GoJNI_GetObjectArrayElement
func GoJNI_GetObjectArrayElement(env *C.JNIEnv, array C.jobjectArray, index C.jsize) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jnull()
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil {
		return jnull()
	}
	i := int(index)
	vm.mu.RLock()
	if i < 0 || i >= len(o.elems) {
		vm.mu.RUnlock()
		return jnull()
	}
	id := o.elems[i]
	vm.mu.RUnlock()
	if id == 0 {
		return jnull()
	}
	if vm.get(id) != nil {
		vm.addLocal(unsafe.Pointer(env), id)
	}
	return idToJobject(id)
}

//export GoJNI_SetObjectArrayElement
func GoJNI_SetObjectArrayElement(env *C.JNIEnv, array C.jobjectArray, index C.jsize, val C.jobject) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil {
		return
	}
	i := int(index)
	newID := jobjectToID(uintptr(val))
	vm.mu.Lock()
	if i < 0 || i >= len(o.elems) {
		vm.mu.Unlock()
		return
	}
	vm.replaceHeapEdgeLocked(o, o.elems[i], newID)
	o.elems[i] = newID
	vm.mu.Unlock()
}

func elemSize(kind int) int {
	switch kind {
	case 'Z', 'B':
		return 1
	case 'C', 'S':
		return 2
	case 'I', 'F':
		return 4
	case 'J', 'D':
		return 8
	default:
		return 1
	}
}

//export GoJNI_NewArray
func GoJNI_NewArray(env *C.JNIEnv, typeKind C.jint, length C.jsize) C.jarray {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jarrayOf(jnull())
	}
	n := int(length)
	if n < 0 {
		n = 0
	}
	vm.mu.Lock()
	cls := vm.classes["java/lang/Object"]
	o := vm.newObjectOn(unsafe.Pointer(env), cls)
	o.arrKind = int(typeKind)
	o.bytes = make([]byte, n*elemSize(int(typeKind)))
	vm.mu.Unlock()
	return jarrayOf(idToJobject(o.id))
}

//export GoJNI_GetArrayElements
func GoJNI_GetArrayElements(env *C.JNIEnv, array C.jarray, isCopy *C.jboolean, typeKind C.jint) unsafe.Pointer {
	_ = typeKind
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return nil
	}
	if isCopy != nil {
		*isCopy = C.JNI_TRUE
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil {
		return nil
	}
	n := len(o.bytes)
	if n == 0 {
		n = 1
	}
	p := C.malloc(C.size_t(n))
	if p == nil {
		return nil
	}
	if len(o.bytes) > 0 {
		copy(unsafe.Slice((*byte)(p), len(o.bytes)), o.bytes)
	}
	return p
}

//export GoJNI_ReleaseArrayElements
func GoJNI_ReleaseArrayElements(env *C.JNIEnv, array C.jarray, elems unsafe.Pointer, mode C.jint, typeKind C.jint) {
	_ = typeKind
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || elems == nil {
		return
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o != nil && mode != C.JNI_ABORT && len(o.bytes) > 0 {
		copy(o.bytes, unsafe.Slice((*byte)(elems), len(o.bytes)))
	}
	// JNI_COMMIT copies back but keeps the buffer owned by the caller; only
	// mode 0 (copy+free) and JNI_ABORT (free only) release it.
	if mode != C.JNI_COMMIT {
		C.free(elems)
	}
}

type criticalPin struct {
	pinner runtime.Pinner
	obj    *Object
}

var criticalPins sync.Map // uintptr -> *criticalPin

//export GoJNI_GetPrimitiveArrayCritical
func GoJNI_GetPrimitiveArrayCritical(env *C.JNIEnv, array C.jarray, isCopy *C.jboolean) unsafe.Pointer {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return nil
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil {
		return nil
	}
	if len(o.bytes) == 0 {
		return GoJNI_GetArrayElements(env, array, isCopy, 'B')
	}
	pin := &criticalPin{obj: o}
	pin.pinner.Pin(o)
	pin.pinner.Pin(unsafe.SliceData(o.bytes))
	ptr := unsafe.Pointer(unsafe.SliceData(o.bytes))
	criticalPins.Store(uintptr(ptr), pin)
	if isCopy != nil {
		*isCopy = C.JNI_FALSE
	}
	return ptr
}

//export GoJNI_ReleasePrimitiveArrayCritical
func GoJNI_ReleasePrimitiveArrayCritical(env *C.JNIEnv, array C.jarray, carray unsafe.Pointer, mode C.jint) {
	if carray == nil {
		return
	}
	if v, ok := criticalPins.LoadAndDelete(uintptr(carray)); ok {
		_ = mode
		v.(*criticalPin).pinner.Unpin()
		return
	}
	GoJNI_ReleaseArrayElements(env, array, carray, mode, 'B')
}

//export GoJNI_GetArrayRegion
func GoJNI_GetArrayRegion(env *C.JNIEnv, array C.jarray, start, length C.jsize, buf unsafe.Pointer, typeKind C.jint) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || buf == nil {
		return
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil {
		return
	}
	es := elemSize(int(typeKind))
	off, n, ok := byteRegion(int(start), int(length), len(o.bytes), es)
	if !ok {
		return
	}
	copy(unsafe.Slice((*byte)(buf), n), o.bytes[off:off+n])
}

//export GoJNI_SetArrayRegion
func GoJNI_SetArrayRegion(env *C.JNIEnv, array C.jarray, start, length C.jsize, buf unsafe.Pointer, typeKind C.jint) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || buf == nil {
		return
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil {
		return
	}
	es := elemSize(int(typeKind))
	off, n, ok := byteRegion(int(start), int(length), len(o.bytes), es)
	if !ok {
		return
	}
	copy(o.bytes[off:off+n], unsafe.Slice((*byte)(buf), n))
}

// maxRegisteredNatives bounds one RegisterNatives batch before unsafe.Slice.
// The official client registers small batches (tens); a bogus positive count
// must fail with JNI_ERR instead of slicing past the real C array.
const maxRegisteredNatives = 1 << 14

//export GoJNI_RegisterNatives
func GoJNI_RegisterNatives(env *C.JNIEnv, clazz C.jclass, methods *C.JNINativeMethod, nMethods C.jint) C.jint {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || methods == nil {
		return C.JNI_OK
	}
	cn := classNameOf(vm, clazz)
	n := int(nMethods)
	if n < 0 || n > maxRegisteredNatives {
		return C.JNI_ERR
	}
	slice := unsafe.Slice(methods, n)
	vm.mu.Lock()
	defer vm.mu.Unlock()
	for i := 0; i < n; i++ {
		name := C.GoString(slice[i].name)
		sig := C.GoString(slice[i].signature)
		key := methodLogName(cn, name, sig)
		vm.natives[key] = uintptr(slice[i].fnPtr)
		logging.Logger(logging.CatJNI).Info("[jni] RegisterNatives", "method", key, "fn", fmt.Sprintf("%#x", uintptr(slice[i].fnPtr)))
	}
	return C.JNI_OK
}

//export GoJNI_UnregisterNatives
func GoJNI_UnregisterNatives(env *C.JNIEnv, clazz C.jclass) C.jint {
	_ = env
	_ = clazz
	return C.JNI_OK
}

// testRegisterNativesCount calls GoJNI_RegisterNatives with a non-nil
// one-entry native method table and the given count, translating the JNI
// result to a Go int (0=OK, -1=ERR) because test files in this package
// cannot name C constants.
func testRegisterNativesCount(n int32) int {
	methods := make([]C.JNINativeMethod, 1)
	switch GoJNI_RegisterNatives(nil, jclassNull(), &methods[0], C.jint(n)) {
	case C.JNI_OK:
		return 0
	case C.JNI_ERR:
		return -1
	default:
		return -2
	}
}

//export GoJNI_MonitorEnter
func GoJNI_MonitorEnter(env *C.JNIEnv, obj C.jobject) C.jint {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_ERR
	}
	id := jobjectToID(uintptr(obj))
	vm.mu.RLock()
	m := vm.monitors[id]
	vm.mu.RUnlock()
	if m == nil {
		vm.mu.Lock()
		m = vm.monitors[id]
		if m == nil {
			m = &sync.Mutex{}
			vm.monitors[id] = m
		}
		vm.mu.Unlock()
	}
	m.Lock()
	return C.JNI_OK
}

//export GoJNI_MonitorExit
func GoJNI_MonitorExit(env *C.JNIEnv, obj C.jobject) C.jint {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_ERR
	}
	id := jobjectToID(uintptr(obj))
	vm.mu.RLock()
	m := vm.monitors[id]
	vm.mu.RUnlock()
	if m != nil {
		m.Unlock()
	}
	return C.JNI_OK
}

//export GoJNI_GetJavaVM
func GoJNI_GetJavaVM(env *C.JNIEnv, vmOut **C.JavaVM) C.jint {
	_ = env
	if vmOut != nil {
		*vmOut = C.tipsy_jni_java_vm()
	}
	return C.JNI_OK
}

//export GoJNI_EnvDetached
func GoJNI_EnvDetached(env *C.JNIEnv) {
	vm := globalVM.Load()
	if vm != nil {
		vm.detachThreadState(unsafe.Pointer(env))
	}
}

//export GoJNI_ExceptionCheck
func GoJNI_ExceptionCheck(env *C.JNIEnv) C.jboolean {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_FALSE
	}
	if vm.pending(unsafe.Pointer(env)) != 0 {
		return C.JNI_TRUE
	}
	return C.JNI_FALSE
}

//export GoJNI_NewDirectByteBuffer
func GoJNI_NewDirectByteBuffer(env *C.JNIEnv, address unsafe.Pointer, capacity C.jlong) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jnull()
	}
	vm.mu.Lock()
	cls := vm.classes["java/lang/Object"]
	o := vm.newObjectOn(unsafe.Pointer(env), cls)
	o.direct = address
	o.cap = int64(capacity)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

//export GoJNI_GetDirectBufferAddress
func GoJNI_GetDirectBufferAddress(env *C.JNIEnv, buf C.jobject) unsafe.Pointer {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return nil
	}
	o := vm.get(jobjectToID(uintptr(buf)))
	if o == nil {
		return nil
	}
	return o.direct
}

//export GoJNI_GetDirectBufferCapacity
func GoJNI_GetDirectBufferCapacity(env *C.JNIEnv, buf C.jobject) C.jlong {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return -1
	}
	o := vm.get(jobjectToID(uintptr(buf)))
	if o == nil {
		return -1
	}
	return C.jlong(o.cap)
}

//export GoJNI_GetObjectRefType
func GoJNI_GetObjectRefType(env *C.JNIEnv, obj C.jobject) C.jobjectRefType {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || jobjectToID(uintptr(obj)) == 0 {
		return C.JNIInvalidRefType
	}
	o := vm.get(jobjectToID(uintptr(obj)))
	if o == nil {
		return C.JNIInvalidRefType
	}
	if o.global {
		return C.JNIGlobalRefType
	}
	return C.JNILocalRefType
}

//export GoJNI_NewWeakGlobalRef
func GoJNI_NewWeakGlobalRef(env *C.JNIEnv, obj C.jobject) C.jweak {
	return jweakOf(GoJNI_NewGlobalRef(env, obj))
}

//export GoJNI_DeleteWeakGlobalRef
func GoJNI_DeleteWeakGlobalRef(env *C.JNIEnv, ref C.jweak) {
	GoJNI_DeleteGlobalRef(env, asJobjectFromWeak(ref))
}

//export GoJNI_Unimplemented
func GoJNI_Unimplemented(name *C.char) {
	logf("[jni] unimplemented: " + C.GoString(name))
}
