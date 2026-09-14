// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include <stdlib.h>

static jobject tipsy_reference_new_global(JNIEnv *env, jobject obj) {
	return env != NULL && env->functions != NULL && env->functions->NewGlobalRef != NULL
		? env->functions->NewGlobalRef(env, obj) : NULL;
}
static void tipsy_reference_delete_global(JNIEnv *env, jobject obj) {
	if (env != NULL && env->functions != NULL && env->functions->DeleteGlobalRef != NULL) {
		env->functions->DeleteGlobalRef(env, obj);
	}
}
static jstring tipsy_reference_new_string_utf(JNIEnv *env, const char *bytes) {
	return env != NULL && env->functions != NULL && env->functions->NewStringUTF != NULL
		? env->functions->NewStringUTF(env, bytes) : NULL;
}
static jstring tipsy_reference_new_string(JNIEnv *env, const jchar *chars, jsize length) {
	return env != NULL && env->functions != NULL && env->functions->NewString != NULL
		? env->functions->NewString(env, chars, length) : NULL;
}
static jsize tipsy_reference_get_string_length(JNIEnv *env, jstring value) {
	return env != NULL && env->functions != NULL && env->functions->GetStringLength != NULL
		? env->functions->GetStringLength(env, value) : 0;
}
static jsize tipsy_reference_get_string_utf_length(JNIEnv *env, jstring value) {
	return env != NULL && env->functions != NULL && env->functions->GetStringUTFLength != NULL
		? env->functions->GetStringUTFLength(env, value) : 0;
}
static const char *tipsy_reference_get_string_utf_chars(JNIEnv *env, jstring value) {
	return env != NULL && env->functions != NULL && env->functions->GetStringUTFChars != NULL
		? env->functions->GetStringUTFChars(env, value, NULL) : NULL;
}
static void tipsy_reference_release_string_utf_chars(JNIEnv *env, jstring value, const char *chars) {
	if (env != NULL && env->functions != NULL && env->functions->ReleaseStringUTFChars != NULL) {
		env->functions->ReleaseStringUTFChars(env, value, chars);
	}
}
static void tipsy_reference_get_string_region(JNIEnv *env, jstring value, jsize start, jsize length, jchar *buf) {
	if (env != NULL && env->functions != NULL && env->functions->GetStringRegion != NULL) {
		env->functions->GetStringRegion(env, value, start, length, buf);
	}
}
static void tipsy_reference_get_string_utf_region(JNIEnv *env, jstring value, jsize start, jsize length, char *buf) {
	if (env != NULL && env->functions != NULL && env->functions->GetStringUTFRegion != NULL) {
		env->functions->GetStringUTFRegion(env, value, start, length, buf);
	}
}
*/
import "C"

import "unsafe"

// These helpers are test fixtures for the native JNIEnv vtable. They are not
// called on launch paths; keeping the C call boundary here means the tests do
// not accidentally exercise exported Go functions directly.
func referenceVtableNewGlobal(envRaw unsafe.Pointer, id int64) int64 {
	return jobjectToID(uintptr(C.tipsy_reference_new_global((*C.JNIEnv)(envRaw), idToJobject(id))))
}

func referenceVtableDeleteGlobal(envRaw unsafe.Pointer, id int64) {
	C.tipsy_reference_delete_global((*C.JNIEnv)(envRaw), idToJobject(id))
}

func referenceVtableNewModifiedUTF8(envRaw unsafe.Pointer, payload []byte) int64 {
	bytes := make([]byte, len(payload)+1)
	copy(bytes, payload)
	p := C.CBytes(bytes)
	defer C.free(p)
	return jobjectToID(uintptr(C.tipsy_reference_new_string_utf((*C.JNIEnv)(envRaw), (*C.char)(p))))
}

func referenceVtableNewModifiedUTF8Nil(envRaw unsafe.Pointer) int64 {
	return jobjectToID(uintptr(C.tipsy_reference_new_string_utf((*C.JNIEnv)(envRaw), nil)))
}

func referenceVtableNewUTF16(envRaw unsafe.Pointer, units []uint16) int64 {
	if len(units) == 0 {
		return jobjectToID(uintptr(C.tipsy_reference_new_string((*C.JNIEnv)(envRaw), nil, 0)))
	}
	p := C.malloc(C.size_t(len(units)) * C.size_t(unsafe.Sizeof(C.jchar(0))))
	if p == nil {
		return 0
	}
	defer C.free(p)
	copy(unsafe.Slice((*uint16)(p), len(units)), units)
	return jobjectToID(uintptr(C.tipsy_reference_new_string((*C.JNIEnv)(envRaw), (*C.jchar)(p), C.jsize(len(units)))))
}

func referenceVtableNewUTF16Nil(envRaw unsafe.Pointer, length int) int64 {
	return jobjectToID(uintptr(C.tipsy_reference_new_string((*C.JNIEnv)(envRaw), nil, C.jsize(length))))
}

func referenceVtableStringLength(envRaw unsafe.Pointer, id int64) int {
	return int(C.tipsy_reference_get_string_length((*C.JNIEnv)(envRaw), jstringOf(idToJobject(id))))
}

func referenceVtableModifiedUTF8(envRaw unsafe.Pointer, id int64) []byte {
	value := jstringOf(idToJobject(id))
	n := int(C.tipsy_reference_get_string_utf_length((*C.JNIEnv)(envRaw), value))
	p := C.tipsy_reference_get_string_utf_chars((*C.JNIEnv)(envRaw), value)
	if p == nil {
		return nil
	}
	defer C.tipsy_reference_release_string_utf_chars((*C.JNIEnv)(envRaw), value, p)
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(p)), n)...)
}

func referenceVtableUTF16Region(envRaw unsafe.Pointer, id int64, start, length int, initial []uint16) []uint16 {
	p := C.malloc(C.size_t(len(initial)) * C.size_t(unsafe.Sizeof(C.jchar(0))))
	if p == nil {
		return nil
	}
	defer C.free(p)
	dst := unsafe.Slice((*uint16)(p), len(initial))
	copy(dst, initial)
	C.tipsy_reference_get_string_region((*C.JNIEnv)(envRaw), jstringOf(idToJobject(id)), C.jsize(start), C.jsize(length), (*C.jchar)(p))
	return append([]uint16(nil), dst...)
}

func referenceVtableModifiedUTF8Region(envRaw unsafe.Pointer, id int64, start, length, capacity int, fill byte) []byte {
	p := C.malloc(C.size_t(capacity))
	if p == nil {
		return nil
	}
	defer C.free(p)
	dst := unsafe.Slice((*byte)(p), capacity)
	for i := range dst {
		dst[i] = fill
	}
	C.tipsy_reference_get_string_utf_region((*C.JNIEnv)(envRaw), jstringOf(idToJobject(id)), C.jsize(start), C.jsize(length), (*C.char)(p))
	return append([]byte(nil), dst...)
}
