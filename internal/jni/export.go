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
	"time"
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

// stubDispatchLogged dedupes the dispatch-time stub-fallback diagnostic:
// one log per unique class.name+sig|retKind for the life of the process.
var stubDispatchLogged sync.Map

// logStubDispatch records that the JNIEnv dispatch path is about to answer
// an unresolved method through vm.stubCall. Observation-only: it fires after
// dispatch returned handled=false and before stubCall runs, so every fallback
// return and side effect is preserved exactly. It never fires for methods
// vm.dispatch handles (implemented paths, <init>, field getters).
func logStubDispatch(class, name, sig string, retKind rune) {
	key := methodLogName(class, name, sig) + "|" + string(retKind)
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
	v, handled := vm.dispatch(obj, class, name, sig, args)
	if handled {
		return v, true
	}
	logStubDispatch(class, name, sig, retKind)
	logLifecycleStubArgs(class, name, sig, args)
	return vm.stubCall(class, name, sig, C.jint(retKind)), false
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
func (vm *VM) findClassByName(name string) C.jobject {
	if name == "" {
		return jnull()
	}
	vm.mu.Lock()
	cls := vm.classes[name]
	if cls == nil || cls.obj == nil {
		logging.Logger(logging.CatJNI).Error("[jni] auto-class: " + name)
		cls = vm.ensureClassLocked(name)
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
		if n, ok := o.fields["name"].(string); ok {
			return n
		}
	}
	for name, c := range vm.classes {
		if c.obj != nil && c.obj.id == id {
			return name
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
	vm.mu.Lock()
	subC, supC := vm.classes[subN], vm.classes[supN]
	vm.mu.Unlock()
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
	vm.mu.Lock()
	vm.pending = jobjectToID(uintptr(asJobjectFromThrow(obj)))
	vm.mu.Unlock()
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
	o := vm.newObjectLocked(cls)
	if msg != nil {
		o.str = C.GoString(msg)
	}
	vm.pending = o.id
	vm.mu.Unlock()
	return C.JNI_OK
}

//export GoJNI_ExceptionOccurred
func GoJNI_ExceptionOccurred(env *C.JNIEnv) C.jthrowable {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return jthrowableOf(jnull())
	}
	vm.mu.Lock()
	id := vm.pending
	vm.mu.Unlock()
	return jthrowableOf(idToJobject(id))
}

//export GoJNI_ExceptionDescribe
func GoJNI_ExceptionDescribe(env *C.JNIEnv) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	vm.mu.Lock()
	id := vm.pending
	vm.mu.Unlock()
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
	vm.mu.Lock()
	vm.pending = 0
	vm.mu.Unlock()
}

//export GoJNI_FatalError
func GoJNI_FatalError(env *C.JNIEnv, msg *C.char) {
	_ = env
	logf("[jni] FatalError: " + C.GoString(msg))
}

//export GoJNI_PushLocalFrame
func GoJNI_PushLocalFrame(env *C.JNIEnv, capacity C.jint) C.jint {
	_ = env
	_ = capacity
	return C.JNI_OK
}

//export GoJNI_PopLocalFrame
func GoJNI_PopLocalFrame(env *C.JNIEnv, result C.jobject) C.jobject {
	_ = env
	return result
}

//export GoJNI_NewGlobalRef
func GoJNI_NewGlobalRef(env *C.JNIEnv, lobj C.jobject) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return lobj
	}
	o := vm.get(jobjectToID(uintptr(lobj)))
	if o != nil {
		vm.mu.Lock()
		o.global = true
		vm.mu.Unlock()
	}
	return lobj
}

//export GoJNI_DeleteGlobalRef
func GoJNI_DeleteGlobalRef(env *C.JNIEnv, gref C.jobject) {
	_ = env
	_ = gref
}

//export GoJNI_DeleteLocalRef
func GoJNI_DeleteLocalRef(env *C.JNIEnv, obj C.jobject) {
	_ = env
	_ = obj
}

