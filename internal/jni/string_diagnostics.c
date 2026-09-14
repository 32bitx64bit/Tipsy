/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#include "string_diagnostics.h"

#include <stdatomic.h>
#include <string.h>

enum string_metric {
	STRING_METRIC_CALLS = 0,
	STRING_METRIC_SUCCEEDED,
	STRING_METRIC_INPUT_UTF8_BYTES,
	STRING_METRIC_OUTPUT_UTF8_BYTES,
	STRING_METRIC_OUTPUT_UTF16_BYTES,
	STRING_METRIC_C_ALLOCATED_BYTES,
	STRING_METRIC_COPIED_BYTES,
	STRING_METRIC_STRING_OBJECTS,
	STRING_METRIC_DURATION_SAMPLES,
	STRING_METRIC_SAMPLED_DURATION_NS,
	STRING_METRIC_MAX_SAMPLED_DURATION_NS,
	STRING_METRICS
};

static _Atomic int g_string_diag_enabled;
static _Atomic uint64_t g_string_metrics[TIPSY_JNI_STRING_PATHS][STRING_METRICS];
static _Atomic uint64_t g_string_duration_buckets[TIPSY_JNI_STRING_PATHS][TIPSY_JNI_STRING_DURATION_BUCKETS];
static _Thread_local int tls_string_path = -1;

static int valid_path(int path)
{
	return path >= 0 && path < TIPSY_JNI_STRING_PATHS;
}

static int duration_bucket(uint64_t elapsed_ns)
{
	static const uint64_t upper[TIPSY_JNI_STRING_DURATION_BUCKETS - 1] = {
		250, 500, 1000, 2000, 5000, 10000, 50000
	};
	int i;

	for (i = 0; i < TIPSY_JNI_STRING_DURATION_BUCKETS - 1; i++) {
		if (elapsed_ns <= upper[i]) {
			return i;
		}
	}
	return TIPSY_JNI_STRING_DURATION_BUCKETS - 1;
}

static void update_max(_Atomic uint64_t *slot, uint64_t value)
{
	uint64_t previous = atomic_load_explicit(slot, memory_order_relaxed);

	while (previous < value && !atomic_compare_exchange_weak_explicit(slot,
		&previous, value, memory_order_relaxed, memory_order_relaxed)) {
	}
}

void tipsy_jni_string_diag_set_enabled(int enabled)
{
	atomic_store_explicit(&g_string_diag_enabled, enabled != 0, memory_order_relaxed);
	if (!enabled) {
		tls_string_path = -1;
	}
}

int tipsy_jni_string_diag_enabled(void)
{
	return atomic_load_explicit(&g_string_diag_enabled, memory_order_relaxed);
}

void tipsy_jni_string_diag_enter(int path)
{
	if (tipsy_jni_string_diag_enabled() && valid_path(path)) {
		tls_string_path = path;
	}
}

int tipsy_jni_string_diag_current_path(void)
{
	return tls_string_path;
}

void tipsy_jni_string_diag_leave(void)
{
	tls_string_path = -1;
}

int tipsy_jni_string_diag_record_call(int path)
{
	uint64_t calls;

	if (!tipsy_jni_string_diag_enabled() || !valid_path(path)) {
		return 0;
	}
	calls = atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_CALLS], 1,
		memory_order_relaxed) + 1;
	// Bounded deterministic 1/64 sampling keeps the enabled observer from
	// taking a clock on every high-frequency JNI string call.
	return (calls & 63) == 0;
}

void tipsy_jni_string_diag_record_unsampled_call(int path)
{
	if (!tipsy_jni_string_diag_enabled() || !valid_path(path)) {
		return;
	}
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_CALLS], 1,
		memory_order_relaxed);
}

