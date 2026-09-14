/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Native-JNIEnv benchmark support.  The tagged Go wrapper is deliberately
 * the only package entry point: normal Tipsy builds do not expose this test
 * harness.
 */
#ifndef TIPSY_JNI_PERFBENCH_NATIVE_H
#define TIPSY_JNI_PERFBENCH_NATIVE_H

#include "jni_bridge.h"

#include <stdint.h>

enum tipsy_jni_perf_kind {
	TIPSY_JNI_PERF_EXCEPTION_CLEAR = 1,
	TIPSY_JNI_PERF_EXCEPTION_PENDING,
	TIPSY_JNI_PERF_LOCAL_REF_PAIR,
	TIPSY_JNI_PERF_DISPATCH_CORE_HIT,
	TIPSY_JNI_PERF_IS_SAME_OBJECT,
	TIPSY_JNI_PERF_GET_VERSION,
	TIPSY_JNI_PERF_FIELD_GETTER_STRING,
	TIPSY_JNI_PERF_STRING_CHARS,
	TIPSY_JNI_PERF_STRING_CHARS_KNOWN_LENGTH,
	TIPSY_JNI_PERF_NEW_STRING_UTF_DELETE,
	TIPSY_JNI_PERF_IS_INSTANCE_OF,
};

enum tipsy_jni_perf_status {
	TIPSY_JNI_PERF_OK = 0,
	TIPSY_JNI_PERF_BAD_CONFIG = -1,
	TIPSY_JNI_PERF_THREAD_CREATE = -2,
	TIPSY_JNI_PERF_ATTACH = -3,
	TIPSY_JNI_PERF_SETUP = -4,
	TIPSY_JNI_PERF_EXPECTED = -5,
	TIPSY_JNI_PERF_CLOCK = -6,
	TIPSY_JNI_PERF_DETACH = -7,
};

struct tipsy_jni_perf_config {
	uint64_t iterations;
	uint64_t expected_checksum;
	uintptr_t object_a;
	uintptr_t object_b;
	uintptr_t dispatch_class;
	uintptr_t dispatch_method;
	const char *string_utf;
	int64_t expected;
	int kind;
};

struct tipsy_jni_perf_result {
	uint64_t elapsed_ns;
	uint64_t operations;
	uint64_t checksum;
	uintptr_t last_value;
	int status;
	int attach_rc;
	int detach_rc;
};

/* Each setup helper uses the real JNIEnv table.  The returned handles are
 * global references and must be released with tipsy_jni_perf_delete_global.
 */
int tipsy_jni_perf_make_object(JNIEnv *env, uintptr_t *out);
int tipsy_jni_perf_make_class(JNIEnv *env, const char *name, uintptr_t *out);
int tipsy_jni_perf_make_field_object(JNIEnv *env, const char *class_name, uintptr_t *out);
int tipsy_jni_perf_make_dispatch(JNIEnv *env, uintptr_t *clazz, uintptr_t *method);
int tipsy_jni_perf_make_field_method(JNIEnv *env, const char *class_name, uintptr_t *method);
int tipsy_jni_perf_make_string(JNIEnv *env, const char *value, uintptr_t *out);
void tipsy_jni_perf_delete_global(JNIEnv *env, uintptr_t ref);

/* Run cfg->iterations real indirect JNIEnv calls on an attached native
 * pthread.  The monotonic interval covers only the C loop, not thread
 * creation, attachment, warmup, or detachment.
 */
int tipsy_jni_perf_run(JavaVM *vm, const struct tipsy_jni_perf_config *cfg,
	struct tipsy_jni_perf_result *out);

/* Proves clear and pending exception state remains independent while two
 * native threads are attached at the same time. */
int tipsy_jni_perf_check_thread_exception_isolation(JavaVM *vm,
	struct tipsy_jni_perf_result *out);

#endif
