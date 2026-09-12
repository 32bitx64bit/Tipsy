/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_JNI_WEBRTC_AUDIO_MANAGER_H
#define TIPSY_JNI_WEBRTC_AUDIO_MANAGER_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Typed caller for the native WebRTC registers on
 * org/webrtc/voiceengine/WebRtcAudioManager:
 *   nativeCacheAudioParameters(IIIZZZZZZZIIJ)V
 * (upstream modules/audio_device/android/audio_manager.cc,
 * AudioManager::CacheAudioParameters). Fifteen arguments: the first six ride
 * in SysV integer registers, the rest on the stack, so the exact JNI
 * prototype is spelled out here instead of a generic pointer-width caller. */
void tipsy_webrtc_cache_audio_parameters(uintptr_t fn, uintptr_t env, uintptr_t thiz,
	int32_t sample_rate, int32_t output_channels, int32_t input_channels,
	uint8_t hardware_aec, uint8_t hardware_agc, uint8_t hardware_ns,
	uint8_t low_latency_output, uint8_t low_latency_input,
	uint8_t pro_audio, uint8_t a_audio,
	int32_t output_buffer_size, int32_t input_buffer_size,
	int64_t native_audio_manager);

/* Test-only witness with the exact JNI prototype; records every argument. */
uintptr_t tipsy_webrtc_record_cache_fn(void);
void tipsy_webrtc_rec_reset(void);
int tipsy_webrtc_rec_count(void);
uintptr_t tipsy_webrtc_rec_env(void);
uintptr_t tipsy_webrtc_rec_thiz(void);
int32_t tipsy_webrtc_rec_int(int i);
int tipsy_webrtc_rec_bool(int i);
int64_t tipsy_webrtc_rec_native(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_JNI_WEBRTC_AUDIO_MANAGER_H */