//export GoJNI_IsSameObject
func GoJNI_IsSameObject(env *C.JNIEnv, obj1, obj2 C.jobject) C.jboolean {
	_ = env
	if jobjectToID(uintptr(obj1)) == jobjectToID(uintptr(obj2)) {
		return C.JNI_TRUE
	}
	return C.JNI_FALSE
}

//export GoJNI_NewLocalRef
func GoJNI_NewLocalRef(env *C.JNIEnv, ref C.jobject) C.jobject {
	_ = env
	return ref
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
	o := vm.newObjectLocked(cls)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

//export GoJNI_NewObjectA
func GoJNI_NewObjectA(env *C.JNIEnv, clazz C.jclass, methodID C.jmethodID, args *C.jvalue) C.jobject {
	obj := GoJNI_AllocObject(env, clazz)
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
	return jclassOf(idToJobject(o.class.obj.id))
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
	if n != "<init>" && !isImplementedMethod(n, s) {
		logMissingMethod(cn, n, s)
	}
	return allocMethod(cn, n, s, isStatic != 0)
}

//export GoJNI_CallA
func GoJNI_CallA(env *C.JNIEnv, obj C.jobject, clazz C.jclass, methodID C.jmethodID, args *C.jvalue, isStatic C.jint, retKind C.jint, out *C.jvalue) {
	C.tipsy_jvalue_zero(out)
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	class, name, sig, _, ok := parseMethod(methodID)
	if !ok {
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
	v, _ := callDispatchOrStub(vm, obj, class, name, sig, args, rune(retKind))
	switch rune(retKind) {
	case 'L':
		C.tipsy_jvalue_set_l(out, v)
	case 'I', 'B', 'C', 'S', 'Z':
		C.tipsy_jvalue_set_i(out, C.jint(v))
	case 'J':
		C.tipsy_jvalue_set_j(out, C.jlong(v))
	case 'F':
		C.tipsy_jvalue_set_f(out, C.jfloat(math.Float32frombits(uint32(uintptr(v)))))
	default:
	}
}

func (vm *VM) dispatch(obj C.jobject, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	o := vm.get(jobjectToID(uintptr(obj)))
	key := name + sig
	if name == "<init>" {
		return obj, true
	}
	if v, ok := vm.dispatchInput(o, class, name, sig, args); ok {
		return v, true
	}
	if v, ok := vm.dispatchConnectivity(o, class, name, sig, args); ok {
		return v, true
	}
	if v, ok := vm.dispatchInsets(class, name, sig, args); ok {
		return v, true
	}
	if v, ok := vm.dispatchNativeHelper(o, class, name, sig, args); ok {
		return v, true
	}
	if v, ok := vm.dispatchTextInput(o, class, name, sig, args); ok {
		return v, true
	}
	if v, ok := vm.dispatchTextConnection(o, class, name, sig, args); ok {
		return v, true
	}
	if v, ok := vm.dispatchLuaTextBox(o, class, name, sig, args); ok {
		return v, true
	}
	switch key {
	case "getFilesDir()Ljava/io/File;":
		vm.mu.Lock()
		f := vm.newFileLocked(vm.filesDir)
		vm.mu.Unlock()
		return idToJobject(f.id), true
	case "getFilesDir()Ljava/lang/String;":
		vm.mu.Lock()
		s := vm.newStringLocked(vm.filesDir)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getAppVersion()Ljava/lang/String;":
		vm.mu.Lock()
		s := vm.newStringLocked("2.734.917")
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getCacheDir()Ljava/io/File;":
		vm.mu.Lock()
		f := vm.newFileLocked(vm.cacheDir)
		vm.mu.Unlock()
		return idToJobject(f.id), true
	case "getObbDir()Ljava/io/File;":
		vm.mu.Lock()
		f := vm.newFileLocked(vm.obbDir)
		vm.mu.Unlock()
		return idToJobject(f.id), true
	case "getExternalFilesDir(Ljava/lang/String;)Ljava/io/File;":
		vm.mu.Lock()
		f := vm.newFileLocked(vm.filesDir)
		vm.mu.Unlock()
		return idToJobject(f.id), true
	case "getAssets()Landroid/content/res/AssetManager;":
		vm.mu.Lock()
		cls := vm.classes["android/content/res/AssetManager"]
		a := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(a.id), true
	case "getWindow()Landroid/view/Window;":
		vm.mu.Lock()
		cls := vm.classes["android/view/Window"]
		w := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(w.id), true
	case "getApplicationContext()Landroid/content/Context;":
		if o != nil {
			return idToJobject(o.id), true
		}
		return obj, true
	case "getPackageName()Ljava/lang/String;":
		vm.mu.Lock()
		s := vm.newStringLocked("com.roblox.client")
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getAbsolutePath()Ljava/lang/String;", "getPath()Ljava/lang/String;":
		p := ""
		if o != nil {
			p = o.str
		}
		vm.mu.Lock()
		s := vm.newStringLocked(p)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getSurface()Landroid/view/Surface;":
		vm.mu.Lock()
		cls := vm.classes["android/view/Surface"]
		s := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getDecorView()Landroid/view/View;":
		vm.mu.Lock()
		cls := vm.classes["android/view/View"]
		v := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(v.id), true
	case "getWindowManager()Landroid/view/WindowManager;":
		vm.mu.Lock()
		cls := vm.classes["android/view/WindowManager"]
		w := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(w.id), true
	case "getDefaultDisplay()Landroid/view/Display;":
		vm.mu.Lock()
		cls := vm.classes["android/view/Display"]
		d := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(d.id), true
	case "getResources()Landroid/content/res/Resources;":
		vm.mu.Lock()
		cls := vm.classes["android/content/res/Resources"]
		r := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(r.id), true
	case "getPackageManager()Landroid/content/pm/PackageManager;":
		vm.mu.Lock()
		cls := vm.classes["android/content/pm/PackageManager"]
		p := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(p.id), true
	case "getApplicationInfo()Landroid/content/pm/ApplicationInfo;":
		vm.mu.Lock()
		cls := vm.classes["android/content/pm/ApplicationInfo"]
		a := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(a.id), true
	case "getLocale()Ljava/lang/String;", "getRobloxLocale()Ljava/lang/String;", "getGameLocale()Ljava/lang/String;":
		vm.mu.Lock()
		s := vm.newStringLocked("en_US")
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getSystemService(Ljava/lang/String;)Ljava/lang/Object;":
		return vm.lookupSystemService(vm.stringFromArg(args, 0)), true
	case "getClassLoader()Ljava/lang/ClassLoader;":
		vm.mu.Lock()
		cls := vm.classes["java/lang/ClassLoader"]
		c := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(c.id), true
	case "findClass(Ljava/lang/String;)Ljava/lang/Class;":
		fcName := vm.stringFromArg(args, 0)
		logFindClassName(fcName)
		return vm.findClassByName(fcName), true
	case "getMainLooper()Landroid/os/Looper;":
		vm.mu.Lock()
		cls := vm.classes["android/os/Looper"]
		l := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(l.id), true
	case "getRuntime()Ljava/lang/Runtime;":
		vm.mu.Lock()
		cls := vm.classes["java/lang/Runtime"]
		r := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(r.id), true
	case "getMetrics(Landroid/util/DisplayMetrics;)V":
		if args != nil {
			m := vm.get(jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 0))))
			vm.mu.Lock()
			vm.fillDisplayMetricsLocked(m)
			vm.mu.Unlock()
		}
		return jnull(), true
	case "getDisplayMetrics()Landroid/util/DisplayMetrics;":
		vm.mu.Lock()
		cls := vm.ensureClassLocked("android/util/DisplayMetrics")
		m := vm.newObjectLocked(cls)
		vm.fillDisplayMetricsLocked(m)
		vm.mu.Unlock()
		return idToJobject(m.id), true
	case "getScreenPhysicalSizeInMillimeters(Landroid/content/Context;)Landroid/graphics/Point;":
		// java/lang/Class.getScreenPhysicalSizeInMillimeters(Context):
		// static display-metrics helper the engine queries during init.
		// Returns the host screen's physical size as android.graphics.Point
		// {int x, int y} in millimeters (X-server-reported when available).
		w, h := vm.screenPhysicalSizeMM()
		if w <= 0 || h <= 0 {
			logging.Logger(logging.CatJNI).Error("[jni] getScreenPhysicalSizeInMillimeters: no display geometry (x=0 or y=0)")
		}
		vm.mu.Lock()
		cls := vm.ensureClassLocked("android/graphics/Point")
		p := vm.newObjectLocked(cls)
		if p.fields == nil {
			p.fields = make(map[string]any)
		}
		p.fields["x"] = w
		p.fields["y"] = h
		vm.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] getScreenPhysicalSizeInMillimeters", "x", w, "y", h)
		return idToJobject(p.id), true
	case "getLocales()Landroid/os/LocaleList;":
		vm.mu.Lock()
		locale := vm.newObjectLocked(vm.ensureClassLocked("java/util/Locale"))
		locale.fields["language"] = "en"
		locale.fields["script"] = ""
		locale.fields["country"] = "US"
		locale.fields["variant"] = ""
		list := vm.newObjectLocked(vm.ensureClassLocked("android/os/LocaleList"))
		list.elems = []int64{locale.id}
		vm.mu.Unlock()
		return idToJobject(list.id), true
	case "getLanguage()Ljava/lang/String;", "getScript()Ljava/lang/String;", "getCountry()Ljava/lang/String;", "getVariant()Ljava/lang/String;":
		field := map[string]string{
			"getLanguage()Ljava/lang/String;": "language",
			"getScript()Ljava/lang/String;":   "script",
			"getCountry()Ljava/lang/String;":  "country",
			"getVariant()Ljava/lang/String;":  "variant",
		}[key]
		value := ""
		if o != nil {
			value, _ = o.fields[field].(string)
		}
		vm.mu.Lock()
		s := vm.newStringLocked(value)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getNativeHelper()Lcom/roblox/client/startup/NativeHelper;":
		vm.mu.Lock()
		cls := vm.ensureClassLocked("com/roblox/client/startup/NativeHelper")
		h := vm.newObjectLocked(cls)
		vm.mu.Unlock()
		return idToJobject(h.id), true
	case "getDeviceStaticParams()Lcom/roblox/engine/jni/model/DeviceStaticParams;":
		return vm.newDeviceStaticParams(), true
	case "setDeviceStaticParams(Lcom/roblox/engine/jni/model/DeviceStaticParams;)V":
		return obj, true
	case "bootstrapTheApp()V":
		if onBootstrap != nil {
			onBootstrap(uintptr(vm.envRaw), uintptr(obj))
		}
		return obj, true
	case "addBoolean(Ljava/lang/String;ZZ)V",
		"addInt(Ljava/lang/String;II)V",
		"addString(Ljava/lang/String;Ljava/lang/String;Z)V",
		"gameActivity_onFlagsFailed()V",
		"gameActivity_onFlagsLoaded()V":
		return obj, true
	case "loadLibrary(Ljava/lang/String;)V", "gc()V":
		return jnull(), true
	case "getProperty(Ljava/lang/String;)Ljava/lang/String;":
		vm.mu.Lock()
		s := vm.newStringLocked("")
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getBytes(Ljava/lang/String;)[B":
		// Roblox's syncTextboxTextAndCursorPosition2 JNI wrapper uses the
		// ordinary Java String.getBytes("UTF-8") path before handing the
		// textbox snapshot to native code. Preserve Java's standard UTF-8
		// bytes, including non-ASCII text, without logging either source or
		// result. Unsupported/nil charsets and non-String receivers return
		// null honestly; the supplied APK calls only the UTF-8 form.
		if class != "java/lang/String" || o == nil {
			return jnull(), true
		}
		charset := vm.stringFromArg(args, 0)
		if !strings.EqualFold(charset, "UTF-8") &&
			!strings.EqualFold(charset, "UTF8") &&
			!strings.EqualFold(charset, "unicode-1-1-utf-8") {
			return jnull(), true
		}
		vm.mu.Lock()
		arrayClass := vm.ensureClassLocked("[B")
		bytes := vm.newObjectLocked(arrayClass)
		bytes.arrKind = int('B')
		bytes.bytes = append([]byte(nil), []byte(o.str)...)
		vm.mu.Unlock()
		return idToJobject(bytes.id), true
	case "currentTimeMillis()J", "getProcessTimestamp()J":
		ms := time.Now().UnixMilli()
		return C.jobject(unsafe.Pointer(uintptr(ms))), true
	case "nanoTime()J":
		ns := time.Now().UnixNano()
		return C.jobject(unsafe.Pointer(uintptr(ns))), true
	case "getAllocatableBytes()J":
		// LocalStorageManager native V3 GetMethodIDs this; later OTA
		// rbxm open can CallLongMethod. Official Java uses StatFs.
		const allocatable = 8 << 30 // 8 GiB
		return C.jobject(unsafe.Pointer(uintptr(allocatable))), true
	case "availableProcessors()I":
		return C.jobject(unsafe.Pointer(uintptr(runtime.NumCPU()))), true
	case "getIdentifier(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;)I":
		return jnull(), true
	case "toString()Ljava/lang/String;":
		str := ""
		if o != nil {
			str = o.str
			if str == "" {
				str = o.class.name
			}
		}
		vm.mu.Lock()
		s := vm.newStringLocked(str)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getName()Ljava/lang/String;":
		n := class
		if o != nil {
			if name, ok := o.fields["name"].(string); ok {
				n = name
			}
		}
		vm.mu.Lock()
		s := vm.newStringLocked(n)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case "getClass()Ljava/lang/Class;":
		if o != nil && o.class != nil && o.class.obj != nil {
			return idToJobject(o.class.obj.id), true
		}
		return jnull(), true
	case "size()I":
		n := 0
		if o != nil {
			n = len(o.elems)
		}
		return C.jobject(unsafe.Pointer(uintptr(n))), true
	case "isEmpty()Z":
		empty := 1
		if o != nil && len(o.elems) > 0 {
			empty = 0
		}
		return C.jobject(unsafe.Pointer(uintptr(empty))), true
	case "get(I)Ljava/lang/Object;", "get(I)Ljava/util/Locale;":
		if o == nil || args == nil {
			return jnull(), true
		}
		idx := int(jvalueIAt(args, 0))
		if idx < 0 || idx >= len(o.elems) {
			return jnull(), true
		}
		return idToJobject(o.elems[idx]), true
	case "add(Ljava/lang/Object;)Z":
		if o != nil && args != nil {
			id := jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 0)))
			vm.mu.Lock()
			o.elems = append(o.elems, id)
			vm.mu.Unlock()
		}
		return C.jobject(unsafe.Pointer(uintptr(1))), true
	case "iterator()Ljava/util/Iterator;":
		vm.mu.Lock()
		cls := vm.ensureClassLocked("java/util/Iterator")
		it := vm.newObjectLocked(cls)
		if o != nil {
			it.fields["list"] = o.id
		}
		it.fields["index"] = int32(0)
		vm.mu.Unlock()
		return idToJobject(it.id), true
	case "hasNext()Z":
		if o == nil {
			return jnull(), true
		}
		listID, _ := o.fields["list"].(int64)
		idx, _ := o.fields["index"].(int32)
		list := vm.get(listID)
		n := 0
		if list != nil {
			n = len(list.elems)
		}
		if int(idx) < n {
			return C.jobject(unsafe.Pointer(uintptr(1))), true
		}
		return jnull(), true
	case "next()Ljava/lang/Object;":
		if o == nil {
			return jnull(), true
		}
		listID, _ := o.fields["list"].(int64)
		idx, _ := o.fields["index"].(int32)
		list := vm.get(listID)
		if list == nil || int(idx) < 0 || int(idx) >= len(list.elems) {
			return jnull(), true
		}
		vm.mu.Lock()
		o.fields["index"] = idx + 1
		vm.mu.Unlock()
		return idToJobject(list.elems[idx]), true
	default:
		if o != nil {
			if v, ok := vm.fieldGetter(o, name, sig); ok {
				return v, true
			}
		}
		_ = args
		_ = class
		return jnull(), false
	}
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
	o.fields["appVersion"] = "2.734.917"
	o.fields["cpu64Bit"] = true
	o.fields["deviceName"] = "tipsy"
	o.fields["deviceSku"] = "tipsy"
	o.fields["manufacturer"] = "Tipsy"
	o.fields["osVersion"] = "26"
	return idToJobject(o.id)
}

