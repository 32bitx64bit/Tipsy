/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#define _GNU_SOURCE
#include "stutter_diag.h"

#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

static _Atomic int g_jni_stutter_enabled;
static _Atomic uint64_t g_jni_calls[TIPSY_JNI_THREAD_CLASSES][TIPSY_JNI_CALL_FAMILIES];
static _Atomic uint64_t g_thread_name_lookups;
static __thread int tls_thread_class = -1;

static int classify_thread_name(const char *name)
{
	if (name != NULL && strncmp(name, "RBX Worker", 10) == 0) {
		return TIPSY_JNI_THREAD_RBX_WORKER;
	}
	if (name != NULL && strcmp(name, "Main") == 0) {
		return TIPSY_JNI_THREAD_MAIN;
	}
	return TIPSY_JNI_THREAD_OTHER;
}

static int current_thread_class(void)
{
	char name[16] = {0};

	if (tls_thread_class >= 0) {
		return tls_thread_class;
	}
	atomic_fetch_add_explicit(&g_thread_name_lookups, 1, memory_order_relaxed);
	if (pthread_getname_np(pthread_self(), name, sizeof(name)) != 0) {
		tls_thread_class = TIPSY_JNI_THREAD_OTHER;
	} else {
		tls_thread_class = classify_thread_name(name);
	}
	return tls_thread_class;
}

void tipsy_jni_stutter_diag_set_enabled(int enabled)
{
	atomic_store_explicit(&g_jni_stutter_enabled, enabled != 0, memory_order_relaxed);
}

int tipsy_jni_stutter_diag_enabled(void)
{
	return atomic_load_explicit(&g_jni_stutter_enabled, memory_order_relaxed);
}

void tipsy_jni_stutter_diag_record(int family)
{
	int thread_class;

	if (!atomic_load_explicit(&g_jni_stutter_enabled, memory_order_relaxed) ||
	    family < 0 || family >= TIPSY_JNI_CALL_FAMILIES) {
		return;
	}
	thread_class = current_thread_class();
	atomic_fetch_add_explicit(&g_jni_calls[thread_class][family], 1, memory_order_relaxed);
}

void tipsy_jni_stutter_diag_snapshot(TipsyJNIStutterStats *out, int reset)
{
	int thread_class;
	int family;

	if (out == NULL) {
		return;
	}
	memset(out, 0, sizeof(*out));
	for (thread_class = 0; thread_class < TIPSY_JNI_THREAD_CLASSES; thread_class++) {
		for (family = 0; family < TIPSY_JNI_CALL_FAMILIES; family++) {
			if (reset) {
				out->calls[thread_class][family] = atomic_exchange_explicit(
					&g_jni_calls[thread_class][family], 0, memory_order_relaxed);
			} else {
				out->calls[thread_class][family] = atomic_load_explicit(
					&g_jni_calls[thread_class][family], memory_order_relaxed);
			}
		}
	}
}

int tipsy_jni_test_classify_thread_name(const char *name)
{
	return classify_thread_name(name);
}

uint64_t tipsy_jni_test_thread_name_lookups(void)
{
	return atomic_load_explicit(&g_thread_name_lookups, memory_order_relaxed);
}

void tipsy_jni_test_reset_tls(void)
{
	tls_thread_class = -1;
}

struct named_record {
	const char *name;
	int family;
	uint64_t calls;
};

static void *record_on_named_thread(void *opaque)
{
	struct named_record *record = opaque;
	uint64_t n;

	(void)pthread_setname_np(pthread_self(), record->name);
	tls_thread_class = -1;
	for (n = 0; n < record->calls; n++) {
		tipsy_jni_stutter_diag_record(record->family);
	}
	return NULL;
}

int tipsy_jni_test_record_on_named_thread(const char *name, int family, uint64_t calls)
{
	pthread_t thread;
	struct named_record record = { name, family, calls };

	if (name == NULL || strlen(name) >= 16) {
		return -1;
	}
	if (pthread_create(&thread, NULL, record_on_named_thread, &record) != 0) {
		return -2;
	}
	return pthread_join(thread, NULL);
}
