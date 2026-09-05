/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_JNI_STUTTER_DIAG_H
#define TIPSY_JNI_STUTTER_DIAG_H

#include <stdint.h>

enum {
	TIPSY_JNI_THREAD_RBX_WORKER = 0,
	TIPSY_JNI_THREAD_MAIN = 1,
	TIPSY_JNI_THREAD_OTHER = 2,
	TIPSY_JNI_THREAD_CLASSES = 3
};

enum {
	TIPSY_JNI_CALL_INSTANCE = 0,
	TIPSY_JNI_CALL_STATIC = 1,
	TIPSY_JNI_CALL_NONVIRTUAL = 2,
	TIPSY_JNI_CALL_FAMILIES = 3
};

typedef struct TipsyJNIStutterStats {
	uint64_t calls[TIPSY_JNI_THREAD_CLASSES][TIPSY_JNI_CALL_FAMILIES];
} TipsyJNIStutterStats;

void tipsy_jni_stutter_diag_set_enabled(int enabled);
int tipsy_jni_stutter_diag_enabled(void);
void tipsy_jni_stutter_diag_record(int family);
void tipsy_jni_stutter_diag_snapshot(TipsyJNIStutterStats *out, int reset);

int tipsy_jni_test_classify_thread_name(const char *name);
uint64_t tipsy_jni_test_thread_name_lookups(void);
void tipsy_jni_test_reset_tls(void);
int tipsy_jni_test_record_on_named_thread(const char *name, int family, uint64_t calls);

#endif
