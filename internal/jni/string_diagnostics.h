/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_JNI_STRING_DIAGNOSTICS_H
#define TIPSY_JNI_STRING_DIAGNOSTICS_H

#include <stdint.h>

enum {
	TIPSY_JNI_STRING_GET_CHARS = 0,
	TIPSY_JNI_STRING_GET_UTF_CHARS = 1,
	TIPSY_JNI_STRING_GET_CRITICAL = 2,
	TIPSY_JNI_STRING_RELEASE_CHARS = 3,
	TIPSY_JNI_STRING_RELEASE_UTF_CHARS = 4,
	TIPSY_JNI_STRING_RELEASE_CRITICAL = 5,
	TIPSY_JNI_STRING_NEW_UTF = 6,
	TIPSY_JNI_STRING_IS_INSTANCE_OF = 7,
	TIPSY_JNI_STRING_FIELD_GETTER_STRING = 8,
	TIPSY_JNI_STRING_PATHS = 9,
	TIPSY_JNI_STRING_DURATION_BUCKETS = 8
};

typedef struct TipsyJNIStringPathStats {
	uint64_t calls;
	uint64_t succeeded;
	uint64_t input_utf8_bytes;
	uint64_t output_utf8_bytes;
	uint64_t output_utf16_bytes;
	uint64_t c_allocated_bytes;
	uint64_t copied_bytes;
	uint64_t string_objects;
	uint64_t duration_samples;
	uint64_t sampled_duration_ns;
	uint64_t max_sampled_duration_ns;
	uint64_t duration_buckets[TIPSY_JNI_STRING_DURATION_BUCKETS];
} TipsyJNIStringPathStats;

typedef struct TipsyJNIStringDiagnostics {
	TipsyJNIStringPathStats paths[TIPSY_JNI_STRING_PATHS];
} TipsyJNIStringDiagnostics;

void tipsy_jni_string_diag_set_enabled(int enabled);
int tipsy_jni_string_diag_enabled(void);
void tipsy_jni_string_diag_enter(int path);
int tipsy_jni_string_diag_current_path(void);
void tipsy_jni_string_diag_leave(void);
int tipsy_jni_string_diag_record_call(int path);
void tipsy_jni_string_diag_record_unsampled_call(int path);
void tipsy_jni_string_diag_record_sample(int path, uint64_t elapsed_ns);
void tipsy_jni_string_diag_add(int path, uint64_t succeeded,
	uint64_t input_utf8_bytes, uint64_t output_utf8_bytes,
	uint64_t output_utf16_bytes, uint64_t c_allocated_bytes,
	uint64_t copied_bytes, uint64_t string_objects);
void tipsy_jni_string_diag_snapshot(TipsyJNIStringDiagnostics *out, int reset);

#endif
