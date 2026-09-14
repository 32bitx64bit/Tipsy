// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include <stdlib.h>

static const jchar *tipsy_test_diag_get_chars(JNIEnv *env, jstring value) {
	return env != NULL && env->functions != NULL && env->functions->GetStringChars != NULL
		? env->functions->GetStringChars(env, value, NULL) : NULL;
}
static const jchar *tipsy_test_diag_get_critical(JNIEnv *env, jstring value) {
	return env != NULL && env->functions != NULL && env->functions->GetStringCritical != NULL
		? env->functions->GetStringCritical(env, value, NULL) : NULL;
}
static void tipsy_test_diag_release_chars(JNIEnv *env, jstring value, const jchar *chars) {
	if (env != NULL && env->functions != NULL && env->functions->ReleaseStringChars != NULL) {
		env->functions->ReleaseStringChars(env, value, chars);
	}
}
static void tipsy_test_diag_release_critical(JNIEnv *env, jstring value, const jchar *chars) {
	if (env != NULL && env->functions != NULL && env->functions->ReleaseStringCritical != NULL) {
		env->functions->ReleaseStringCritical(env, value, chars);
	}
}
static const char *tipsy_test_diag_get_utf_chars(JNIEnv *env, jstring value) {
	return env != NULL && env->functions != NULL && env->functions->GetStringUTFChars != NULL
		? env->functions->GetStringUTFChars(env, value, NULL) : NULL;
}
static void tipsy_test_diag_release_utf_chars(JNIEnv *env, jstring value, const char *chars) {
	if (env != NULL && env->functions != NULL && env->functions->ReleaseStringUTFChars != NULL) {
		env->functions->ReleaseStringUTFChars(env, value, chars);
	}
}
static jboolean tipsy_test_diag_instance_of(JNIEnv *env, jobject obj, jclass cls) {
	return env != NULL && env->functions != NULL && env->functions->IsInstanceOf != NULL
		? env->functions->IsInstanceOf(env, obj, cls) : JNI_FALSE;
}
static jfieldID tipsy_test_diag_field_id(JNIEnv *env, jclass cls, const char *name, const char *sig) {
	return env != NULL && env->functions != NULL && env->functions->GetFieldID != NULL
		? env->functions->GetFieldID(env, cls, name, sig) : NULL;
}
static jobject tipsy_test_diag_get_object_field(JNIEnv *env, jobject obj, jclass cls, jfieldID field) {
	(void)cls;
	return env != NULL && env->functions != NULL && env->functions->GetObjectField != NULL
		? env->functions->GetObjectField(env, obj, field) : NULL;
}
*/
import "C"

import "unsafe"

func testDiagnosticGetChars(env unsafe.Pointer, str uintptr) unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_test_diag_get_chars((*C.JNIEnv)(env), C.jstring(unsafe.Pointer(str))))
}

func testDiagnosticGetCritical(env unsafe.Pointer, str uintptr) unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_test_diag_get_critical((*C.JNIEnv)(env), C.jstring(unsafe.Pointer(str))))
}

func testDiagnosticReleaseChars(env unsafe.Pointer, str uintptr, chars unsafe.Pointer) {
	C.tipsy_test_diag_release_chars((*C.JNIEnv)(env), C.jstring(unsafe.Pointer(str)), (*C.jchar)(chars))
}

func testDiagnosticReleaseCritical(env unsafe.Pointer, str uintptr, chars unsafe.Pointer) {
	C.tipsy_test_diag_release_critical((*C.JNIEnv)(env), C.jstring(unsafe.Pointer(str)), (*C.jchar)(chars))
}

func testDiagnosticGetUTFChars(env unsafe.Pointer, str uintptr) unsafe.Pointer {
	return unsafe.Pointer(C.tipsy_test_diag_get_utf_chars((*C.JNIEnv)(env), C.jstring(unsafe.Pointer(str))))
}

func testDiagnosticReleaseUTFChars(env unsafe.Pointer, str uintptr, chars unsafe.Pointer) {
	C.tipsy_test_diag_release_utf_chars((*C.JNIEnv)(env), C.jstring(unsafe.Pointer(str)), (*C.char)(chars))
}

func testDiagnosticInstanceOf(env unsafe.Pointer, obj, cls uintptr) bool {
	return C.tipsy_test_diag_instance_of((*C.JNIEnv)(env), C.jobject(unsafe.Pointer(obj)), C.jclass(unsafe.Pointer(cls))) == C.JNI_TRUE
}

func testDiagnosticFieldID(env unsafe.Pointer, cls uintptr, name, sig string) uintptr {
	nameC := C.CString(name)
	defer C.free(unsafe.Pointer(nameC))
	sigC := C.CString(sig)
	defer C.free(unsafe.Pointer(sigC))
	return uintptr(C.tipsy_test_diag_field_id((*C.JNIEnv)(env), C.jclass(unsafe.Pointer(cls)), nameC, sigC))
}

func testDiagnosticGetObjectField(env unsafe.Pointer, obj, cls, field uintptr) uintptr {
	return uintptr(C.tipsy_test_diag_get_object_field((*C.JNIEnv)(env), C.jobject(unsafe.Pointer(obj)), C.jclass(unsafe.Pointer(cls)), C.jfieldID(unsafe.Pointer(field))))
}
