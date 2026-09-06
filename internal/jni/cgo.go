// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func initCBridge() error {
	if C.tipsy_jni_init() != 0 {
		return fmt.Errorf("jni: C bridge init failed")
	}
	return nil
}

func javaVMPtr() unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_jni_java_vm())
}

func attachNativeMainEnvPtr() unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_jni_attach_native_main())
}

func currentEnvPtr() unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_jni_current_env())
}

func nativeInterfacePtr() unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_jni_native_interface())
}

func attachCurrentThreadForTest(daemon bool) (int32, unsafe.Pointer) {
	var env *C.JNIEnv
	d := C.int(0)
	if daemon {
		d = 1
	}
	rc := C.tipsy_jni_attach_current_thread(&env, d)
	return int32(rc), unsafe.Pointer(env)
}

func detachCurrentThreadForTest() int32 {
	return int32(C.tipsy_jni_detach_current_thread())
}

func getEnvForTest(version int32) (int32, unsafe.Pointer) {
	// A non-NULL sentinel proves that every failure path clears the output.
	sentinel := C.malloc(1)
	defer C.free(sentinel)
	env := sentinel
	rc := C.tipsy_jni_get_env(&env, C.jint(version))
	return int32(rc), env
}

func envFunctionsForTest(raw unsafe.Pointer) uintptr {
	if raw == nil {
		return 0
	}
	return uintptr(unsafe.Pointer((*C.JNIEnv)(raw).functions))
}

func attachedThreadCountForTest() int {
	return int(C.tipsy_jni_attached_thread_count())
}

func attachAndExitThreadForTest() int32 {
	return int32(C.tipsy_jni_test_attach_and_exit())
}

func nativeMainEnvIsForTest(raw unsafe.Pointer) bool {
	return C.tipsy_jni_test_native_main_env_is((*C.JNIEnv)(raw)) != 0
}

func throwForTest(raw unsafe.Pointer, obj uintptr) int32 {
	return int32(GoJNI_Throw((*C.JNIEnv)(raw), jthrowableOf(idToJobject(jobjectToID(obj)))))
}

func exceptionOccurredForTest(raw unsafe.Pointer) uintptr {
	return uintptr(asJobjectFromThrow(GoJNI_ExceptionOccurred((*C.JNIEnv)(raw))))
}

func exceptionClearForTest(raw unsafe.Pointer) {
	GoJNI_ExceptionClear((*C.JNIEnv)(raw))
}

func wrapNativeMainForTest(enabled bool) int32 {
	v := C.int(0)
	if enabled {
		v = 1
	}
	return int32(C.tipsy_jni_test_wrap_native_main(v))
}

func wrappedFindCallsForTest() (int, bool) {
	var ownerOK C.int
	calls := C.tipsy_jni_test_wrapped_find_calls(&ownerOK)
	return int(calls), ownerOK != 0
}

func allocMethod(class, name, sig string, static bool) C.jmethodID {
	m := (*C.TipsyMethod)(C.calloc(1, C.sizeof_struct_TipsyMethod))
	if m == nil {
		var z C.jmethodID
		return z
	}
	m.magic = C.TIPSY_METHOD_MAGIC
	m.class_name = C.CString(class)
	m.name = C.CString(name)
	m.sig = C.CString(sig)
	if static {
		m.is_static = 1
	}
	return C.jmethodID(unsafe.Pointer(m))
}

func allocField(class, name, sig string, static bool) C.jfieldID {
	f := (*C.TipsyField)(C.calloc(1, C.sizeof_struct_TipsyField))
	if f == nil {
		var z C.jfieldID
		return z
	}
	f.magic = C.TIPSY_FIELD_MAGIC
	f.class_name = C.CString(class)
	f.name = C.CString(name)
	f.sig = C.CString(sig)
	if static {
		f.is_static = 1
	}
	return C.jfieldID(unsafe.Pointer(f))
}

func parseMethod(mid C.jmethodID) (class, name, sig string, static bool, ok bool) {
	if info, hit := lookupMethod(mid); hit {
		return info.class, info.name, info.sig, info.static, true
	}
	if jmethodNil(mid) {
		return "", "", "", false, false
	}
	m := (*C.TipsyMethod)(unsafe.Pointer(mid))
	if m.magic != C.TIPSY_METHOD_MAGIC {
		return "", "", "", false, false
	}
	return C.GoString(m.class_name), C.GoString(m.name), C.GoString(m.sig), m.is_static != 0, true
}

func parseField(fid C.jfieldID) (class, name, sig string, static bool, ok bool) {
	if info, hit := lookupField(fid); hit {
		return info.class, info.name, info.sig, info.static, true
	}
	if jfieldNil(fid) {
		return "", "", "", false, false
	}
	f := (*C.TipsyField)(unsafe.Pointer(fid))
	if f.magic != C.TIPSY_FIELD_MAGIC {
		return "", "", "", false, false
	}
	return C.GoString(f.class_name), C.GoString(f.name), C.GoString(f.sig), f.is_static != 0, true
}

func jmethodNil(m C.jmethodID) bool {
	var z C.jmethodID
	return m == z
}

func jfieldNil(f C.jfieldID) bool {
	var z C.jfieldID
	return f == z
}

func idToJobject(id int64) C.jobject {
	return C.jobject(uintptr(id))
}

func jobjectToID(o uintptr) int64 {
	return int64(o)
}

func jclassOf(o C.jobject) C.jclass {
	return *(*C.jclass)(unsafe.Pointer(&o))
}

func jstringOf(o C.jobject) C.jstring {
	return *(*C.jstring)(unsafe.Pointer(&o))
}

func jthrowableOf(o C.jobject) C.jthrowable {
	return *(*C.jthrowable)(unsafe.Pointer(&o))
}

func jarrayOf(o C.jobject) C.jarray {
	return *(*C.jarray)(unsafe.Pointer(&o))
}

func jobjectArrayOf(o C.jobject) C.jobjectArray {
	return *(*C.jobjectArray)(unsafe.Pointer(&o))
}

func jweakOf(o C.jobject) C.jweak {
	return *(*C.jweak)(unsafe.Pointer(&o))
}

func asJobjectFromClass(c C.jclass) C.jobject {
	return *(*C.jobject)(unsafe.Pointer(&c))
}

func asJobjectFromString(s C.jstring) C.jobject {
	return *(*C.jobject)(unsafe.Pointer(&s))
}

func asJobjectFromArray(a C.jarray) C.jobject {
	return *(*C.jobject)(unsafe.Pointer(&a))
}

func asJobjectFromThrow(t C.jthrowable) C.jobject {
	return *(*C.jobject)(unsafe.Pointer(&t))
}

func asJobjectFromWeak(w C.jweak) C.jobject {
	return *(*C.jobject)(unsafe.Pointer(&w))
}

func jnull() C.jobject {
	var z C.jobject
	return z
}

func jclassNull() C.jclass {
	var z C.jclass
	return z
}

func jmethodNull() C.jmethodID {
	var z C.jmethodID
	return z
}

func jfieldNull() C.jfieldID {
	var z C.jfieldID
	return z
}