func jvalueIAt(args *C.jvalue, i int) int32 {
	if args == nil || i < 0 {
		return 0
	}
	slice := unsafe.Slice(args, i+1)
	return int32(C.tipsy_jvalue_i(&slice[i]))
}

func (vm *VM) fieldGetter(o *Object, name, sig string) (C.jobject, bool) {
	if o == nil || !strings.HasPrefix(sig, "()") {
		return jnull(), false
	}
	val, ok := o.fields[name]
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
	obj := C.tipsy_jvalue_l_at(args, C.int(i))
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
		C.tipsy_jvalue_set_i(out, v)
	case 'F':
		C.tipsy_jvalue_set_f(out, 1)
	case 'Z':
		C.tipsy_jvalue_set_z(out, 0)
	case 'J':
		C.tipsy_jvalue_set_j(out, 0)
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
			C.tipsy_jvalue_set_l(out, idToJobject(o.id))
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
	return allocField(cn, n, s, isStatic != 0)
}

//export GoJNI_GetField
func GoJNI_GetField(env *C.JNIEnv, obj C.jobject, clazz C.jclass, fieldID C.jfieldID, isStatic C.jint, retKind C.jint, out *C.jvalue) {
	C.tipsy_jvalue_zero(out)
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	_, name, sig, _, ok := parseField(fieldID)
	if !ok {
		return
	}
	var o *Object
	if isStatic != 0 {
		n := classNameOf(vm, clazz)
		vm.mu.Lock()
		if c := vm.classes[n]; c != nil {
			o = c.obj
		}
		vm.mu.Unlock()
	} else {
		o = vm.get(jobjectToID(uintptr(obj)))
	}
	if o == nil {
		vm.stubField(name, sig, retKind, out)
		return
	}
	val, exists := o.fields[name]
	if !exists {
		vm.stubField(name, sig, retKind, out)
		return
	}
	switch rune(retKind) {
	case 'L':
		switch t := val.(type) {
		case int64:
			C.tipsy_jvalue_set_l(out, idToJobject(t))
		case string:
			vm.mu.Lock()
			s := vm.newStringLocked(t)
			vm.mu.Unlock()
			C.tipsy_jvalue_set_l(out, idToJobject(s.id))
		}
	case 'I':
		switch t := val.(type) {
		case int32:
			C.tipsy_jvalue_set_i(out, C.jint(t))
		case int:
			C.tipsy_jvalue_set_i(out, C.jint(t))
		}
	case 'F':
		if f, ok := val.(float32); ok {
			C.tipsy_jvalue_set_f(out, C.jfloat(f))
		}
	case 'J':
		if i, ok := val.(int64); ok {
			C.tipsy_jvalue_set_j(out, C.jlong(i))
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
			C.tipsy_jvalue_set_z(out, 1)
		} else {
			C.tipsy_jvalue_set_z(out, 0)
		}
	}
}

