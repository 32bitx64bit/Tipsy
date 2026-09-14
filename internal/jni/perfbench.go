// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64 && tipsy_perfbench

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#cgo CFLAGS: -DTIPSY_PERFBENCH
#include "jni_bridge.h"
#include "perfbench_native.h"
#include <stdlib.h>
*/
import "C"

import (
	"unicode/utf16"
	"unsafe"
)

const (
	nativePerfExceptionClear    = int(C.TIPSY_JNI_PERF_EXCEPTION_CLEAR)
	nativePerfExceptionPending  = int(C.TIPSY_JNI_PERF_EXCEPTION_PENDING)
	nativePerfLocalRefPair      = int(C.TIPSY_JNI_PERF_LOCAL_REF_PAIR)
	nativePerfDispatchCoreHit   = int(C.TIPSY_JNI_PERF_DISPATCH_CORE_HIT)
	nativePerfIsSameObject      = int(C.TIPSY_JNI_PERF_IS_SAME_OBJECT)
	nativePerfGetVersion        = int(C.TIPSY_JNI_PERF_GET_VERSION)
	nativePerfFieldGetterString = int(C.TIPSY_JNI_PERF_FIELD_GETTER_STRING)
	nativePerfStringChars       = int(C.TIPSY_JNI_PERF_STRING_CHARS)
	nativePerfStringCharsKnown  = int(C.TIPSY_JNI_PERF_STRING_CHARS_KNOWN_LENGTH)
	nativePerfNewStringUTF      = int(C.TIPSY_JNI_PERF_NEW_STRING_UTF_DELETE)
	nativePerfIsInstanceOf      = int(C.TIPSY_JNI_PERF_IS_INSTANCE_OF)
	nativePerfOK                = int(C.TIPSY_JNI_PERF_OK)
	nativePerfBadConfig         = int(C.TIPSY_JNI_PERF_BAD_CONFIG)
)

type nativePerfConfig struct {
	iterations       uint64
	expectedChecksum uint64
	objectA          uintptr
	objectB          uintptr
	dispatchClass    uintptr
	dispatchMethod   uintptr
	stringUTF        string
	expected         int64
	kind             int
}

type nativePerfResult struct {
	elapsedNS  uint64
	operations uint64
	checksum   uint64
	lastValue  uintptr
	status     int
	attachRC   int
	detachRC   int
}

func nativePerfMakeObject(env *Env) (uintptr, int) {
	if env == nil || env.raw == nil {
		return 0, -1
	}
	var ref C.uintptr_t
	rc := C.tipsy_jni_perf_make_object((*C.JNIEnv)(env.raw), &ref)
	return uintptr(ref), int(rc)
}

func nativePerfMakeClass(env *Env, name string) (uintptr, int) {
	if env == nil || env.raw == nil {
		return 0, -1
	}
	nameC := C.CString(name)
	defer C.free(unsafe.Pointer(nameC))
	var ref C.uintptr_t
	rc := C.tipsy_jni_perf_make_class((*C.JNIEnv)(env.raw), nameC, &ref)
	return uintptr(ref), int(rc)
}

func nativePerfMakeFieldObject(env *Env, className string) (uintptr, int) {
	if env == nil || env.raw == nil {
		return 0, -1
	}
	classC := C.CString(className)
	defer C.free(unsafe.Pointer(classC))
	var ref C.uintptr_t
	rc := C.tipsy_jni_perf_make_field_object((*C.JNIEnv)(env.raw), classC, &ref)
	return uintptr(ref), int(rc)
}

func nativePerfMakeDispatch(env *Env) (uintptr, uintptr, int) {
	if env == nil || env.raw == nil {
		return 0, 0, -1
	}
	var clazz, method C.uintptr_t
	rc := C.tipsy_jni_perf_make_dispatch((*C.JNIEnv)(env.raw), &clazz, &method)
	return uintptr(clazz), uintptr(method), int(rc)
}

func nativePerfMakeFieldMethod(env *Env, className string) (uintptr, int) {
	if env == nil || env.raw == nil {
		return 0, -1
	}
	classC := C.CString(className)
	defer C.free(unsafe.Pointer(classC))
	var method C.uintptr_t
	rc := C.tipsy_jni_perf_make_field_method((*C.JNIEnv)(env.raw), classC, &method)
	return uintptr(method), int(rc)
}