void tipsy_jni_string_diag_record_sample(int path, uint64_t elapsed_ns)
{
	if (!tipsy_jni_string_diag_enabled() || !valid_path(path)) {
		return;
	}
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_DURATION_SAMPLES], 1,
		memory_order_relaxed);
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_SAMPLED_DURATION_NS], elapsed_ns,
		memory_order_relaxed);
	update_max(&g_string_metrics[path][STRING_METRIC_MAX_SAMPLED_DURATION_NS], elapsed_ns);
	atomic_fetch_add_explicit(&g_string_duration_buckets[path][duration_bucket(elapsed_ns)], 1,
		memory_order_relaxed);
}

void tipsy_jni_string_diag_add(int path, uint64_t succeeded,
	uint64_t input_utf8_bytes, uint64_t output_utf8_bytes,
	uint64_t output_utf16_bytes, uint64_t c_allocated_bytes,
	uint64_t copied_bytes, uint64_t string_objects)
{
	if (!tipsy_jni_string_diag_enabled() || !valid_path(path)) {
		return;
	}
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_SUCCEEDED], succeeded,
		memory_order_relaxed);
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_INPUT_UTF8_BYTES], input_utf8_bytes,
		memory_order_relaxed);
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_OUTPUT_UTF8_BYTES], output_utf8_bytes,
		memory_order_relaxed);
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_OUTPUT_UTF16_BYTES], output_utf16_bytes,
		memory_order_relaxed);
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_C_ALLOCATED_BYTES], c_allocated_bytes,
		memory_order_relaxed);
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_COPIED_BYTES], copied_bytes,
		memory_order_relaxed);
	atomic_fetch_add_explicit(&g_string_metrics[path][STRING_METRIC_STRING_OBJECTS], string_objects,
		memory_order_relaxed);
}

static uint64_t snapshot_metric(_Atomic uint64_t *slot, int reset)
{
	if (reset) {
		return atomic_exchange_explicit(slot, 0, memory_order_relaxed);
	}
	return atomic_load_explicit(slot, memory_order_relaxed);
}

void tipsy_jni_string_diag_snapshot(TipsyJNIStringDiagnostics *out, int reset)
{
	int path;
	int bucket;

	if (out == NULL) {
		return;
	}
	memset(out, 0, sizeof(*out));
	for (path = 0; path < TIPSY_JNI_STRING_PATHS; path++) {
		TipsyJNIStringPathStats *dst = &out->paths[path];
		dst->calls = snapshot_metric(&g_string_metrics[path][STRING_METRIC_CALLS], reset);
		dst->succeeded = snapshot_metric(&g_string_metrics[path][STRING_METRIC_SUCCEEDED], reset);
		dst->input_utf8_bytes = snapshot_metric(&g_string_metrics[path][STRING_METRIC_INPUT_UTF8_BYTES], reset);
		dst->output_utf8_bytes = snapshot_metric(&g_string_metrics[path][STRING_METRIC_OUTPUT_UTF8_BYTES], reset);
		dst->output_utf16_bytes = snapshot_metric(&g_string_metrics[path][STRING_METRIC_OUTPUT_UTF16_BYTES], reset);
		dst->c_allocated_bytes = snapshot_metric(&g_string_metrics[path][STRING_METRIC_C_ALLOCATED_BYTES], reset);
		dst->copied_bytes = snapshot_metric(&g_string_metrics[path][STRING_METRIC_COPIED_BYTES], reset);
		dst->string_objects = snapshot_metric(&g_string_metrics[path][STRING_METRIC_STRING_OBJECTS], reset);
		dst->duration_samples = snapshot_metric(&g_string_metrics[path][STRING_METRIC_DURATION_SAMPLES], reset);
		dst->sampled_duration_ns = snapshot_metric(&g_string_metrics[path][STRING_METRIC_SAMPLED_DURATION_NS], reset);
		dst->max_sampled_duration_ns = snapshot_metric(&g_string_metrics[path][STRING_METRIC_MAX_SAMPLED_DURATION_NS], reset);
		for (bucket = 0; bucket < TIPSY_JNI_STRING_DURATION_BUCKETS; bucket++) {
			dst->duration_buckets[bucket] = snapshot_metric(&g_string_duration_buckets[path][bucket], reset);
		}
	}
}