//export GoJNI_SetField
func GoJNI_SetField(env *C.JNIEnv, obj C.jobject, clazz C.jclass, fieldID C.jfieldID, val C.jvalue, isStatic C.jint, retKind C.jint) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	_, name, _, _, ok := parseField(fieldID)
	if !ok {
		return
	}
	o := vm.get(jobjectToID(uintptr(obj)))
	if isStatic != 0 {
		n := classNameOf(vm, clazz)
		vm.mu.Lock()
		if c := vm.classes[n]; c != nil {
			o = c.obj
		}
		vm.mu.Unlock()
	}
	if o == nil {
		return
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	switch rune(retKind) {
	case 'L':
		o.fields[name] = jobjectToID(uintptr(C.tipsy_jvalue_l(&val)))
	case 'I':
		o.fields[name] = int32(C.tipsy_jvalue_i(&val))
	case 'J':
		o.fields[name] = int64(C.tipsy_jvalue_j(&val))
	case 'Z':
		o.fields[name] = C.tipsy_jvalue_i(&val) != 0
	}
}

//export GoJNI_NewString
func GoJNI_NewString(env *C.JNIEnv, unicode *C.jchar, len C.jsize) C.jstring {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
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
	o := vm.newStringLocked(s)
	vm.mu.Unlock()
	return jstringOf(idToJobject(o.id))
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
	return C.jsize(len(utf16.Encode([]rune(o.str))))
}