func nativePerfMakeString(env *Env, value string) (uintptr, int) {
	if env == nil || env.raw == nil {
		return 0, nativePerfBadConfig
	}
	valueC, ok := nativePerfMUTF8CString(value)
	if !ok {
		return 0, nativePerfBadConfig
	}
	defer C.free(unsafe.Pointer(valueC))
	var ref C.uintptr_t
	rc := C.tipsy_jni_perf_make_string((*C.JNIEnv)(env.raw), valueC, &ref)
	return uintptr(ref), int(rc)
}

// nativePerfModifiedUTF8 encodes the Go test fixture as the Modified UTF-8
// byte sequence that a real JNIEnv.NewStringUTF expects.  Its C caller still
// needs a terminating zero, but that zero is not part of the Java String:
// embedded Go NULs are encoded as C0 80 in the payload.
func nativePerfModifiedUTF8(value string) ([]byte, bool) {
	units := utf16.Encode([]rune(value))
	length, ok := modifiedUTF8Length(units)
	if !ok || length == int(^uint(0)>>1) {
		return nil, false
	}
	payload := make([]byte, length)
	encodeModifiedUTF8To(payload, units)
	return payload, true
}

func nativePerfMUTF8CString(value string) (*C.char, bool) {
	payload, ok := nativePerfModifiedUTF8(value)
	if !ok {
		return nil, false
	}
	terminated := make([]byte, len(payload)+1)
	copy(terminated, payload)
	return (*C.char)(C.CBytes(terminated)), true
}

func nativePerfMethodCached(method uintptr) bool {
	info, ok := lookupMethod(C.jmethodID(unsafe.Pointer(method)))
	return ok && info.loadHandler() != nil
}

func nativePerfSetStringField(vm *VM, obj uintptr, value string) bool {
	if vm == nil || obj == 0 {
		return false
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	o := vm.objects[jobjectToID(obj)]
	if o == nil {
		return false
	}
	if o.fields == nil {
		o.fields = make(map[string]any)
	}
	o.fields["getUsername"] = value
	return true
}

func nativePerfDeleteGlobal(env *Env, ref uintptr) {
	if env == nil || env.raw == nil || ref == 0 {
		return
	}
	C.tipsy_jni_perf_delete_global((*C.JNIEnv)(env.raw), C.uintptr_t(ref))
}

func nativePerfRun(vm *VM, cfg nativePerfConfig) nativePerfResult {
	if vm == nil || vm.javaVM == nil {
		return nativePerfResult{status: nativePerfBadConfig}
	}
	var stringUTF *C.char
	if cfg.stringUTF != "" || cfg.kind == nativePerfNewStringUTF {
		var ok bool
		stringUTF, ok = nativePerfMUTF8CString(cfg.stringUTF)
		if !ok {
			return nativePerfResult{status: nativePerfBadConfig}
		}
		defer C.free(unsafe.Pointer(stringUTF))
	}
	cfgC := C.struct_tipsy_jni_perf_config{
		iterations:        C.uint64_t(cfg.iterations),
		expected_checksum: C.uint64_t(cfg.expectedChecksum),
		object_a:          C.uintptr_t(cfg.objectA),
		object_b:          C.uintptr_t(cfg.objectB),
		dispatch_class:    C.uintptr_t(cfg.dispatchClass),
		dispatch_method:   C.uintptr_t(cfg.dispatchMethod),
		string_utf:        stringUTF,
		expected:          C.int64_t(cfg.expected),
		kind:              C.int(cfg.kind),
	}
	var out C.struct_tipsy_jni_perf_result
	C.tipsy_jni_perf_run((*C.JavaVM)(vm.javaVM), &cfgC, &out)
	return nativePerfResult{
		elapsedNS:  uint64(out.elapsed_ns),
		operations: uint64(out.operations),
		checksum:   uint64(out.checksum),
		lastValue:  uintptr(out.last_value),
		status:     int(out.status),
		attachRC:   int(out.attach_rc),
		detachRC:   int(out.detach_rc),
	}
}

func nativePerfCheckThreadExceptionIsolation(vm *VM) nativePerfResult {
	if vm == nil || vm.javaVM == nil {
		return nativePerfResult{status: -1}
	}
	var out C.struct_tipsy_jni_perf_result
	C.tipsy_jni_perf_check_thread_exception_isolation((*C.JavaVM)(unsafe.Pointer(vm.javaVM)), &out)
	return nativePerfResult{
		elapsedNS:  uint64(out.elapsed_ns),
		operations: uint64(out.operations),
		checksum:   uint64(out.checksum),
		lastValue:  uintptr(out.last_value),
		status:     int(out.status),
		attachRC:   int(out.attach_rc),
		detachRC:   int(out.detach_rc),
	}
}
