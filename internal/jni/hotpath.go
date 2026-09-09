// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import "unsafe"

func testStringUTF16Len(envRaw unsafe.Pointer, strID int64) int {
	return int(GoJNI_GetStringLength((*C.JNIEnv)(envRaw), jstringOf(idToJobject(strID))))
}

func testGetStringChars(envRaw unsafe.Pointer, strID int64) (ptr unsafe.Pointer, isCopy bool) {
	var copyFlag C.jboolean
	p := GoJNI_GetStringChars((*C.JNIEnv)(envRaw), jstringOf(idToJobject(strID)), &copyFlag)
	return unsafe.Pointer(p), copyFlag != 0
}

func testReleaseStringChars(envRaw unsafe.Pointer, strID int64, chars unsafe.Pointer) {
	GoJNI_ReleaseStringChars((*C.JNIEnv)(envRaw), jstringOf(idToJobject(strID)), (*C.jchar)(chars))
}

func testPrimitiveArrayCritical(envRaw unsafe.Pointer, arrID int64) (ptr unsafe.Pointer, isCopy bool) {
	var copyFlag C.jboolean
	p := GoJNI_GetPrimitiveArrayCritical((*C.JNIEnv)(envRaw), jarrayOf(idToJobject(arrID)), &copyFlag)
	return p, copyFlag != 0
}

func testReleasePrimitiveArrayCritical(envRaw unsafe.Pointer, arrID int64, ptr unsafe.Pointer, mode int) {
	GoJNI_ReleasePrimitiveArrayCritical((*C.JNIEnv)(envRaw), jarrayOf(idToJobject(arrID)), ptr, C.jint(mode))
}

func testGetArrayElements(envRaw unsafe.Pointer, arrID int64) (ptr unsafe.Pointer, isCopy bool) {
	var copyFlag C.jboolean
	p := GoJNI_GetArrayElements((*C.JNIEnv)(envRaw), jarrayOf(idToJobject(arrID)), &copyFlag, 'B')
	return p, copyFlag != 0
}

func testReleaseArrayElements(envRaw unsafe.Pointer, arrID int64, ptr unsafe.Pointer, mode int) {
	GoJNI_ReleaseArrayElements((*C.JNIEnv)(envRaw), jarrayOf(idToJobject(arrID)), ptr, C.jint(mode), 'B')
}

func testExceptionCheck(envRaw unsafe.Pointer) bool {
	return GoJNI_ExceptionCheck((*C.JNIEnv)(envRaw)) != C.JNI_FALSE
}

func testThrowID(envRaw unsafe.Pointer, id int64) int32 {
	return int32(GoJNI_Throw((*C.JNIEnv)(envRaw), jthrowableOf(idToJobject(id))))
}

func testExceptionClear(envRaw unsafe.Pointer) {
	GoJNI_ExceptionClear((*C.JNIEnv)(envRaw))
}

func testNewLocalRef(envRaw unsafe.Pointer, id int64) int64 {
	return jobjectToID(uintptr(GoJNI_NewLocalRef((*C.JNIEnv)(envRaw), idToJobject(id))))
}

func testDeleteLocalRef(envRaw unsafe.Pointer, id int64) {
	GoJNI_DeleteLocalRef((*C.JNIEnv)(envRaw), idToJobject(id))
}

func testPushFrame(envRaw unsafe.Pointer, capacity int) int {
	return int(GoJNI_PushLocalFrame((*C.JNIEnv)(envRaw), C.jint(capacity)))
}

func testPopFrame(envRaw unsafe.Pointer, resultID int64) int64 {
	return jobjectToID(uintptr(GoJNI_PopLocalFrame((*C.JNIEnv)(envRaw), idToJobject(resultID))))
}

func testNewStringOn(envRaw unsafe.Pointer, s string) uintptr {
	return (&Env{raw: envRaw, vm: vmFromEnv(envRaw)}).NewString(s)
}

func testNewUTF16String(vm *VM, s string) int64 {
	if vm == nil {
		return 0
	}
	vm.mu.Lock()
	o := vm.newStringOn(vm.envRaw, s)
	vm.mu.Unlock()
	return o.id
}

func testNewByteObject(vm *VM, data []byte) int64 {
	if vm == nil {
		return 0
	}
	vm.mu.Lock()
	o := vm.newObjectOn(vm.envRaw, vm.classes["java/lang/Object"])
	o.arrKind = int('B')
	o.bytes = append([]byte(nil), data...)
	vm.mu.Unlock()
	return o.id
}