var stringChars sync.Map // uintptr -> []uint16 backing (kept alive)

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
	stringChars.Store(uintptr(p), u)
	return (*C.jchar)(p)
}

//export GoJNI_ReleaseStringChars
func GoJNI_ReleaseStringChars(env *C.JNIEnv, str C.jstring, chars *C.jchar) {
	_ = env
	_ = str
	if chars != nil {
		C.free(unsafe.Pointer(chars))
		stringChars.Delete(uintptr(unsafe.Pointer(chars)))
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
	o := vm.newStringLocked(s)
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
	s, n := int(start), int(length)
	if s < 0 || s+n > len(u) {
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
	s, n := int(start), int(length)
	if s < 0 || s+n > len(b) {
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
	if o.bytes != nil {
		return C.jsize(len(o.bytes))
	}
	return C.jsize(len(o.elems))
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
	o := vm.newObjectLocked(cls)
	o.elems = make([]int64, n)
	for i := range o.elems {
		o.elems[i] = initID
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
	if o == nil || int(index) < 0 || int(index) >= len(o.elems) {
		return jnull()
	}
	return idToJobject(o.elems[index])
}

//export GoJNI_SetObjectArrayElement
func GoJNI_SetObjectArrayElement(env *C.JNIEnv, array C.jobjectArray, index C.jsize, val C.jobject) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	o := vm.get(jobjectToID(uintptr(array)))
	if o == nil || int(index) < 0 || int(index) >= len(o.elems) {
		return
	}
	vm.mu.Lock()
	o.elems[index] = jobjectToID(uintptr(val))
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
	o := vm.newObjectLocked(cls)
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
	C.free(elems)
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
	off := int(start) * es
	n := int(length) * es
	if off < 0 || off+n > len(o.bytes) {
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
	off := int(start) * es
	n := int(length) * es
	if off < 0 || off+n > len(o.bytes) {
		return
	}
	copy(o.bytes[off:off+n], unsafe.Slice((*byte)(buf), n))
}

//export GoJNI_RegisterNatives
func GoJNI_RegisterNatives(env *C.JNIEnv, clazz C.jclass, methods *C.JNINativeMethod, nMethods C.jint) C.jint {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil || methods == nil {
		return C.JNI_OK
	}
	cn := classNameOf(vm, clazz)
	n := int(nMethods)
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

//export GoJNI_MonitorEnter
func GoJNI_MonitorEnter(env *C.JNIEnv, obj C.jobject) C.jint {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_ERR
	}
	id := jobjectToID(uintptr(obj))
	vm.mu.Lock()
	m := vm.monitors[id]
	if m == nil {
		m = &sync.Mutex{}
		vm.monitors[id] = m
	}
	vm.mu.Unlock()
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
	vm.mu.Lock()
	m := vm.monitors[id]
	vm.mu.Unlock()
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

//export GoJNI_ExceptionCheck
func GoJNI_ExceptionCheck(env *C.JNIEnv) C.jboolean {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_FALSE
	}
	vm.mu.Lock()
	p := vm.pending
	vm.mu.Unlock()
	if p != 0 {
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
	o := vm.newObjectLocked(cls)
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
