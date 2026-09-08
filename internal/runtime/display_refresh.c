/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "display_refresh.h"

#include <stdint.h>
#include <stdlib.h>

typedef void (*tipsy_current_refresh_fn)(JNIEnv *, jclass, jfloat);
typedef void (*tipsy_supported_refresh_fn)(JNIEnv *, jclass, jfloatArray);

// This function is itself submitted through loader.CallP8, preserving the
// dedicated native-main pthread/stack used by every other Roblox JNI entry.
int64_t tipsy_publish_display_refresh_rates(void *current_ptr,
	void *supported_ptr, void *env_ptr, void *class_ptr, void *current_hz_ptr,
	void *rates_ptr, void *rates_len_ptr, void *unused) {
	tipsy_current_refresh_fn current_fn = (tipsy_current_refresh_fn)current_ptr;
	tipsy_supported_refresh_fn supported_fn = (tipsy_supported_refresh_fn)supported_ptr;
	JNIEnv *env = (JNIEnv *)env_ptr;
	jclass cls = (jclass)class_ptr;
	jfloat *current_hz = (jfloat *)current_hz_ptr;
	jfloat *rates = (jfloat *)rates_ptr;
	jsize rates_len = (jsize)(uintptr_t)rates_len_ptr;
	(void)unused;
	if (current_fn == NULL || supported_fn == NULL || env == NULL || cls == NULL ||
		current_hz == NULL || rates_len <= 0 || rates == NULL) {
		return -1;
	}
	current_fn(env, cls, *current_hz);
	jfloatArray array = env->functions->NewFloatArray(env, rates_len);
	if (array == NULL) {
		return -2;
	}
	env->functions->SetFloatArrayRegion(env, array, 0, rates_len, rates);
	supported_fn(env, cls, array);
	return 0;
}

uintptr_t tipsy_display_refresh_publisher_addr(void) {
	return (uintptr_t)tipsy_publish_display_refresh_rates;
}

static jfloat tipsy_test_current_hz;
static jfloat tipsy_test_supported_hz[32];
static jsize tipsy_test_supported_count;

void tipsy_test_current_refresh(JNIEnv *env, jclass cls, jfloat hz) {
	(void)env;
	(void)cls;
	tipsy_test_current_hz = hz;
}

void tipsy_test_supported_refresh(JNIEnv *env, jclass cls, jfloatArray rates) {
	(void)cls;
	tipsy_test_supported_count = env->functions->GetArrayLength(env, rates);
	if (tipsy_test_supported_count > 32) {
		tipsy_test_supported_count = 32;
	}
	if (tipsy_test_supported_count > 0) {
		env->functions->GetFloatArrayRegion(env, rates, 0,
			tipsy_test_supported_count, tipsy_test_supported_hz);
	}
}

uintptr_t tipsy_test_current_refresh_addr(void) {
	return (uintptr_t)tipsy_test_current_refresh;
}

uintptr_t tipsy_test_supported_refresh_addr(void) {
	return (uintptr_t)tipsy_test_supported_refresh;
}

void tipsy_test_reset_display_refresh(void) {
	tipsy_test_current_hz = 0;
	tipsy_test_supported_count = 0;
}

jfloat tipsy_test_recorded_current_refresh(void) {
	return tipsy_test_current_hz;
}

jsize tipsy_test_recorded_supported_count(void) {
	return tipsy_test_supported_count;
}

jfloat tipsy_test_recorded_supported_refresh(jsize index) {
	if (index < 0 || index >= tipsy_test_supported_count) {
		return 0;
	}
	return tipsy_test_supported_hz[index];
}
