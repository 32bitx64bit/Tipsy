/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "webrtc_audio_manager.h"

#include "jni_bridge.h"

typedef void (*tipsy_webrtc_cache_fn)(JNIEnv *, jobject, jint, jint, jint,
	jboolean, jboolean, jboolean, jboolean, jboolean, jboolean, jboolean,
	jint, jint, jlong);

void tipsy_webrtc_cache_audio_parameters(uintptr_t fn, uintptr_t env, uintptr_t thiz,
	int32_t sample_rate, int32_t output_channels, int32_t input_channels,
	uint8_t hardware_aec, uint8_t hardware_agc, uint8_t hardware_ns,
	uint8_t low_latency_output, uint8_t low_latency_input,
	uint8_t pro_audio, uint8_t a_audio,
	int32_t output_buffer_size, int32_t input_buffer_size,
	int64_t native_audio_manager) {
	((tipsy_webrtc_cache_fn)fn)((JNIEnv *)env, (jobject)thiz,
		(jint)sample_rate, (jint)output_channels, (jint)input_channels,
		(jboolean)hardware_aec, (jboolean)hardware_agc, (jboolean)hardware_ns,
		(jboolean)low_latency_output, (jboolean)low_latency_input,
		(jboolean)pro_audio, (jboolean)a_audio,
		(jint)output_buffer_size, (jint)input_buffer_size,
		(jlong)native_audio_manager);
}

/* ABI witness used only by Go tests: a fake engine native with the exact
 * registered prototype. It stores what arrived in each slot so the test can
 * prove register and stack placement without loading libroblox.so. */
static int tipsy_webrtc_rec_calls;
static uintptr_t tipsy_webrtc_rec_env_v;
static uintptr_t tipsy_webrtc_rec_thiz_v;
static int32_t tipsy_webrtc_rec_ints[5];
static int tipsy_webrtc_rec_bools[7];
static int64_t tipsy_webrtc_rec_native_v;

static void tipsy_webrtc_record_cache(JNIEnv *env, jobject thiz, jint sample_rate,
	jint output_channels, jint input_channels, jboolean hardware_aec,
	jboolean hardware_agc, jboolean hardware_ns, jboolean low_latency_output,
	jboolean low_latency_input, jboolean pro_audio, jboolean a_audio,
	jint output_buffer_size, jint input_buffer_size, jlong native_audio_manager) {
	tipsy_webrtc_rec_calls++;
	tipsy_webrtc_rec_env_v = (uintptr_t)env;
	tipsy_webrtc_rec_thiz_v = (uintptr_t)thiz;
	tipsy_webrtc_rec_ints[0] = sample_rate;
	tipsy_webrtc_rec_ints[1] = output_channels;
	tipsy_webrtc_rec_ints[2] = input_channels;
	tipsy_webrtc_rec_ints[3] = output_buffer_size;
	tipsy_webrtc_rec_ints[4] = input_buffer_size;
	tipsy_webrtc_rec_bools[0] = hardware_aec;
	tipsy_webrtc_rec_bools[1] = hardware_agc;
	tipsy_webrtc_rec_bools[2] = hardware_ns;
	tipsy_webrtc_rec_bools[3] = low_latency_output;
	tipsy_webrtc_rec_bools[4] = low_latency_input;
	tipsy_webrtc_rec_bools[5] = pro_audio;
	tipsy_webrtc_rec_bools[6] = a_audio;
	tipsy_webrtc_rec_native_v = native_audio_manager;
}

uintptr_t tipsy_webrtc_record_cache_fn(void) { return (uintptr_t)tipsy_webrtc_record_cache; }
void tipsy_webrtc_rec_reset(void) {
	tipsy_webrtc_rec_calls = 0;
	tipsy_webrtc_rec_env_v = 0;
	tipsy_webrtc_rec_thiz_v = 0;
	for (int i = 0; i < 5; i++) tipsy_webrtc_rec_ints[i] = 0;
	for (int i = 0; i < 7; i++) tipsy_webrtc_rec_bools[i] = 0;
	tipsy_webrtc_rec_native_v = 0;
}
int tipsy_webrtc_rec_count(void) { return tipsy_webrtc_rec_calls; }
uintptr_t tipsy_webrtc_rec_env(void) { return tipsy_webrtc_rec_env_v; }
uintptr_t tipsy_webrtc_rec_thiz(void) { return tipsy_webrtc_rec_thiz_v; }
int32_t tipsy_webrtc_rec_int(int i) { return i >= 0 && i < 5 ? tipsy_webrtc_rec_ints[i] : 0; }
int tipsy_webrtc_rec_bool(int i) { return i >= 0 && i < 7 ? tipsy_webrtc_rec_bools[i] : 0; }
int64_t tipsy_webrtc_rec_native(void) { return tipsy_webrtc_rec_native_v; }
