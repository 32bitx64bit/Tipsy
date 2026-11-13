// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package native

/*
#cgo CFLAGS: -DTIPSY_JNI_BRIDGE_STANDALONE
#cgo linux LDFLAGS: -Wl,-z,relro,-z,now
#cgo LDFLAGS: -pthread

#include "jni_bridge.h"
#include "call.h"
#include <signal.h>
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>

static size_t tipsy_native_table_size(void)
{
	return sizeof(struct JNINativeInterface_);
}

static size_t tipsy_invoke_table_size(void)
{
	return sizeof(struct JNIInvokeInterface_);
}

static size_t tipsy_javavm_size(void)
{
	return sizeof(JavaVM);
}

static uintptr_t tipsy_read_ptr_slot(uintptr_t base, size_t idx)
{
	const uintptr_t *slots = (const uintptr_t *)base;
	return slots[idx];
}

static const void *tipsy_invoke_interface_ptr(void)
{
	JavaVM *vm = tipsy_jni_java_vm();
	if (vm == NULL) {
		return NULL;
	}
	return (const void *)vm->functions;
}

static const void *tipsy_env_functions_ptr(JNIEnv *env)
{
	if (env == NULL) {
		return NULL;
	}
	return (const void *)env->functions;
}

static jint tipsy_env_get_version(JNIEnv *env)
{
	if (env == NULL || env->functions == NULL || env->functions->GetVersion == NULL) {
		return JNI_ERR;
	}
	return env->functions->GetVersion(env);
}

static void tipsy_crash_write_native_table(void)
{
	volatile uintptr_t *slot;
	void *tbl = tipsy_jni_native_interface();
	signal(SIGSEGV, SIG_DFL);
	signal(SIGBUS, SIG_DFL);
	slot = (volatile uintptr_t *)((char *)tbl + offsetof(struct JNINativeInterface_, FindClass));
	*slot = 0xdeadbeefULL;
}

static void tipsy_crash_write_invoke_table(void)
{
	volatile uintptr_t *slot;
	JavaVM *vm = tipsy_jni_java_vm();
	signal(SIGSEGV, SIG_DFL);
	signal(SIGBUS, SIG_DFL);
	slot = (volatile uintptr_t *)((const char *)vm->functions + offsetof(struct JNIInvokeInterface_, GetEnv));
	*slot = 0xdeadbeefULL;
}
*/
import "C"

import "unsafe"

func initBridge() int {
	return int(C.tipsy_jni_init())
}

func nativeTableAddr() uintptr {
	return uintptr(C.tipsy_jni_native_interface())
}

func invokeTableAddr() uintptr {
	return uintptr(C.tipsy_invoke_interface_ptr())
}

func javaVMAddr() uintptr {
	return uintptr(unsafe.Pointer(C.tipsy_jni_java_vm()))
}

func nativeTableSize() uintptr {
	return uintptr(C.tipsy_native_table_size())
}

func invokeTableSize() uintptr {
	return uintptr(C.tipsy_invoke_table_size())
}

func javaVMSize() uintptr {
	return uintptr(C.tipsy_javavm_size())
}

func readPointerSlots(base uintptr, byteSize uintptr) []uintptr {
	count := int(byteSize / unsafe.Sizeof(uintptr(0)))
	out := make([]uintptr, count)
	for i := 0; i < count; i++ {
		out[i] = uintptr(C.tipsy_read_ptr_slot(C.uintptr_t(base), C.size_t(i)))
	}
	return out
}

func exerciseWrappedNativeMain() (beforeFn, wrappedFn, restoredFn uintptr, version int32, calls uint64, ownerOK bool) {
	C.tipsy_start_native_main()
	env := C.tipsy_jni_attach_native_main()
	if env == nil {
		return 0, 0, 0, -1, 0, false
	}
	beforeFn = uintptr(C.tipsy_env_functions_ptr(env))
	version = int32(C.tipsy_env_get_version(env))
	if C.tipsy_jni_test_wrap_native_main(1) != C.JNI_OK {
		return beforeFn, 0, 0, version, 0, false
	}
	wrappedFn = uintptr(C.tipsy_env_functions_ptr(env))
	cName := C.CString("java/lang/Object")
	defer C.free(unsafe.Pointer(cName))
	_ = C.tipsy_jni_FindClass(env, cName)

	var cOwnerOK C.int
	calls = uint64(C.tipsy_jni_test_wrapped_find_calls(&cOwnerOK))
	ownerOK = cOwnerOK != 0

	if C.tipsy_jni_test_wrap_native_main(0) != C.JNI_OK {
		return beforeFn, wrappedFn, 0, version, calls, ownerOK
	}
	restoredFn = uintptr(C.tipsy_env_functions_ptr(env))
	return beforeFn, wrappedFn, restoredFn, version, calls, ownerOK
}

func crashWriteNativeTable() {
	C.tipsy_crash_write_native_table()
}

func crashWriteInvokeTable() {
	C.tipsy_crash_write_invoke_table()
}
