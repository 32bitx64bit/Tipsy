/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Minimal Android OpenSL ES buffer-queue bridge for the official client.
 * Client callbacks never perform host I/O: one worker per stream moves PCM
 * between the Android queue contract and PulseAudio (including PipeWire's
 * Pulse server). Microphone capture is opened only after RECORDING is asked
 * for and a capture buffer has actually been supplied.
 */
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include "android_bridge.h"

#include <pulse/error.h>
#include <pulse/simple.h>

#include <errno.h>
#include <pthread.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <time.h>

extern void GoAndroid_LogAudio(char *event, char *detail);

typedef uint32_t SLresult;
typedef uint32_t SLboolean;
typedef uint32_t SLuint32;
typedef int32_t SLint32;
typedef int16_t SLint16;
typedef int16_t SLmillibel;
typedef int16_t SLpermille;
typedef const void *SLInterfaceID;

#define SL_RESULT_SUCCESS 0u
#define SL_RESULT_PRECONDITIONS_VIOLATED 1u
#define SL_RESULT_PARAMETER_INVALID 2u
#define SL_RESULT_MEMORY_FAILURE 3u
#define SL_RESULT_RESOURCE_ERROR 4u
#define SL_RESULT_BUFFER_INSUFFICIENT 7u
#define SL_RESULT_CONTENT_UNSUPPORTED 9u
#define SL_RESULT_FEATURE_UNSUPPORTED 12u

#define SL_OBJECT_STATE_UNREALIZED 1u
#define SL_OBJECT_STATE_REALIZED 2u
#define SL_PLAYSTATE_STOPPED 1u
#define SL_PLAYSTATE_PAUSED 2u
#define SL_PLAYSTATE_PLAYING 3u
#define SL_RECORDSTATE_STOPPED 1u
#define SL_RECORDSTATE_PAUSED 2u
#define SL_RECORDSTATE_RECORDING 3u
#define SL_TIME_UNKNOWN 0xffffffffu

#define SL_DATALOCATOR_OUTPUTMIX 4u
#define SL_DATALOCATOR_BUFFERQUEUE 6u
#define SL_DATALOCATOR_IODEVICE 3u
#define SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE 0x800007bdu
#define SL_DATAFORMAT_PCM 2u
#define SL_ANDROID_DATAFORMAT_PCM_EX 4u
#define SL_ANDROID_PCM_REPRESENTATION_SIGNED_INT 1u
#define SL_ANDROID_PCM_REPRESENTATION_UNSIGNED_INT 2u
#define SL_ANDROID_PCM_REPRESENTATION_FLOAT 3u
#define SL_BYTEORDER_BIGENDIAN 1u
#define SL_BYTEORDER_LITTLEENDIAN 2u
#define SL_ENGINEOPTION_THREADSAFE 1u
#define SL_SPEAKER_FRONT_CENTER 0x4u
#define SL_IODEVICE_AUDIOINPUT 1u
#define SL_DEFAULTDEVICEID_AUDIOINPUT 0xffffffffu
#define SL_DEFAULTDEVICEID_AUDIOOUTPUT 0xffffffffu

/* OpenSLES.h object class IDs, the argument of
 * SLEngineItf::QueryNumSupportedInterfaces / QuerySupportedInterfaces. */
#define SL_OBJECTID_ENGINE 0x1001u
#define SL_OBJECTID_AUDIOPLAYER 0x1004u
#define SL_OBJECTID_AUDIORECORDER 0x1005u
#define SL_OBJECTID_OUTPUTMIX 0x1009u

/* SLES/OpenSLES_AndroidConfiguration.h keys the client sets on a player or
 * recorder before Realize. FMOD tags its player as a media stream; WebRTC's
 * legacy Android ADM (opensles_player.cc / opensles_recorder.cc) tags the
 * voice player SL_ANDROID_STREAM_VOICE and the recorder with the
 * voice-communication preset. Tipsy uses the tag only to size the host
 * playback buffer for voice; nothing about the PCM path changes. */
#define SL_ANDROID_KEY_STREAM_TYPE "androidPlaybackStreamType"
#define SL_ANDROID_KEY_RECORDING_PRESET "androidRecordingPreset"
#define SL_ANDROID_KEY_PERFORMANCE_MODE "androidPerformanceMode"
#define SL_ANDROID_STREAM_VOICE 0
#define SL_ANDROID_RECORDING_PRESET_VOICE_COMMUNICATION 4

struct SLObjectItf_;
struct SLEngineItf_;
struct SLPlayItf_;
struct SLRecordItf_;
struct SLBufferQueueItf_;
struct SLVolumeItf_;
struct SLAndroidConfigurationItf_;
struct SLOutputMixItf_;

typedef const struct SLObjectItf_ *const *SLObjectItf;
typedef const struct SLEngineItf_ *const *SLEngineItf;
typedef const struct SLPlayItf_ *const *SLPlayItf;
typedef const struct SLRecordItf_ *const *SLRecordItf;
typedef const struct SLBufferQueueItf_ *const *SLBufferQueueItf;
typedef const struct SLVolumeItf_ *const *SLVolumeItf;
typedef const struct SLAndroidConfigurationItf_ *const *SLAndroidConfigurationItf;
typedef const struct SLOutputMixItf_ *const *SLOutputMixItf;

typedef void (*slObjectCallback)(SLObjectItf, const void *, SLuint32, SLresult, SLuint32, void *);
typedef void (*slPlayCallback)(SLPlayItf, void *, SLuint32);
typedef void (*slRecordCallback)(SLRecordItf, void *, SLuint32);
typedef void (*slBufferQueueCallback)(SLBufferQueueItf, void *);
typedef void (*slMixDeviceChangeCallback)(SLOutputMixItf, void *);

typedef struct {
	void *pLocator;
	void *pFormat;
} SLDataSource;
typedef SLDataSource SLDataSink;

typedef struct {
	SLuint32 locatorType;
	SLuint32 numBuffers;
} SLDataLocator_BufferQueue;

typedef struct {
	SLuint32 formatType;
	SLuint32 numChannels;
	SLuint32 samplesPerSec;
	SLuint32 bitsPerSample;
	SLuint32 containerSize;
	SLuint32 channelMask;
	SLuint32 endianness;
	SLuint32 representation;
} TipsyPCMFormat;

typedef struct {
	SLuint32 count;
	SLuint32 index;
} SLBufferQueueState;

struct SLObjectItf_ {
	SLresult (*Realize)(SLObjectItf, SLboolean);
	SLresult (*Resume)(SLObjectItf, SLboolean);
	SLresult (*GetState)(SLObjectItf, SLuint32 *);
	SLresult (*GetInterface)(SLObjectItf, SLInterfaceID, void *);
	SLresult (*RegisterCallback)(SLObjectItf, slObjectCallback, void *);
	void (*AbortAsyncOperation)(SLObjectItf);
	void (*Destroy)(SLObjectItf);
	SLresult (*SetPriority)(SLObjectItf, SLint32, SLboolean);
	SLresult (*GetPriority)(SLObjectItf, SLint32 *, SLboolean *);
	SLresult (*SetLossOfControlInterfaces)(SLObjectItf, SLint16, SLInterfaceID *, SLboolean);
};

struct SLEngineItf_ {
	void *CreateLEDDevice;
	void *CreateVibraDevice;
	SLresult (*CreateAudioPlayer)(SLEngineItf, SLObjectItf *, SLDataSource *, SLDataSink *,
	                              SLuint32, const SLInterfaceID *, const SLboolean *);
	SLresult (*CreateAudioRecorder)(SLEngineItf, SLObjectItf *, SLDataSource *, SLDataSink *,
	                                SLuint32, const SLInterfaceID *, const SLboolean *);
	void *CreateMidiPlayer;
	void *CreateListener;
	void *Create3DGroup;
	SLresult (*CreateOutputMix)(SLEngineItf, SLObjectItf *, SLuint32,
	                            const SLInterfaceID *, const SLboolean *);
	void *CreateMetadataExtractor;
	void *CreateExtensionObject;
	SLresult (*QueryNumSupportedInterfaces)(SLEngineItf, SLuint32, SLuint32 *);
	SLresult (*QuerySupportedInterfaces)(SLEngineItf, SLuint32, SLuint32, SLInterfaceID *);
	SLresult (*QueryNumSupportedExtensions)(SLEngineItf, SLuint32 *);
	SLresult (*QuerySupportedExtension)(SLEngineItf, SLuint32, uint8_t *, SLint16 *);
	SLresult (*IsExtensionSupported)(SLEngineItf, const uint8_t *, SLboolean *);
};

struct SLPlayItf_ {
	SLresult (*SetPlayState)(SLPlayItf, SLuint32);
	SLresult (*GetPlayState)(SLPlayItf, SLuint32 *);
	SLresult (*GetDuration)(SLPlayItf, SLuint32 *);
	SLresult (*GetPosition)(SLPlayItf, SLuint32 *);
	SLresult (*RegisterCallback)(SLPlayItf, slPlayCallback, void *);
	SLresult (*SetCallbackEventsMask)(SLPlayItf, SLuint32);
	SLresult (*GetCallbackEventsMask)(SLPlayItf, SLuint32 *);
	SLresult (*SetMarkerPosition)(SLPlayItf, SLuint32);
	SLresult (*ClearMarkerPosition)(SLPlayItf);
	SLresult (*GetMarkerPosition)(SLPlayItf, SLuint32 *);
	SLresult (*SetPositionUpdatePeriod)(SLPlayItf, SLuint32);
	SLresult (*GetPositionUpdatePeriod)(SLPlayItf, SLuint32 *);
};

struct SLRecordItf_ {
	SLresult (*SetRecordState)(SLRecordItf, SLuint32);
	SLresult (*GetRecordState)(SLRecordItf, SLuint32 *);
	SLresult (*SetDurationLimit)(SLRecordItf, SLuint32);
	SLresult (*GetPosition)(SLRecordItf, SLuint32 *);
	SLresult (*RegisterCallback)(SLRecordItf, slRecordCallback, void *);
	SLresult (*SetCallbackEventsMask)(SLRecordItf, SLuint32);
	SLresult (*GetCallbackEventsMask)(SLRecordItf, SLuint32 *);
	SLresult (*SetMarkerPosition)(SLRecordItf, SLuint32);
	SLresult (*ClearMarkerPosition)(SLRecordItf);
	SLresult (*GetMarkerPosition)(SLRecordItf, SLuint32 *);
	SLresult (*SetPositionUpdatePeriod)(SLRecordItf, SLuint32);
	SLresult (*GetPositionUpdatePeriod)(SLRecordItf, SLuint32 *);
};

struct SLBufferQueueItf_ {
	SLresult (*Enqueue)(SLBufferQueueItf, const void *, SLuint32);
	SLresult (*Clear)(SLBufferQueueItf);
	SLresult (*GetState)(SLBufferQueueItf, SLBufferQueueState *);
	SLresult (*RegisterCallback)(SLBufferQueueItf, slBufferQueueCallback, void *);
};

struct SLVolumeItf_ {
	SLresult (*SetVolumeLevel)(SLVolumeItf, SLmillibel);
	SLresult (*GetVolumeLevel)(SLVolumeItf, SLmillibel *);
	SLresult (*GetMaxVolumeLevel)(SLVolumeItf, SLmillibel *);
	SLresult (*SetMute)(SLVolumeItf, SLboolean);
	SLresult (*GetMute)(SLVolumeItf, SLboolean *);
	SLresult (*EnableStereoPosition)(SLVolumeItf, SLboolean);
	SLresult (*IsEnabledStereoPosition)(SLVolumeItf, SLboolean *);
	SLresult (*SetStereoPosition)(SLVolumeItf, SLpermille);
	SLresult (*GetStereoPosition)(SLVolumeItf, SLpermille *);
};

struct SLAndroidConfigurationItf_ {
	SLresult (*SetConfiguration)(SLAndroidConfigurationItf, const uint8_t *, const void *, SLuint32);
	SLresult (*GetConfiguration)(SLAndroidConfigurationItf, const uint8_t *, SLuint32 *, void *);
	SLresult (*AcquireJavaProxy)(SLAndroidConfigurationItf, SLuint32, void **);
	SLresult (*ReleaseJavaProxy)(SLAndroidConfigurationItf, SLuint32);
};

struct SLOutputMixItf_ {
	SLresult (*GetDestinationOutputDeviceIDs)(SLOutputMixItf, SLint32 *, SLuint32 *);
	SLresult (*RegisterDeviceChangeCallback)(SLOutputMixItf, slMixDeviceChangeCallback, void *);
	SLresult (*ReRoute)(SLOutputMixItf, SLint32, SLuint32 *);
};

static uint32_t sl_iid_engine[4] = {1, 0, 0, 0};
static uint32_t sl_iid_play[4] = {2, 0, 0, 0};
static uint32_t sl_iid_bufferqueue[4] = {3, 0, 0, 0};
static uint32_t sl_iid_volume[4] = {4, 0, 0, 0};
static uint32_t sl_iid_outputmix[4] = {5, 0, 0, 0};
static uint32_t sl_iid_androidconfig[4] = {6, 0, 0, 0};
static uint32_t sl_iid_androidsimplebufferqueue[4] = {7, 0, 0, 0};
static uint32_t sl_iid_record[4] = {8, 0, 0, 0};
static uint32_t sl_iid_seek[4] = {9, 0, 0, 0};
static uint32_t sl_iid_prefetch[4] = {10, 0, 0, 0};
static uint32_t sl_iid_androidbufferqueue[4] = {11, 0, 0, 0};

const void *SL_IID_ENGINE = sl_iid_engine;
const void *SL_IID_PLAY = sl_iid_play;
const void *SL_IID_BUFFERQUEUE = sl_iid_bufferqueue;
const void *SL_IID_VOLUME = sl_iid_volume;
const void *SL_IID_OUTPUTMIX = sl_iid_outputmix;
const void *SL_IID_ANDROIDCONFIGURATION = sl_iid_androidconfig;
const void *SL_IID_ANDROIDSIMPLEBUFFERQUEUE = sl_iid_androidsimplebufferqueue;
const void *SL_IID_RECORD = sl_iid_record;
const void *SL_IID_SEEK = sl_iid_seek;
const void *SL_IID_PREFETCHSTATUS = sl_iid_prefetch;
const void *SL_IID_ANDROIDBUFFERQUEUESOURCE = sl_iid_androidbufferqueue;

typedef enum {
	OBJ_ENGINE,
	OBJ_OUTPUT_MIX,
	OBJ_PLAYER,
	OBJ_RECORDER,
} object_kind;

typedef struct queue_node {
	struct queue_node *next;
	void *client_buffer;
	uint8_t *io_buffer;
	uint32_t size;
	uint64_t generation;
} queue_node;

typedef struct tipsy_sl_object {
	const struct SLObjectItf_ *object_vt;
	const struct SLEngineItf_ *engine_vt;
	const struct SLPlayItf_ *play_vt;
	const struct SLRecordItf_ *record_vt;
	const struct SLBufferQueueItf_ *queue_vt;
	const struct SLVolumeItf_ *volume_vt;
	const struct SLAndroidConfigurationItf_ *config_vt;
	const struct SLOutputMixItf_ *mix_vt;
	object_kind kind;
	SLuint32 realized;
	SLuint32 state;
	SLint32 priority;
	SLboolean preemptable;
	SLmillibel volume;
	SLboolean mute;
	SLboolean stereo_enabled;
	SLpermille stereo_position;
	SLuint32 callback_mask;
	SLuint32 marker;
	SLuint32 update_period;
	SLuint32 duration_limit;
	uint64_t bytes_transferred;
	pa_sample_spec sample;
	uint32_t queue_capacity;
	uint32_t queue_count;
	uint32_t queue_index;
	uint64_t queue_generation;
	queue_node *head;
	queue_node *tail;
	int inflight;
	int close_requested;
	int destroying;
	int destroy_deferred;
	int thread_started;
	int first_flow_logged;
	int backend_fake;
	int voice_stream;
	pa_simple *stream;
	pthread_t thread;
	pthread_mutex_t mu;
	pthread_cond_t cond;
	slObjectCallback object_cb;
	void *object_ctx;
	slPlayCallback play_cb;
	void *play_ctx;
	slRecordCallback record_cb;
	void *record_ctx;
	slBufferQueueCallback queue_cb;
	void *queue_ctx;
	slMixDeviceChangeCallback mix_cb;
	void *mix_ctx;
} tipsy_sl_object;

#define FROM_MEMBER(ptr, type, member) ((type *)((char *)(ptr)-offsetof(type, member)))
static tipsy_sl_object *from_object(SLObjectItf self) { return (tipsy_sl_object *)self; }
static tipsy_sl_object *from_engine(SLEngineItf self) { return FROM_MEMBER(self, tipsy_sl_object, engine_vt); }
static tipsy_sl_object *from_play(SLPlayItf self) { return FROM_MEMBER(self, tipsy_sl_object, play_vt); }
static tipsy_sl_object *from_record(SLRecordItf self) { return FROM_MEMBER(self, tipsy_sl_object, record_vt); }
static tipsy_sl_object *from_queue(SLBufferQueueItf self) { return FROM_MEMBER(self, tipsy_sl_object, queue_vt); }
static tipsy_sl_object *from_volume(SLVolumeItf self) { return FROM_MEMBER(self, tipsy_sl_object, volume_vt); }
static tipsy_sl_object *from_config(SLAndroidConfigurationItf self) { return FROM_MEMBER(self, tipsy_sl_object, config_vt); }
static tipsy_sl_object *from_mix(SLOutputMixItf self) { return FROM_MEMBER(self, tipsy_sl_object, mix_vt); }

typedef struct {
	pthread_mutex_t mu;
	int enabled;
	int fail_first_write;
	int fail_first_read;
	uint32_t opens;
	uint32_t writes;
	uint32_t reads;
	uint64_t written_bytes;
	uint64_t read_bytes;
} fake_backend_state;

static fake_backend_state fake_backend = {
	.mu = PTHREAD_MUTEX_INITIALIZER,
};

static pthread_mutex_t capture_gate_mu = PTHREAD_MUTEX_INITIALIZER;
static int capture_muted;
static int capture_denied_logged;
static int capture_muted_logged;
static int capture_error_logged;

static void audio_log(const char *event, const char *detail)
{
	GoAndroid_LogAudio((char *)event, (char *)(detail ? detail : ""));
}

static int iid_equal(SLInterfaceID a, SLInterfaceID b)
{
	return a != NULL && b != NULL && (a == b || memcmp(a, b, 16) == 0);
}

/* Observation-only diagnostics for what the client probes and the bridge
 * refuses. Each distinct key is logged once per process; the table is small
 * and bounded so a probing loop cannot flood the log. Nothing here changes a
 * result code. */
#define ONCE_SLOTS 48
#define ONCE_KEY_CAP 160
static pthread_mutex_t once_mu = PTHREAD_MUTEX_INITIALIZER;
static char once_keys[ONCE_SLOTS][ONCE_KEY_CAP];
static int once_count;

static int log_once(const char *event, const char *detail)
{
	char key[ONCE_KEY_CAP];
	snprintf(key, sizeof(key), "%s|%s", event, detail ? detail : "");
	pthread_mutex_lock(&once_mu);
	for (int i = 0; i < once_count; i++) {
		if (strcmp(once_keys[i], key) == 0) {
			pthread_mutex_unlock(&once_mu);
			return 0;
		}
	}
	if (once_count >= ONCE_SLOTS) {
		pthread_mutex_unlock(&once_mu);
		return 0;
	}
	snprintf(once_keys[once_count], ONCE_KEY_CAP, "%s", key);
	once_count++;
	pthread_mutex_unlock(&once_mu);
	audio_log(event, detail);
	return 1;
}

static void log_once_reset(void)
{
	pthread_mutex_lock(&once_mu);
	once_count = 0;
	pthread_mutex_unlock(&once_mu);
}

static const char *iid_name(SLInterfaceID iid)
{
	static const struct { const void **iid; const char *name; } table[] = {
		{&SL_IID_ENGINE, "ENGINE"}, {&SL_IID_PLAY, "PLAY"}, {&SL_IID_BUFFERQUEUE, "BUFFERQUEUE"},
		{&SL_IID_VOLUME, "VOLUME"}, {&SL_IID_OUTPUTMIX, "OUTPUTMIX"},
		{&SL_IID_ANDROIDCONFIGURATION, "ANDROIDCONFIGURATION"},
		{&SL_IID_ANDROIDSIMPLEBUFFERQUEUE, "ANDROIDSIMPLEBUFFERQUEUE"}, {&SL_IID_RECORD, "RECORD"},
		{&SL_IID_SEEK, "SEEK"}, {&SL_IID_PREFETCHSTATUS, "PREFETCHSTATUS"},
		{&SL_IID_ANDROIDBUFFERQUEUESOURCE, "ANDROIDBUFFERQUEUESOURCE"},
	};
	for (size_t i = 0; i < sizeof(table) / sizeof(table[0]); i++) {
		if (iid_equal(iid, *table[i].iid))
			return table[i].name;
	}
	return NULL;
}

/* SLInterfaceID_ is {time_low, time_mid, time_hi_and_version, clock_seq,
 * node[6]}; print it the way OpenSLES_IID.c spells GUIDs so an unknown
 * request can be matched against the Khronos / Android header tables. */
static void iid_describe(SLInterfaceID iid, char *out, size_t cap)
{
	const char *name = iid_name(iid);
	if (name != NULL) {
		snprintf(out, cap, "%s", name);
		return;
	}
	if (iid == NULL) {
		snprintf(out, cap, "(null)");
		return;
	}
	const uint8_t *b = iid;
	uint32_t time_low;
	uint16_t time_mid, time_hi, clock_seq;
	memcpy(&time_low, b, 4);
	memcpy(&time_mid, b + 4, 2);
	memcpy(&time_hi, b + 6, 2);
	memcpy(&clock_seq, b + 8, 2);
	snprintf(out, cap, "%08x-%04x-%04x-%04x-%02x%02x%02x%02x%02x%02x", time_low, time_mid, time_hi,
	         clock_seq, b[10], b[11], b[12], b[13], b[14], b[15]);
}

static const char *kind_name(object_kind kind)
{
	switch (kind) {
	case OBJ_ENGINE:
		return "engine";
	case OBJ_OUTPUT_MIX:
		return "outputmix";
	case OBJ_PLAYER:
		return "player";
	case OBJ_RECORDER:
		return "recorder";
	}
	return "object";
}

static int env_is_truthy(const char *v)
{
	return v != NULL && (strcmp(v, "1") == 0 || strcasecmp(v, "true") == 0 || strcasecmp(v, "yes") == 0);
}

static int env_is_falsey(const char *v)
{
	return v != NULL && (strcmp(v, "0") == 0 || strcasecmp(v, "off") == 0 ||
	                      strcasecmp(v, "false") == 0 || strcasecmp(v, "no") == 0);
}

static int microphone_disabled(void)
{
	if (env_is_truthy(getenv("TIPSY_DISABLE_MICROPHONE")))
		return 1;
	return env_is_falsey(getenv("TIPSY_MICROPHONE"));
}

int tipsy_audio_microphone_disabled(void)
{
	return microphone_disabled();
}

void tipsy_audio_set_capture_muted(int muted)
{
	pthread_mutex_lock(&capture_gate_mu);
	capture_muted = muted ? 1 : 0;
	if (!capture_muted)
		capture_muted_logged = 0;
	pthread_mutex_unlock(&capture_gate_mu);
}

int tipsy_audio_capture_muted(void)
{
	pthread_mutex_lock(&capture_gate_mu);
	int muted = capture_muted;
	pthread_mutex_unlock(&capture_gate_mu);
	return muted;
}

static int capture_is_muted(void)
{
	pthread_mutex_lock(&capture_gate_mu);
	int muted = capture_muted;
	int already = capture_muted_logged;
	if (muted && !already)
		capture_muted_logged = 1;
	pthread_mutex_unlock(&capture_gate_mu);
	if (muted && !already)
		audio_log("capture muted", "");
	return muted;
}

static void log_capture_denied_once(void)
{
	pthread_mutex_lock(&capture_gate_mu);
	int already = capture_denied_logged;
	capture_denied_logged = 1;
	pthread_mutex_unlock(&capture_gate_mu);
	if (!already)
		audio_log("capture denied", "microphone disabled");
}

static void log_capture_error_once(const char *detail)
{
	pthread_mutex_lock(&capture_gate_mu);
	int already = capture_error_logged;
	capture_error_logged = 1;
	pthread_mutex_unlock(&capture_gate_mu);
	if (!already)
		audio_log("capture stream error", detail ? detail : "");
}

static void capture_error_cleared(void)
{
	pthread_mutex_lock(&capture_gate_mu);
	capture_error_logged = 0;
	pthread_mutex_unlock(&capture_gate_mu);
}

static void capture_denied_reset_if_allowed(void)
{
	if (microphone_disabled())
		return;
	pthread_mutex_lock(&capture_gate_mu);
	capture_denied_logged = 0;
	pthread_mutex_unlock(&capture_gate_mu);
}

static int backend_open(tipsy_sl_object *o)
{
	char detail[192];
	if (o->kind == OBJ_RECORDER && microphone_disabled()) {
		log_capture_denied_once();
		return -1;
	}
	capture_denied_reset_if_allowed();
	if (o->backend_fake) {
		pthread_mutex_lock(&fake_backend.mu);
		fake_backend.opens++;
		pthread_mutex_unlock(&fake_backend.mu);
		o->stream = (pa_simple *)o;
		return 0;
	}
	int error = 0;
	int pinned = 0;
	const char *dev = NULL;
	pa_buffer_attr rec_attr;
	pa_buffer_attr play_attr;
	const pa_buffer_attr *attr = NULL;
	pthread_mutex_lock(&o->mu);
	int voice = o->voice_stream;
	pthread_mutex_unlock(&o->mu);
	if (o->kind == OBJ_RECORDER) {
		const char *src = getenv("TIPSY_MICROPHONE_SOURCE");
		if (src != NULL && src[0] != '\0') {
			dev = src;
			pinned = 1;
		}
		size_t frame = pa_frame_size(&o->sample);
		uint32_t frag = (uint32_t)(frame * (o->sample.rate / 50u));
		if (frag < 128)
			frag = 128;
		memset(&rec_attr, 0xff, sizeof(rec_attr));
		rec_attr.fragsize = frag;
		attr = &rec_attr;
	} else {
		/* Every OpenSL player paces itself on buffer completion: WebRTC's
		 * OpenSLESPlayer keeps two 10 ms buffers in flight and assumes a
		 * ~50 ms device path (kLowLatencyModeDelayEstimateInMilliseconds);
		 * FMOD's OpenSL output refills one DSP block per completion. The
		 * Pulse server's default target length is seconds (PipeWire's
		 * pulse server: 2 s), which would sit between the mixer and the
		 * speaker. Ask for a 50 ms target instead (the simple API connects
		 * with PA_STREAM_ADJUST_LATENCY); the Java AudioTrack path FMOD
		 * used before requested the same order of buffering
		 * (fmod_audio.go, tlength = the client's block bytes). */
		size_t frame = pa_frame_size(&o->sample);
		memset(&play_attr, 0xff, sizeof(play_attr));
		play_attr.tlength = (uint32_t)(frame * (o->sample.rate / 20u));
		attr = &play_attr;
	}
	pa_stream_direction_t direction = o->kind == OBJ_RECORDER ? PA_STREAM_RECORD : PA_STREAM_PLAYBACK;
	o->stream = pa_simple_new(NULL, "Tipsy", direction, dev,
	                          o->kind == OBJ_RECORDER ? "Roblox microphone" : (voice ? "Roblox voice" : "Roblox audio"),
	                          &o->sample, NULL, attr, &error);
	if (o->stream == NULL) {
		snprintf(detail, sizeof(detail), "%s (%u Hz, %u ch)", pa_strerror(error),
		         o->sample.rate, o->sample.channels);
		audio_log(o->kind == OBJ_RECORDER ? "capture device unavailable" : "playback device unavailable", detail);
		return -1;
	}
	const char *role = "";
	if (voice)
		role = o->kind == OBJ_RECORDER ? "; voice communication preset" : "; voice stream, 50 ms target";
	else if (o->kind == OBJ_PLAYER)
		role = "; media stream, 50 ms target";
	snprintf(detail, sizeof(detail), "PulseAudio %u Hz, %u ch, format=%d%s%s", o->sample.rate,
	         o->sample.channels, (int)o->sample.format, role,
	         (o->kind == OBJ_RECORDER && pinned) ? "; pinned source set" : "");
	audio_log(o->kind == OBJ_RECORDER ? "capture stream opened" : "playback stream opened", detail);
	return 0;
}

static void backend_close(tipsy_sl_object *o)
{
	if (o->stream == NULL)
		return;
	if (!o->backend_fake)
		pa_simple_free(o->stream);
	o->stream = NULL;
}

static int backend_transfer(tipsy_sl_object *o, void *buffer, size_t bytes)
{
	if (o->kind == OBJ_RECORDER && capture_is_muted()) {
		memset(buffer, 0, bytes);
		if (o->backend_fake) {
			pthread_mutex_lock(&fake_backend.mu);
			fake_backend.reads++;
			fake_backend.read_bytes += bytes;
			pthread_mutex_unlock(&fake_backend.mu);
		}
		capture_error_cleared();
		return 0;
	}
	if (o->backend_fake) {
		int fail = 0;
		pthread_mutex_lock(&fake_backend.mu);
		if (o->kind == OBJ_RECORDER) {
			fake_backend.reads++;
			if (fake_backend.fail_first_read) {
				fake_backend.fail_first_read = 0;
				fail = 1;
			} else {
				memset(buffer, 0x5a, bytes);
				fake_backend.read_bytes += bytes;
			}
		} else {
			fake_backend.writes++;
			if (fake_backend.fail_first_write) {
				fake_backend.fail_first_write = 0;
				fail = 1;
			} else {
				fake_backend.written_bytes += bytes;
			}
		}
		pthread_mutex_unlock(&fake_backend.mu);
		if (fail && o->kind == OBJ_RECORDER)
			log_capture_error_once("host transfer failed");
		else if (!fail && o->kind == OBJ_RECORDER)
			capture_error_cleared();
		return fail ? -1 : 0;
	}
	int error = 0;
	int rc = o->kind == OBJ_RECORDER
	             ? pa_simple_read(o->stream, buffer, bytes, &error)
	             : pa_simple_write(o->stream, buffer, bytes, &error);
	if (rc < 0) {
		if (o->kind == OBJ_RECORDER)
			log_capture_error_once(pa_strerror(error));
		else
			audio_log("playback stream error", pa_strerror(error));
	} else if (o->kind == OBJ_RECORDER)
		capture_error_cleared();
	return rc;
}

static void free_node(queue_node *n)
{
	if (n == NULL)
		return;
	free(n->io_buffer);
	free(n);
}

static void free_pending_locked(tipsy_sl_object *o)
{
	queue_node *n = o->head;
	while (n != NULL) {
		queue_node *next = n->next;
		free_node(n);
		n = next;
	}
	o->head = o->tail = NULL;
	o->queue_count = o->inflight ? 1u : 0u;
}

static int state_active(const tipsy_sl_object *o)
{
	return (o->kind == OBJ_PLAYER && o->state == SL_PLAYSTATE_PLAYING) ||
	       (o->kind == OBJ_RECORDER && o->state == SL_RECORDSTATE_RECORDING);
}

static void retry_pause(void)
{
	struct timespec ts = {.tv_sec = 0, .tv_nsec = 100000000L};
	nanosleep(&ts, NULL);
}

static void *stream_worker(void *arg)
{
	tipsy_sl_object *o = arg;
	(void)pthread_setname_np(pthread_self(), "tip.opensles");
	pthread_mutex_lock(&o->mu);
	for (;;) {
		while (!o->destroying && (!state_active(o) || o->head == NULL)) {
			if (o->close_requested) {
				o->close_requested = 0;
				pthread_mutex_unlock(&o->mu);
				backend_close(o);
				pthread_mutex_lock(&o->mu);
			}
			pthread_cond_wait(&o->cond, &o->mu);
		}
		if (o->destroying)
			break;

		queue_node *n = o->head;
		o->head = n->next;
		if (o->head == NULL)
			o->tail = NULL;
		n->next = NULL;
		o->inflight = 1;
		pthread_mutex_unlock(&o->mu);

		int rc = 0;
		if (o->kind == OBJ_RECORDER && microphone_disabled()) {
			log_capture_denied_once();
			backend_close(o);
			rc = -1;
		} else {
			if (o->stream == NULL)
				rc = backend_open(o);
			if (rc == 0)
				rc = backend_transfer(o, n->io_buffer, n->size);
		}
		if (rc < 0)
			backend_close(o);

		pthread_mutex_lock(&o->mu);
		o->inflight = 0;
		if (rc < 0 && !o->destroying && n->generation == o->queue_generation) {
			n->next = o->head;
			o->head = n;
			if (o->tail == NULL)
				o->tail = n;
			pthread_mutex_unlock(&o->mu);
			retry_pause();
			pthread_mutex_lock(&o->mu);
			continue;
		}

		if (o->queue_count > 0)
			o->queue_count--;
		int deliver = rc == 0 && !o->destroying && n->generation == o->queue_generation;
		if (deliver && o->kind == OBJ_RECORDER)
			memcpy(n->client_buffer, n->io_buffer, n->size);
		if (deliver) {
			o->queue_index++;
			o->bytes_transferred += n->size;
		}
		slBufferQueueCallback cb = deliver ? o->queue_cb : NULL;
		void *ctx = o->queue_ctx;
		if (deliver && !o->first_flow_logged) {
			o->first_flow_logged = 1;
			audio_log(o->kind == OBJ_RECORDER ? "capture flowing" : "playback flowing",
			          "first buffer completed; audio content is not logged");
		}
		pthread_mutex_unlock(&o->mu);
		free_node(n);
		if (cb != NULL)
			cb((SLBufferQueueItf)&o->queue_vt, ctx);
		pthread_mutex_lock(&o->mu);
	}
	pthread_mutex_unlock(&o->mu);
	backend_close(o);
	if (o->destroy_deferred) {
		pthread_cond_destroy(&o->cond);
		pthread_mutex_destroy(&o->mu);
		free(o);
	}
	return NULL;
}

static int format_from(const void *format, pa_sample_spec *out)
{
	if (format == NULL || out == NULL)
		return -1;
	const TipsyPCMFormat *pcm = format;
	if (pcm->formatType != SL_DATAFORMAT_PCM && pcm->formatType != SL_ANDROID_DATAFORMAT_PCM_EX)
		return -1;
	if (pcm->numChannels < 1 || pcm->numChannels > 8 || pcm->samplesPerSec < 8000000u)
		return -1;
	out->rate = pcm->samplesPerSec / 1000u;
	out->channels = (uint8_t)pcm->numChannels;
	if (out->rate < 8000 || out->rate > PA_RATE_MAX)
		return -1;
	SLuint32 representation = pcm->formatType == SL_ANDROID_DATAFORMAT_PCM_EX
	                              ? pcm->representation
	                              : SL_ANDROID_PCM_REPRESENTATION_SIGNED_INT;
	int big = pcm->endianness == SL_BYTEORDER_BIGENDIAN;
	if (representation == SL_ANDROID_PCM_REPRESENTATION_FLOAT && pcm->containerSize == 32)
		out->format = big ? PA_SAMPLE_FLOAT32BE : PA_SAMPLE_FLOAT32LE;
	else if (representation == SL_ANDROID_PCM_REPRESENTATION_UNSIGNED_INT && pcm->containerSize == 8)
		out->format = PA_SAMPLE_U8;
	else if (representation == SL_ANDROID_PCM_REPRESENTATION_SIGNED_INT && pcm->containerSize == 16)
		out->format = big ? PA_SAMPLE_S16BE : PA_SAMPLE_S16LE;
	else if (representation == SL_ANDROID_PCM_REPRESENTATION_SIGNED_INT && pcm->containerSize == 24)
		out->format = big ? PA_SAMPLE_S24BE : PA_SAMPLE_S24LE;
	else if (representation == SL_ANDROID_PCM_REPRESENTATION_SIGNED_INT && pcm->containerSize == 32)
		out->format = big ? PA_SAMPLE_S32BE : PA_SAMPLE_S32LE;
	else
		return -1;
	return pa_sample_spec_valid(out) ? 0 : -1;
}

static int supports_iid(object_kind kind, SLInterfaceID iid)
{
	if (kind == OBJ_PLAYER)
		return iid_equal(iid, SL_IID_PLAY) || iid_equal(iid, SL_IID_BUFFERQUEUE) ||
		       iid_equal(iid, SL_IID_ANDROIDSIMPLEBUFFERQUEUE) || iid_equal(iid, SL_IID_VOLUME) ||
		       iid_equal(iid, SL_IID_ANDROIDCONFIGURATION);
	if (kind == OBJ_RECORDER)
		return iid_equal(iid, SL_IID_RECORD) || iid_equal(iid, SL_IID_BUFFERQUEUE) ||
		       iid_equal(iid, SL_IID_ANDROIDSIMPLEBUFFERQUEUE) ||
		       iid_equal(iid, SL_IID_ANDROIDCONFIGURATION);
	return kind == OBJ_ENGINE ? iid_equal(iid, SL_IID_ENGINE) : iid_equal(iid, SL_IID_OUTPUTMIX);
}

static int required_interfaces_supported(object_kind kind, SLuint32 count,
	                                     const SLInterfaceID *ids, const SLboolean *required)
{
	char detail[224];
	for (SLuint32 i = 0; i < count; i++) {
		if (ids != NULL && !supports_iid(kind, ids[i])) {
			char iid[64];
			iid_describe(ids[i], iid, sizeof(iid));
			snprintf(detail, sizeof(detail), "%s create: interface %s not offered (%s)", kind_name(kind),
			         iid, required != NULL && required[i] ? "required; create refused" : "optional; ignored");
			log_once("OpenSL interface request", detail);
		}
		if (required != NULL && required[i] && (ids == NULL || !supports_iid(kind, ids[i])))
			return 0;
	}
	return 1;
}

/* Interfaces the bridge exposes per object class, in the order
 * QuerySupportedInterfaces enumerates them. Only what GetInterface can hand
 * out is listed; nothing is advertised that would then be refused. */
static SLuint32 supported_iids(object_kind kind, SLInterfaceID *out, SLuint32 cap)
{
	SLInterfaceID list[5];
	SLuint32 n = 0;
	switch (kind) {
	case OBJ_ENGINE:
		list[n++] = SL_IID_ENGINE;
		break;
	case OBJ_OUTPUT_MIX:
		list[n++] = SL_IID_OUTPUTMIX;
		break;
	case OBJ_PLAYER:
		list[n++] = SL_IID_PLAY;
		list[n++] = SL_IID_BUFFERQUEUE;
		list[n++] = SL_IID_ANDROIDSIMPLEBUFFERQUEUE;
		list[n++] = SL_IID_VOLUME;
		list[n++] = SL_IID_ANDROIDCONFIGURATION;
		break;
	case OBJ_RECORDER:
		list[n++] = SL_IID_RECORD;
		list[n++] = SL_IID_BUFFERQUEUE;
		list[n++] = SL_IID_ANDROIDSIMPLEBUFFERQUEUE;
		list[n++] = SL_IID_ANDROIDCONFIGURATION;
		break;
	}
	for (SLuint32 i = 0; out != NULL && i < n && i < cap; i++)
		out[i] = list[i];
	return n;
}

static int object_kind_from_id(SLuint32 object_id, object_kind *kind)
{
	switch (object_id) {
	case SL_OBJECTID_ENGINE:
		*kind = OBJ_ENGINE;
		return 1;
	case SL_OBJECTID_OUTPUTMIX:
		*kind = OBJ_OUTPUT_MIX;
		return 1;
	case SL_OBJECTID_AUDIOPLAYER:
		*kind = OBJ_PLAYER;
		return 1;
	case SL_OBJECTID_AUDIORECORDER:
		*kind = OBJ_RECORDER;
		return 1;
	}
	return 0;
}

static SLresult object_realize(SLObjectItf self, SLboolean async)
{
	(void)async;
	if (self == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	from_object(self)->realized = SL_OBJECT_STATE_REALIZED;
	return SL_RESULT_SUCCESS;
}

static SLresult object_resume(SLObjectItf self, SLboolean async)
{
	return object_realize(self, async);
}

static SLresult object_get_state(SLObjectItf self, SLuint32 *state)
{
	if (self == NULL || state == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*state = from_object(self)->realized;
	return SL_RESULT_SUCCESS;
}

static SLresult object_get_interface(SLObjectItf self, SLInterfaceID iid, void *out)
{
	if (self == NULL || iid == NULL || out == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_object(self);
	if (o->kind == OBJ_ENGINE && iid_equal(iid, SL_IID_ENGINE))
		*(SLEngineItf *)out = (SLEngineItf)&o->engine_vt;
	else if (o->kind == OBJ_OUTPUT_MIX && iid_equal(iid, SL_IID_OUTPUTMIX))
		*(SLOutputMixItf *)out = (SLOutputMixItf)&o->mix_vt;
	else if (o->kind == OBJ_PLAYER && iid_equal(iid, SL_IID_PLAY))
		*(SLPlayItf *)out = (SLPlayItf)&o->play_vt;
	else if (o->kind == OBJ_RECORDER && iid_equal(iid, SL_IID_RECORD))
		*(SLRecordItf *)out = (SLRecordItf)&o->record_vt;
	else if ((o->kind == OBJ_PLAYER || o->kind == OBJ_RECORDER) &&
	         (iid_equal(iid, SL_IID_BUFFERQUEUE) || iid_equal(iid, SL_IID_ANDROIDSIMPLEBUFFERQUEUE)))
		*(SLBufferQueueItf *)out = (SLBufferQueueItf)&o->queue_vt;
	else if (o->kind == OBJ_PLAYER && iid_equal(iid, SL_IID_VOLUME))
		*(SLVolumeItf *)out = (SLVolumeItf)&o->volume_vt;
	else if ((o->kind == OBJ_PLAYER || o->kind == OBJ_RECORDER) &&
	         iid_equal(iid, SL_IID_ANDROIDCONFIGURATION))
		*(SLAndroidConfigurationItf *)out = (SLAndroidConfigurationItf)&o->config_vt;
	else {
		char iid_text[64], detail[160];
		iid_describe(iid, iid_text, sizeof(iid_text));
		snprintf(detail, sizeof(detail), "%s GetInterface(%s): FEATURE_UNSUPPORTED", kind_name(o->kind), iid_text);
		log_once("OpenSL interface request", detail);
		*(void **)out = NULL;
		return SL_RESULT_FEATURE_UNSUPPORTED;
	}
	return SL_RESULT_SUCCESS;
}

static SLresult object_register_callback(SLObjectItf self, slObjectCallback cb, void *ctx)
{
	if (self == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	from_object(self)->object_cb = cb;
	from_object(self)->object_ctx = ctx;
	return SL_RESULT_SUCCESS;
}

static void object_abort(SLObjectItf self) { (void)self; }

static void object_destroy(SLObjectItf self)
{
	if (self == NULL)
		return;
	tipsy_sl_object *o = from_object(self);
	if (o->thread_started) {
		pthread_mutex_lock(&o->mu);
		o->destroying = 1;
		o->queue_generation++;
		free_pending_locked(o);
		pthread_cond_broadcast(&o->cond);
		pthread_mutex_unlock(&o->mu);
		if (pthread_equal(pthread_self(), o->thread)) {
			o->destroy_deferred = 1;
			return;
		}
		pthread_join(o->thread, NULL);
	}
	pthread_cond_destroy(&o->cond);
	pthread_mutex_destroy(&o->mu);
	free(o);
}

static SLresult object_set_priority(SLObjectItf self, SLint32 priority, SLboolean preemptable)
{
	if (self == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	from_object(self)->priority = priority;
	from_object(self)->preemptable = preemptable;
	return SL_RESULT_SUCCESS;
}

static SLresult object_get_priority(SLObjectItf self, SLint32 *priority, SLboolean *preemptable)
{
	if (self == NULL || priority == NULL || preemptable == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*priority = from_object(self)->priority;
	*preemptable = from_object(self)->preemptable;
	return SL_RESULT_SUCCESS;
}

static SLresult object_set_loss(SLObjectItf self, SLint16 n, SLInterfaceID *ids, SLboolean enabled)
{
	(void)self;
	(void)n;
	(void)ids;
	(void)enabled;
	return SL_RESULT_SUCCESS;
}

static const struct SLObjectItf_ object_vtable = {
	object_realize, object_resume, object_get_state, object_get_interface, object_register_callback,
	object_abort, object_destroy, object_set_priority, object_get_priority, object_set_loss,
};

static tipsy_sl_object *object_new(object_kind kind)
{
	tipsy_sl_object *o = calloc(1, sizeof(*o));
	if (o == NULL)
		return NULL;
	o->object_vt = &object_vtable;
	o->kind = kind;
	o->realized = SL_OBJECT_STATE_UNREALIZED;
	o->state = kind == OBJ_RECORDER ? SL_RECORDSTATE_STOPPED : SL_PLAYSTATE_STOPPED;
	o->update_period = 1000;
	o->queue_capacity = 4;
	pthread_mutex_init(&o->mu, NULL);
	pthread_cond_init(&o->cond, NULL);
	pthread_mutex_lock(&fake_backend.mu);
	o->backend_fake = fake_backend.enabled;
	pthread_mutex_unlock(&fake_backend.mu);
	return o;
}

static SLresult set_stream_state(tipsy_sl_object *o, SLuint32 state, int recording)
{
	if (o == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	if ((!recording && state != SL_PLAYSTATE_STOPPED && state != SL_PLAYSTATE_PAUSED && state != SL_PLAYSTATE_PLAYING) ||
	    (recording && state != SL_RECORDSTATE_STOPPED && state != SL_RECORDSTATE_PAUSED && state != SL_RECORDSTATE_RECORDING))
		return SL_RESULT_PARAMETER_INVALID;
	pthread_mutex_lock(&o->mu);
	o->state = state;
	if (state == SL_PLAYSTATE_STOPPED || state == SL_RECORDSTATE_STOPPED)
		o->close_requested = 1;
	pthread_cond_broadcast(&o->cond);
	pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}

static SLresult play_set_state(SLPlayItf self, SLuint32 state) { return set_stream_state(from_play(self), state, 0); }
static SLresult play_get_state(SLPlayItf self, SLuint32 *state)
{
	if (self == NULL || state == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_play(self);
	pthread_mutex_lock(&o->mu);
	*state = o->state;
	pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}
static SLresult play_get_duration(SLPlayItf self, SLuint32 *msec)
{
	(void)self;
	if (msec == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*msec = SL_TIME_UNKNOWN;
	return SL_RESULT_SUCCESS;
}
static SLresult stream_get_position(tipsy_sl_object *o, SLuint32 *msec)
{
	if (o == NULL || msec == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	pthread_mutex_lock(&o->mu);
	size_t frame = pa_frame_size(&o->sample);
	*msec = frame == 0 || o->sample.rate == 0 ? 0 : (SLuint32)((o->bytes_transferred / frame) * 1000u / o->sample.rate);
	pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}
static SLresult play_get_position(SLPlayItf self, SLuint32 *msec) { return stream_get_position(from_play(self), msec); }
static SLresult play_register_callback(SLPlayItf self, slPlayCallback cb, void *ctx)
{
	tipsy_sl_object *o = from_play(self);
	o->play_cb = cb;
	o->play_ctx = ctx;
	return SL_RESULT_SUCCESS;
}
static SLresult play_set_mask(SLPlayItf self, SLuint32 mask) { from_play(self)->callback_mask = mask; return SL_RESULT_SUCCESS; }
static SLresult play_get_mask(SLPlayItf self, SLuint32 *mask) { if (!mask) return SL_RESULT_PARAMETER_INVALID; *mask = from_play(self)->callback_mask; return SL_RESULT_SUCCESS; }
static SLresult play_set_marker(SLPlayItf self, SLuint32 msec) { from_play(self)->marker = msec; return SL_RESULT_SUCCESS; }
static SLresult play_clear_marker(SLPlayItf self) { from_play(self)->marker = 0; return SL_RESULT_SUCCESS; }
static SLresult play_get_marker(SLPlayItf self, SLuint32 *msec) { if (!msec) return SL_RESULT_PARAMETER_INVALID; *msec = from_play(self)->marker; return SL_RESULT_SUCCESS; }
static SLresult play_set_period(SLPlayItf self, SLuint32 msec) { from_play(self)->update_period = msec; return SL_RESULT_SUCCESS; }
static SLresult play_get_period(SLPlayItf self, SLuint32 *msec) { if (!msec) return SL_RESULT_PARAMETER_INVALID; *msec = from_play(self)->update_period; return SL_RESULT_SUCCESS; }

static const struct SLPlayItf_ play_vtable = {
	play_set_state, play_get_state, play_get_duration, play_get_position, play_register_callback,
	play_set_mask, play_get_mask, play_set_marker, play_clear_marker, play_get_marker,
	play_set_period, play_get_period,
};

static SLresult record_set_state(SLRecordItf self, SLuint32 state) { return set_stream_state(from_record(self), state, 1); }
static SLresult record_get_state(SLRecordItf self, SLuint32 *state)
{
	if (!state) return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_record(self);
	pthread_mutex_lock(&o->mu); *state = o->state; pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}
static SLresult record_set_limit(SLRecordItf self, SLuint32 msec) { from_record(self)->duration_limit = msec; return SL_RESULT_SUCCESS; }
static SLresult record_get_position(SLRecordItf self, SLuint32 *msec) { return stream_get_position(from_record(self), msec); }
static SLresult record_register_callback(SLRecordItf self, slRecordCallback cb, void *ctx) { from_record(self)->record_cb = cb; from_record(self)->record_ctx = ctx; return SL_RESULT_SUCCESS; }
static SLresult record_set_mask(SLRecordItf self, SLuint32 mask) { from_record(self)->callback_mask = mask; return SL_RESULT_SUCCESS; }
static SLresult record_get_mask(SLRecordItf self, SLuint32 *mask) { if (!mask) return SL_RESULT_PARAMETER_INVALID; *mask = from_record(self)->callback_mask; return SL_RESULT_SUCCESS; }
static SLresult record_set_marker(SLRecordItf self, SLuint32 msec) { from_record(self)->marker = msec; return SL_RESULT_SUCCESS; }
static SLresult record_clear_marker(SLRecordItf self) { from_record(self)->marker = 0; return SL_RESULT_SUCCESS; }
static SLresult record_get_marker(SLRecordItf self, SLuint32 *msec) { if (!msec) return SL_RESULT_PARAMETER_INVALID; *msec = from_record(self)->marker; return SL_RESULT_SUCCESS; }
static SLresult record_set_period(SLRecordItf self, SLuint32 msec) { from_record(self)->update_period = msec; return SL_RESULT_SUCCESS; }
static SLresult record_get_period(SLRecordItf self, SLuint32 *msec) { if (!msec) return SL_RESULT_PARAMETER_INVALID; *msec = from_record(self)->update_period; return SL_RESULT_SUCCESS; }

static const struct SLRecordItf_ record_vtable = {
	record_set_state, record_get_state, record_set_limit, record_get_position, record_register_callback,
	record_set_mask, record_get_mask, record_set_marker, record_clear_marker, record_get_marker,
	record_set_period, record_get_period,
};

static SLresult queue_enqueue(SLBufferQueueItf self, const void *buffer, SLuint32 size)
{
	if (self == NULL || buffer == NULL || size == 0)
		return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_queue(self);
	queue_node *n = calloc(1, sizeof(*n));
	if (n == NULL)
		return SL_RESULT_MEMORY_FAILURE;
	n->io_buffer = malloc(size);
	if (n->io_buffer == NULL) { free(n); return SL_RESULT_MEMORY_FAILURE; }
	n->client_buffer = (void *)buffer;
	n->size = size;
	if (o->kind == OBJ_PLAYER)
		memcpy(n->io_buffer, buffer, size);
	pthread_mutex_lock(&o->mu);
	if (o->queue_count >= o->queue_capacity || o->destroying) {
		pthread_mutex_unlock(&o->mu);
		free_node(n);
		return SL_RESULT_BUFFER_INSUFFICIENT;
	}
	n->generation = o->queue_generation;
	if (o->tail != NULL)
		o->tail->next = n;
	else
		o->head = n;
	o->tail = n;
	o->queue_count++;
	pthread_cond_signal(&o->cond);
	pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}

static SLresult queue_clear(SLBufferQueueItf self)
{
	if (self == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_queue(self);
	pthread_mutex_lock(&o->mu);
	o->queue_generation++;
	free_pending_locked(o);
	o->queue_index = 0;
	pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}

static SLresult queue_get_state(SLBufferQueueItf self, SLBufferQueueState *state)
{
	if (self == NULL || state == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_queue(self);
	pthread_mutex_lock(&o->mu);
	state->count = o->queue_count;
	state->index = o->queue_index;
	pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}

static SLresult queue_register_callback(SLBufferQueueItf self, slBufferQueueCallback cb, void *ctx)
{
	if (self == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_queue(self);
	pthread_mutex_lock(&o->mu);
	o->queue_cb = cb;
	o->queue_ctx = ctx;
	pthread_mutex_unlock(&o->mu);
	return SL_RESULT_SUCCESS;
}

static const struct SLBufferQueueItf_ queue_vtable = {
	queue_enqueue, queue_clear, queue_get_state, queue_register_callback,
};

static SLresult volume_set_level(SLVolumeItf self, SLmillibel level) { from_volume(self)->volume = level; return SL_RESULT_SUCCESS; }
static SLresult volume_get_level(SLVolumeItf self, SLmillibel *level) { if (!level) return SL_RESULT_PARAMETER_INVALID; *level = from_volume(self)->volume; return SL_RESULT_SUCCESS; }
static SLresult volume_get_max(SLVolumeItf self, SLmillibel *level) { (void)self; if (!level) return SL_RESULT_PARAMETER_INVALID; *level = 0; return SL_RESULT_SUCCESS; }
static SLresult volume_set_mute(SLVolumeItf self, SLboolean mute) { from_volume(self)->mute = !!mute; return SL_RESULT_SUCCESS; }
static SLresult volume_get_mute(SLVolumeItf self, SLboolean *mute) { if (!mute) return SL_RESULT_PARAMETER_INVALID; *mute = from_volume(self)->mute; return SL_RESULT_SUCCESS; }
static SLresult volume_enable_stereo(SLVolumeItf self, SLboolean enabled) { from_volume(self)->stereo_enabled = !!enabled; return SL_RESULT_SUCCESS; }
static SLresult volume_stereo_enabled(SLVolumeItf self, SLboolean *enabled) { if (!enabled) return SL_RESULT_PARAMETER_INVALID; *enabled = from_volume(self)->stereo_enabled; return SL_RESULT_SUCCESS; }
static SLresult volume_set_stereo(SLVolumeItf self, SLpermille pos) { from_volume(self)->stereo_position = pos; return SL_RESULT_SUCCESS; }
static SLresult volume_get_stereo(SLVolumeItf self, SLpermille *pos) { if (!pos) return SL_RESULT_PARAMETER_INVALID; *pos = from_volume(self)->stereo_position; return SL_RESULT_SUCCESS; }

static const struct SLVolumeItf_ volume_vtable = {
	volume_set_level, volume_get_level, volume_get_max, volume_set_mute, volume_get_mute,
	volume_enable_stereo, volume_stereo_enabled, volume_set_stereo, volume_get_stereo,
};

static SLresult config_set(SLAndroidConfigurationItf self, const uint8_t *key, const void *value, SLuint32 size)
{
	if (self == NULL || key == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	tipsy_sl_object *o = from_config(self);
	if (value == NULL || size < sizeof(SLint32))
		return SL_RESULT_SUCCESS;
	SLint32 v;
	memcpy(&v, value, sizeof(v));
	char detail[160];
	int known = 1;
	pthread_mutex_lock(&o->mu);
	if (o->kind == OBJ_PLAYER && strcmp((const char *)key, SL_ANDROID_KEY_STREAM_TYPE) == 0)
		o->voice_stream = v == SL_ANDROID_STREAM_VOICE;
	else if (o->kind == OBJ_RECORDER && strcmp((const char *)key, SL_ANDROID_KEY_RECORDING_PRESET) == 0)
		o->voice_stream = v == SL_ANDROID_RECORDING_PRESET_VOICE_COMMUNICATION;
	else if (strcmp((const char *)key, SL_ANDROID_KEY_PERFORMANCE_MODE) != 0)
		known = 0;
	/* androidPerformanceMode is accepted and has no host equivalent: the
	 * Pulse target is already the bounded 50 ms path for every player. */
	pthread_mutex_unlock(&o->mu);
	snprintf(detail, sizeof(detail), "%s SetConfiguration(%.48s=%d): %s", kind_name(o->kind),
	         (const char *)key, (int)v, known ? "accepted" : "unknown key, PARAMETER_INVALID");
	log_once("OpenSL configuration", detail);
	/* Android (frameworks/wilhelm IAndroidConfiguration) rejects keys it
	 * does not know rather than pretending to apply them. */
	return known ? SL_RESULT_SUCCESS : SL_RESULT_PARAMETER_INVALID;
}
static SLresult config_get(SLAndroidConfigurationItf self, const uint8_t *key, SLuint32 *size, void *value)
{
	(void)value;
	if (self == NULL || size == NULL) return SL_RESULT_PARAMETER_INVALID;
	char detail[160];
	snprintf(detail, sizeof(detail), "%s GetConfiguration(%.48s): FEATURE_UNSUPPORTED",
	         kind_name(from_config(self)->kind), key != NULL ? (const char *)key : "(null)");
	log_once("OpenSL configuration", detail);
	*size = 0;
	return SL_RESULT_FEATURE_UNSUPPORTED;
}
static SLresult config_acquire(SLAndroidConfigurationItf self, SLuint32 type, void **proxy) { (void)self; (void)type; if (proxy) *proxy = NULL; return SL_RESULT_FEATURE_UNSUPPORTED; }
static SLresult config_release(SLAndroidConfigurationItf self, SLuint32 type) { (void)self; (void)type; return SL_RESULT_FEATURE_UNSUPPORTED; }

static const struct SLAndroidConfigurationItf_ config_vtable = {
	config_set, config_get, config_acquire, config_release,
};

/* Android's IOutputMix_GetDestinationOutputDeviceIDs (frameworks/wilhelm)
 * reports exactly one destination, SL_DEFAULTDEVICEID_AUDIOOUTPUT; a NULL id
 * array is the "how many" query. The bridge has one host sink too. */
static SLresult mix_get_devices(SLOutputMixItf self, SLint32 *count, SLuint32 *ids)
{
	(void)self;
	if (count == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	log_once("OpenSL OutputMix", ids == NULL ? "GetDestinationOutputDeviceIDs count query: 1 device"
	                                         : "GetDestinationOutputDeviceIDs: default audio output");
	if (ids != NULL) {
		if (*count < 1)
			return SL_RESULT_BUFFER_INSUFFICIENT;
		ids[0] = SL_DEFAULTDEVICEID_AUDIOOUTPUT;
	}
	*count = 1;
	return SL_RESULT_SUCCESS;
}
static SLresult mix_register(SLOutputMixItf self, slMixDeviceChangeCallback cb, void *ctx) { from_mix(self)->mix_cb = cb; from_mix(self)->mix_ctx = ctx; return SL_RESULT_SUCCESS; }
static SLresult mix_reroute(SLOutputMixItf self, SLint32 count, SLuint32 *ids)
{
	(void)self; (void)count; (void)ids;
	log_once("OpenSL OutputMix", "ReRoute: FEATURE_UNSUPPORTED (Android answers the same)");
	return SL_RESULT_FEATURE_UNSUPPORTED;
}

static const struct SLOutputMixItf_ mix_vtable = {mix_get_devices, mix_register, mix_reroute};

/* Describe a client PCM descriptor for the create diagnostics. Only the
 * layout fields are printed, never sample data. */
static void format_describe(const void *format, char *out, size_t cap)
{
	if (format == NULL) {
		snprintf(out, cap, "format=NULL");
		return;
	}
	const TipsyPCMFormat *pcm = format;
	if (pcm->formatType != SL_DATAFORMAT_PCM && pcm->formatType != SL_ANDROID_DATAFORMAT_PCM_EX) {
		snprintf(out, cap, "formatType=%u", pcm->formatType);
		return;
	}
	snprintf(out, cap, "%s ch=%u rate=%u.%03u bits=%u container=%u mask=0x%x %s%s", pcm->formatType == SL_DATAFORMAT_PCM ? "PCM" : "PCM_EX",
	         pcm->numChannels, pcm->samplesPerSec / 1000u, pcm->samplesPerSec % 1000u, pcm->bitsPerSample,
	         pcm->containerSize, pcm->channelMask, pcm->endianness == SL_BYTEORDER_BIGENDIAN ? "BE" : "LE",
	         pcm->formatType == SL_ANDROID_DATAFORMAT_PCM_EX
	             ? (pcm->representation == SL_ANDROID_PCM_REPRESENTATION_FLOAT ? " float" : (pcm->representation == SL_ANDROID_PCM_REPRESENTATION_UNSIGNED_INT ? " unsigned" : " signed"))
	             : "");
}

static const char *locator_name(SLuint32 type)
{
	switch (type) {
	case SL_DATALOCATOR_OUTPUTMIX:
		return "OUTPUTMIX";
	case SL_DATALOCATOR_BUFFERQUEUE:
		return "BUFFERQUEUE";
	case SL_DATALOCATOR_IODEVICE:
		return "IODEVICE";
	case SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE:
		return "ANDROIDSIMPLEBUFFERQUEUE";
	}
	return "other";
}

static SLresult create_stream_object(object_kind kind, SLObjectItf *out, SLDataSource *source,
	                                 SLDataSink *sink, SLuint32 count,
	                                 const SLInterfaceID *ids, const SLboolean *required)
{
	char detail[288], fmt_text[160];
	if (out == NULL || source == NULL || sink == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*out = NULL;
	if (!required_interfaces_supported(kind, count, ids, required)) {
		snprintf(detail, sizeof(detail), "%s create refused: required interface not offered", kind_name(kind));
		log_once("OpenSL create failed", detail);
		return SL_RESULT_FEATURE_UNSUPPORTED;
	}
	const void *format = kind == OBJ_PLAYER ? source->pFormat : sink->pFormat;
	const void *locator = kind == OBJ_PLAYER ? source->pLocator : sink->pLocator;
	const SLuint32 *other_locator = kind == OBJ_PLAYER ? sink->pLocator : source->pLocator;
	SLuint32 other_type = other_locator != NULL ? other_locator[0] : 0;
	if (locator == NULL) {
		snprintf(detail, sizeof(detail), "%s create refused: PCM-side locator NULL", kind_name(kind));
		log_once("OpenSL create failed", detail);
		return SL_RESULT_PARAMETER_INVALID;
	}
	const SLDataLocator_BufferQueue *bq = locator;
	format_describe(format, fmt_text, sizeof(fmt_text));
	if (bq->locatorType != SL_DATALOCATOR_BUFFERQUEUE &&
	    bq->locatorType != SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE) {
		snprintf(detail, sizeof(detail), "%s create refused: locator %s (0x%x) is not a buffer queue; %s", kind_name(kind),
		         locator_name(bq->locatorType), bq->locatorType, fmt_text);
		log_once("OpenSL create failed", detail);
		return SL_RESULT_FEATURE_UNSUPPORTED;
	}
	pa_sample_spec sample;
	if (format_from(format, &sample) < 0) {
		snprintf(detail, sizeof(detail), "%s create refused: PCM layout unsupported (%s)", kind_name(kind), fmt_text);
		log_once("OpenSL create failed", detail);
		return SL_RESULT_CONTENT_UNSUPPORTED;
	}
	if (kind == OBJ_RECORDER && other_locator != NULL && other_type == SL_DATALOCATOR_IODEVICE) {
		/* SLDataLocator_IODevice {locatorType, deviceType, deviceID, device} */
		snprintf(detail, sizeof(detail), "recorder: IODEVICE type=%u id=0x%x, %u buffers, %s", other_locator[1],
		         other_locator[2], bq->numBuffers, fmt_text);
	} else {
		snprintf(detail, sizeof(detail), "%s: %s sink/source %s, %u buffers, %s", kind_name(kind),
		         locator_name(other_type), locator_name(bq->locatorType), bq->numBuffers, fmt_text);
	}
	log_once("OpenSL object created", detail);
	tipsy_sl_object *o = object_new(kind);
	if (o == NULL)
		return SL_RESULT_MEMORY_FAILURE;
	o->play_vt = &play_vtable;
	o->record_vt = &record_vtable;
	o->queue_vt = &queue_vtable;
	o->volume_vt = &volume_vtable;
	o->config_vt = &config_vtable;
	o->sample = sample;
	o->queue_capacity = bq->numBuffers > 0 && bq->numBuffers <= 64 ? bq->numBuffers : 4;
	if (pthread_create(&o->thread, NULL, stream_worker, o) != 0) {
		pthread_cond_destroy(&o->cond);
		pthread_mutex_destroy(&o->mu);
		free(o);
		return SL_RESULT_RESOURCE_ERROR;
	}
	o->thread_started = 1;
	*out = (SLObjectItf)&o->object_vt;
	return SL_RESULT_SUCCESS;
}

static SLresult engine_create_player(SLEngineItf self, SLObjectItf *out, SLDataSource *source,
	                                 SLDataSink *sink, SLuint32 count,
	                                 const SLInterfaceID *ids, const SLboolean *required)
{
	(void)self;
	return create_stream_object(OBJ_PLAYER, out, source, sink, count, ids, required);
}

static SLresult engine_create_recorder(SLEngineItf self, SLObjectItf *out, SLDataSource *source,
	                                   SLDataSink *sink, SLuint32 count,
	                                   const SLInterfaceID *ids, const SLboolean *required)
{
	(void)self;
	return create_stream_object(OBJ_RECORDER, out, source, sink, count, ids, required);
}

static SLresult engine_create_mix(SLEngineItf self, SLObjectItf *out, SLuint32 count,
	                              const SLInterfaceID *ids, const SLboolean *required)
{
	(void)self;
	if (out == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*out = NULL;
	if (!required_interfaces_supported(OBJ_OUTPUT_MIX, count, ids, required))
		return SL_RESULT_FEATURE_UNSUPPORTED;
	tipsy_sl_object *o = object_new(OBJ_OUTPUT_MIX);
	if (o == NULL)
		return SL_RESULT_MEMORY_FAILURE;
	o->mix_vt = &mix_vtable;
	*out = (SLObjectItf)&o->object_vt;
	return SL_RESULT_SUCCESS;
}

/* SLEngineItf::QueryNumSupportedInterfaces / QuerySupportedInterfaces answer
 * for the four object classes the bridge can create, exactly the set
 * GetInterface hands out. Other object IDs (LED, vibra, MIDI, listener, 3D
 * group, metadata extractor) are FEATURE_UNSUPPORTED like on Android, where
 * those classes are not implemented either. */
static SLresult engine_query_count(SLEngineItf self, SLuint32 object, SLuint32 *count)
{
	(void)self;
	if (count == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	object_kind kind;
	char detail[96];
	if (!object_kind_from_id(object, &kind)) {
		snprintf(detail, sizeof(detail), "QueryNumSupportedInterfaces(objectID=0x%x): FEATURE_UNSUPPORTED", object);
		log_once("OpenSL engine query", detail);
		*count = 0;
		return SL_RESULT_FEATURE_UNSUPPORTED;
	}
	*count = supported_iids(kind, NULL, 0);
	snprintf(detail, sizeof(detail), "QueryNumSupportedInterfaces(%s): %u", kind_name(kind), *count);
	log_once("OpenSL engine query", detail);
	return SL_RESULT_SUCCESS;
}

static SLresult engine_query_iid(SLEngineItf self, SLuint32 object, SLuint32 index, SLInterfaceID *iid)
{
	(void)self;
	if (iid == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*iid = NULL;
	object_kind kind;
	if (!object_kind_from_id(object, &kind))
		return SL_RESULT_FEATURE_UNSUPPORTED;
	SLInterfaceID list[8];
	SLuint32 n = supported_iids(kind, list, 8);
	if (index >= n)
		return SL_RESULT_PARAMETER_INVALID;
	*iid = list[index];
	return SL_RESULT_SUCCESS;
}

static SLresult engine_extension_count(SLEngineItf self, SLuint32 *count)
{
	(void)self;
	if (count == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	log_once("OpenSL engine query", "QueryNumSupportedExtensions: 0");
	*count = 0;
	return SL_RESULT_SUCCESS;
}

static SLresult engine_extension(SLEngineItf self, SLuint32 index, uint8_t *name, SLint16 *len)
{
	(void)self; (void)index; (void)name;
	if (len != NULL)
		*len = 0;
	log_once("OpenSL engine query", "QuerySupportedExtension: none");
	return SL_RESULT_PARAMETER_INVALID;
}

static SLresult engine_extension_supported(SLEngineItf self, const uint8_t *name, SLboolean *supported)
{
	(void)self;
	if (supported == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	char detail[128];
	snprintf(detail, sizeof(detail), "IsExtensionSupported(%.64s): false", name != NULL ? (const char *)name : "(null)");
	log_once("OpenSL engine query", detail);
	*supported = 0;
	return SL_RESULT_SUCCESS;
}

static const struct SLEngineItf_ engine_vtable = {
	NULL, NULL, engine_create_player, engine_create_recorder, NULL, NULL, NULL, engine_create_mix,
	NULL, NULL, engine_query_count, engine_query_iid, engine_extension_count, engine_extension,
	engine_extension_supported,
};

uint32_t tipsy_slCreateEngine(void *pEngine, uint32_t numOptions, const void *pEngineOptions,
	                          uint32_t numInterfaces, const void *pInterfaceIds,
	                          const void *pInterfaceRequired)
{
	if (pEngine == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*(SLObjectItf *)pEngine = NULL;
	if (!required_interfaces_supported(OBJ_ENGINE, numInterfaces, pInterfaceIds, pInterfaceRequired)) {
		log_once("OpenSL create failed", "engine create refused: required interface not offered");
		return SL_RESULT_FEATURE_UNSUPPORTED;
	}
	tipsy_sl_object *o = object_new(OBJ_ENGINE);
	if (o == NULL)
		return SL_RESULT_MEMORY_FAILURE;
	o->engine_vt = &engine_vtable;
	*(SLObjectItf *)pEngine = (SLObjectItf)&o->object_vt;
	/* SLEngineOption pairs {feature, data}; THREADSAFE is the one Android
	 * honours. Which client created the engine (FMOD's OpenSL output vs
	 * WebRTC's legacy ADM) shows in this shape, so record it. */
	int threadsafe = 0;
	const SLuint32 *opts = pEngineOptions;
	for (uint32_t i = 0; opts != NULL && i < numOptions; i++) {
		if (opts[2 * i] == SL_ENGINEOPTION_THREADSAFE && opts[2 * i + 1] != 0)
			threadsafe = 1;
	}
	char detail[128];
	snprintf(detail, sizeof(detail), "host bridge ready; stream opens lazily; options=%u threadsafe=%d interfaces=%u",
	         numOptions, threadsafe, numInterfaces);
	audio_log("OpenSL ES engine created", detail);
	return SL_RESULT_SUCCESS;
}

typedef struct {
	pthread_mutex_t mu;
	pthread_cond_t cond;
	uint32_t count;
} test_callback_state;

static void test_queue_callback(SLBufferQueueItf queue, void *context)
{
	(void)queue;
	test_callback_state *state = context;
	pthread_mutex_lock(&state->mu);
	state->count++;
	pthread_cond_signal(&state->cond);
	pthread_mutex_unlock(&state->mu);
}

static int test_wait_count(test_callback_state *state, uint32_t want, int timeout_ms)
{
	struct timespec until;
	if (timeout_ms <= 0)
		timeout_ms = 2000;
	clock_gettime(CLOCK_REALTIME, &until);
	until.tv_sec += timeout_ms / 1000;
	until.tv_nsec += (long)(timeout_ms % 1000) * 1000000L;
	if (until.tv_nsec >= 1000000000L) {
		until.tv_sec++;
		until.tv_nsec -= 1000000000L;
	}
	pthread_mutex_lock(&state->mu);
	while (state->count < want) {
		if (pthread_cond_timedwait(&state->cond, &state->mu, &until) == ETIMEDOUT)
			break;
	}
	int ok = state->count >= want;
	pthread_mutex_unlock(&state->mu);
	return ok;
}

static void fake_reset(int fail_first_write, int fail_first_read)
{
	tipsy_audio_set_capture_muted(0);
	pthread_mutex_lock(&capture_gate_mu);
	capture_denied_logged = 0;
	capture_error_logged = 0;
	pthread_mutex_unlock(&capture_gate_mu);
	pthread_mutex_lock(&fake_backend.mu);
	fake_backend.enabled = 1;
	fake_backend.fail_first_write = fail_first_write;
	fake_backend.fail_first_read = fail_first_read;
	fake_backend.opens = fake_backend.writes = fake_backend.reads = 0;
	fake_backend.written_bytes = fake_backend.read_bytes = 0;
	pthread_mutex_unlock(&fake_backend.mu);
}

static void fake_disable(void)
{
	pthread_mutex_lock(&fake_backend.mu);
	fake_backend.enabled = 0;
	pthread_mutex_unlock(&fake_backend.mu);
}

static int buffer_has_nonzero(const uint8_t *buffer, uint32_t bytes)
{
	if (buffer == NULL)
		return 0;
	for (uint32_t i = 0; i < bytes; i++) {
		if (buffer[i] != 0)
			return 1;
	}
	return 0;
}

/* When set, test_stream tags the stream the way WebRTC does (voice stream
 * type on a player, voice-communication preset on a recorder) before Realize,
 * so the host probes can open the voice-sized Pulse buffer for real. */
static int test_voice_tag;

static int test_apply_voice_tag(SLObjectItf stream, int recording)
{
	if (!test_voice_tag)
		return 0;
	SLAndroidConfigurationItf config = NULL;
	if ((*stream)->GetInterface(stream, SL_IID_ANDROIDCONFIGURATION, &config) != 0)
		return -1;
	SLint32 value = recording ? SL_ANDROID_RECORDING_PRESET_VOICE_COMMUNICATION : SL_ANDROID_STREAM_VOICE;
	const char *key = recording ? SL_ANDROID_KEY_RECORDING_PRESET : SL_ANDROID_KEY_STREAM_TYPE;
	return (*config)->SetConfiguration(config, (const uint8_t *)key, &value, sizeof(value)) == 0 ? 0 : -1;
}

static int test_stream(int recording, uint32_t rate, uint32_t channels, uint32_t bytes,
	                   uint64_t *transferred, uint32_t *callbacks, int *had_nonzero, int timeout_ms)
{
	SLObjectItf engine = NULL, mix = NULL, stream = NULL;
	SLEngineItf engine_itf = NULL;
	SLBufferQueueItf queue = NULL;
	SLPlayItf play = NULL;
	SLRecordItf record = NULL;
	uint8_t *buffer = calloc(1, bytes);
	if (buffer == NULL)
		return -1;
	if (had_nonzero)
		*had_nonzero = 1;
	TipsyPCMFormat pcm = {SL_DATAFORMAT_PCM, channels, rate * 1000u, 16, 16, 0, SL_BYTEORDER_LITTLEENDIAN, 0};
	SLDataLocator_BufferQueue bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 2};
	SLuint32 output_locator[4] = {SL_DATALOCATOR_OUTPUTMIX, 0, 0, 0};
	SLuint32 input_locator[4] = {SL_DATALOCATOR_IODEVICE, 1, 0xffffffffu, 0};
	SLDataSource source = recording ? (SLDataSource){input_locator, NULL} : (SLDataSource){&bq, &pcm};
	SLDataSink sink = recording ? (SLDataSink){&bq, &pcm} : (SLDataSink){output_locator, NULL};
	test_callback_state state = {PTHREAD_MUTEX_INITIALIZER, PTHREAD_COND_INITIALIZER, 0};
	int ok = 0;
	uint64_t completed_bytes = 0;
	if (tipsy_slCreateEngine(&engine, 0, NULL, 0, NULL, NULL) != 0 || engine == NULL) goto done;
	if ((*engine)->Realize(engine, 0) != 0 || (*engine)->GetInterface(engine, SL_IID_ENGINE, &engine_itf) != 0) goto done;
	if ((*engine_itf)->CreateOutputMix(engine_itf, &mix, 0, NULL, NULL) != 0) goto done;
	if (recording) {
		if ((*engine_itf)->CreateAudioRecorder(engine_itf, &stream, &source, &sink, 0, NULL, NULL) != 0) goto done;
	} else if ((*engine_itf)->CreateAudioPlayer(engine_itf, &stream, &source, &sink, 0, NULL, NULL) != 0) goto done;
	if (test_apply_voice_tag(stream, recording) != 0) goto done;
	if ((*stream)->Realize(stream, 0) != 0 || (*stream)->GetInterface(stream, SL_IID_ANDROIDSIMPLEBUFFERQUEUE, &queue) != 0) goto done;
	if ((*queue)->RegisterCallback(queue, test_queue_callback, &state) != 0) goto done;
	if (recording) {
		if ((*stream)->GetInterface(stream, SL_IID_RECORD, &record) != 0 || (*record)->SetRecordState(record, SL_RECORDSTATE_RECORDING) != 0) goto done;
	} else if ((*stream)->GetInterface(stream, SL_IID_PLAY, &play) != 0 || (*play)->SetPlayState(play, SL_PLAYSTATE_PLAYING) != 0) goto done;
	if ((*queue)->Enqueue(queue, buffer, bytes) != 0 || !test_wait_count(&state, 1, timeout_ms)) goto done;
	ok = 1;
done:
	if (had_nonzero)
		*had_nonzero = buffer_has_nonzero(buffer, bytes);
	if (stream != NULL) {
		tipsy_sl_object *stream_object = from_object(stream);
		pthread_mutex_lock(&stream_object->mu);
		completed_bytes = stream_object->bytes_transferred;
		pthread_mutex_unlock(&stream_object->mu);
	}
	if (stream) (*stream)->Destroy(stream);
	if (mix) (*mix)->Destroy(mix);
	if (engine) (*engine)->Destroy(engine);
	pthread_mutex_lock(&fake_backend.mu);
	if (transferred) *transferred = fake_backend.enabled
	                                    ? (recording ? fake_backend.read_bytes : fake_backend.written_bytes)
	                                    : completed_bytes;
	if (callbacks) *callbacks = state.count;
	pthread_mutex_unlock(&fake_backend.mu);
	pthread_cond_destroy(&state.cond);
	pthread_mutex_destroy(&state.mu);
	free(buffer);
	return ok ? 0 : -1;
}

static int test_duplex(uint32_t rate, uint32_t channels, uint32_t bytes,
	                   uint64_t *written, uint32_t *play_callbacks,
	                   uint64_t *read_bytes, uint32_t *capture_callbacks, int timeout_ms)
{
	SLObjectItf engine = NULL, mix = NULL, player = NULL, recorder = NULL;
	SLEngineItf engine_itf = NULL;
	SLBufferQueueItf play_queue = NULL, cap_queue = NULL;
	SLPlayItf play = NULL;
	SLRecordItf record = NULL;
	uint8_t *play_buffer = calloc(1, bytes);
	uint8_t *cap_buffer = calloc(1, bytes);
	if (play_buffer == NULL || cap_buffer == NULL) {
		free(play_buffer);
		free(cap_buffer);
		return -1;
	}
	TipsyPCMFormat pcm = {SL_DATAFORMAT_PCM, channels, rate * 1000u, 16, 16, 0, SL_BYTEORDER_LITTLEENDIAN, 0};
	SLDataLocator_BufferQueue play_bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 2};
	SLDataLocator_BufferQueue cap_bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 2};
	SLuint32 output_locator[4] = {SL_DATALOCATOR_OUTPUTMIX, 0, 0, 0};
	SLuint32 input_locator[4] = {SL_DATALOCATOR_IODEVICE, 1, 0xffffffffu, 0};
	SLDataSource play_source = {&play_bq, &pcm};
	SLDataSink play_sink = {output_locator, NULL};
	SLDataSource cap_source = {input_locator, NULL};
	SLDataSink cap_sink = {&cap_bq, &pcm};
	test_callback_state play_state = {PTHREAD_MUTEX_INITIALIZER, PTHREAD_COND_INITIALIZER, 0};
	test_callback_state cap_state = {PTHREAD_MUTEX_INITIALIZER, PTHREAD_COND_INITIALIZER, 0};
	int ok = 0;
	uint64_t play_completed = 0, cap_completed = 0;
	if (tipsy_slCreateEngine(&engine, 0, NULL, 0, NULL, NULL) != 0 || engine == NULL) goto done;
	if ((*engine)->Realize(engine, 0) != 0 || (*engine)->GetInterface(engine, SL_IID_ENGINE, &engine_itf) != 0) goto done;
	if ((*engine_itf)->CreateOutputMix(engine_itf, &mix, 0, NULL, NULL) != 0) goto done;
	if ((*engine_itf)->CreateAudioPlayer(engine_itf, &player, &play_source, &play_sink, 0, NULL, NULL) != 0) goto done;
	if ((*engine_itf)->CreateAudioRecorder(engine_itf, &recorder, &cap_source, &cap_sink, 0, NULL, NULL) != 0) goto done;
	if ((*player)->Realize(player, 0) != 0 || (*player)->GetInterface(player, SL_IID_ANDROIDSIMPLEBUFFERQUEUE, &play_queue) != 0) goto done;
	if ((*recorder)->Realize(recorder, 0) != 0 || (*recorder)->GetInterface(recorder, SL_IID_ANDROIDSIMPLEBUFFERQUEUE, &cap_queue) != 0) goto done;
	if ((*play_queue)->RegisterCallback(play_queue, test_queue_callback, &play_state) != 0) goto done;
	if ((*cap_queue)->RegisterCallback(cap_queue, test_queue_callback, &cap_state) != 0) goto done;
	if ((*player)->GetInterface(player, SL_IID_PLAY, &play) != 0 || (*play)->SetPlayState(play, SL_PLAYSTATE_PLAYING) != 0) goto done;
	if ((*recorder)->GetInterface(recorder, SL_IID_RECORD, &record) != 0 || (*record)->SetRecordState(record, SL_RECORDSTATE_RECORDING) != 0) goto done;
	if ((*play_queue)->Enqueue(play_queue, play_buffer, bytes) != 0) goto done;
	if ((*cap_queue)->Enqueue(cap_queue, cap_buffer, bytes) != 0) goto done;
	if (!test_wait_count(&play_state, 1, timeout_ms) || !test_wait_count(&cap_state, 1, timeout_ms)) goto done;
	ok = 1;
done:
	if (player != NULL) {
		tipsy_sl_object *o = from_object(player);
		pthread_mutex_lock(&o->mu);
		play_completed = o->bytes_transferred;
		pthread_mutex_unlock(&o->mu);
	}
	if (recorder != NULL) {
		tipsy_sl_object *o = from_object(recorder);
		pthread_mutex_lock(&o->mu);
		cap_completed = o->bytes_transferred;
		pthread_mutex_unlock(&o->mu);
	}
	if (player) (*player)->Destroy(player);
	if (recorder) (*recorder)->Destroy(recorder);
	if (mix) (*mix)->Destroy(mix);
	if (engine) (*engine)->Destroy(engine);
	pthread_mutex_lock(&fake_backend.mu);
	if (written) *written = fake_backend.enabled ? fake_backend.written_bytes : play_completed;
	if (read_bytes) *read_bytes = fake_backend.enabled ? fake_backend.read_bytes : cap_completed;
	pthread_mutex_unlock(&fake_backend.mu);
	if (play_callbacks) *play_callbacks = play_state.count;
	if (capture_callbacks) *capture_callbacks = cap_state.count;
	pthread_cond_destroy(&play_state.cond);
	pthread_mutex_destroy(&play_state.mu);
	pthread_cond_destroy(&cap_state.cond);
	pthread_mutex_destroy(&cap_state.mu);
	free(play_buffer);
	free(cap_buffer);
	return ok ? 0 : -1;
}

int tipsy_audio_test_playback(uint32_t rate, uint32_t channels, uint32_t bytes,
	                          uint64_t *written, uint32_t *callbacks)
{
	fake_reset(0, 0);
	int rc = test_stream(0, rate, channels, bytes, written, callbacks, NULL, 2000);
	fake_disable();
	return rc;
}

int tipsy_audio_test_capture(uint32_t rate, uint32_t channels, uint32_t bytes,
	                         uint64_t *read_bytes, uint32_t *callbacks)
{
	fake_reset(0, 0);
	int rc = test_stream(1, rate, channels, bytes, read_bytes, callbacks, NULL, 2000);
	fake_disable();
	return rc;
}

int tipsy_audio_test_invalid_format(void)
{
	fake_reset(0, 0);
	SLObjectItf engine = NULL, player = NULL;
	SLEngineItf engine_itf = NULL;
	TipsyPCMFormat pcm = {SL_DATAFORMAT_PCM, 2, 48000000u, 20, 20, 0, SL_BYTEORDER_LITTLEENDIAN, 0};
	SLDataLocator_BufferQueue bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 2};
	SLuint32 output_locator[4] = {SL_DATALOCATOR_OUTPUTMIX, 0, 0, 0};
	SLDataSource source = {&bq, &pcm};
	SLDataSink sink = {output_locator, NULL};
	int ok = tipsy_slCreateEngine(&engine, 0, NULL, 0, NULL, NULL) == 0 &&
	         (*engine)->GetInterface(engine, SL_IID_ENGINE, &engine_itf) == 0 &&
	         (*engine_itf)->CreateAudioPlayer(engine_itf, &player, &source, &sink, 0, NULL, NULL) != 0;
	if (player) (*player)->Destroy(player);
	if (engine) (*engine)->Destroy(engine);
	fake_disable();
	return ok ? 0 : -1;
}

int tipsy_audio_test_retry(uint32_t *opens, uint32_t *writes, uint32_t *callbacks)
{
	fake_reset(1, 0);
	uint64_t transferred = 0;
	int rc = test_stream(0, 48000, 2, 1920, &transferred, callbacks, NULL, 2000);
	pthread_mutex_lock(&fake_backend.mu);
	if (opens) *opens = fake_backend.opens;
	if (writes) *writes = fake_backend.writes;
	pthread_mutex_unlock(&fake_backend.mu);
	fake_disable();
	return rc == 0 && transferred == 1920 ? 0 : -1;
}

int tipsy_audio_test_capture_retry(uint32_t *opens, uint32_t *reads, uint32_t *callbacks)
{
	fake_reset(0, 1);
	uint64_t transferred = 0;
	int rc = test_stream(1, 48000, 1, 960, &transferred, callbacks, NULL, 2000);
	pthread_mutex_lock(&fake_backend.mu);
	if (opens) *opens = fake_backend.opens;
	if (reads) *reads = fake_backend.reads;
	pthread_mutex_unlock(&fake_backend.mu);
	fake_disable();
	return rc == 0 && transferred == 960 ? 0 : -1;
}

int tipsy_audio_test_host_playback(uint32_t rate, uint32_t channels, uint32_t bytes,
	                               uint64_t *written, uint32_t *callbacks)
{
	fake_disable();
	tipsy_audio_set_capture_muted(0);
	return test_stream(0, rate, channels, bytes, written, callbacks, NULL, 10000);
}

int tipsy_audio_test_host_capture(uint32_t rate, uint32_t channels, uint32_t bytes,
	                              uint64_t *read_bytes, uint32_t *callbacks)
{
	fake_disable();
	tipsy_audio_set_capture_muted(0);
	return test_stream(1, rate, channels, bytes, read_bytes, callbacks, NULL, 10000);
}

int tipsy_audio_test_host_voice_playback(uint32_t rate, uint32_t channels, uint32_t bytes,
	                                     uint64_t *written, uint32_t *callbacks)
{
	fake_disable();
	test_voice_tag = 1;
	int rc = test_stream(0, rate, channels, bytes, written, callbacks, NULL, 10000);
	test_voice_tag = 0;
	return rc;
}

int tipsy_audio_test_duplex(uint32_t rate, uint32_t channels, uint32_t bytes,
	                        uint64_t *written, uint32_t *play_callbacks,
	                        uint64_t *read_bytes, uint32_t *capture_callbacks)
{
	fake_reset(0, 0);
	int rc = test_duplex(rate, channels, bytes, written, play_callbacks, read_bytes, capture_callbacks, 2000);
	fake_disable();
	return rc;
}

int tipsy_audio_test_host_duplex(uint32_t rate, uint32_t channels, uint32_t bytes,
	                             uint64_t *written, uint32_t *play_callbacks,
	                             uint64_t *read_bytes, uint32_t *capture_callbacks)
{
	fake_disable();
	tipsy_audio_set_capture_muted(0);
	return test_duplex(rate, channels, bytes, written, play_callbacks, read_bytes, capture_callbacks, 10000);
}

int tipsy_audio_test_capture_muted(uint64_t *read_bytes, uint32_t *callbacks, int *had_nonzero)
{
	fake_reset(0, 0);
	tipsy_audio_set_capture_muted(1);
	int nz = 1;
	int rc = test_stream(1, 48000, 1, 960, read_bytes, callbacks, &nz, 2000);
	tipsy_audio_set_capture_muted(0);
	fake_disable();
	if (had_nonzero)
		*had_nonzero = nz;
	return rc;
}

int tipsy_audio_test_capture_refused(void)
{
	fake_reset(0, 0);
	uint64_t transferred = 0;
	uint32_t callbacks = 0;
	uint32_t opens = 0;
	int rc = test_stream(1, 48000, 1, 960, &transferred, &callbacks, NULL, 400);
	pthread_mutex_lock(&fake_backend.mu);
	opens = fake_backend.opens;
	pthread_mutex_unlock(&fake_backend.mu);
	fake_disable();
	return rc != 0 && callbacks == 0 && opens == 0 ? 0 : -1;
}

int tipsy_audio_test_capture_midstream_disable(uint32_t *callbacks, uint32_t *reads)
{
	fake_reset(0, 0);
	unsetenv("TIPSY_DISABLE_MICROPHONE");
	SLObjectItf engine = NULL, mix = NULL, stream = NULL;
	SLEngineItf engine_itf = NULL;
	SLBufferQueueItf queue = NULL;
	SLRecordItf record = NULL;
	const uint32_t bytes = 960;
	uint8_t *first = calloc(1, bytes);
	uint8_t *second = calloc(1, bytes);
	if (first == NULL || second == NULL) {
		free(first);
		free(second);
		fake_disable();
		return -1;
	}
	TipsyPCMFormat pcm = {SL_DATAFORMAT_PCM, 1, 48000000u, 16, 16, 0, SL_BYTEORDER_LITTLEENDIAN, 0};
	SLDataLocator_BufferQueue bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 4};
	SLuint32 input_locator[4] = {SL_DATALOCATOR_IODEVICE, 1, 0xffffffffu, 0};
	SLDataSource source = {input_locator, NULL};
	SLDataSink sink = {&bq, &pcm};
	test_callback_state state = {PTHREAD_MUTEX_INITIALIZER, PTHREAD_COND_INITIALIZER, 0};
	int ok = 0;
	uint32_t reads_after_first = 0;
	if (tipsy_slCreateEngine(&engine, 0, NULL, 0, NULL, NULL) != 0 || engine == NULL) goto done;
	if ((*engine)->Realize(engine, 0) != 0 || (*engine)->GetInterface(engine, SL_IID_ENGINE, &engine_itf) != 0) goto done;
	if ((*engine_itf)->CreateOutputMix(engine_itf, &mix, 0, NULL, NULL) != 0) goto done;
	if ((*engine_itf)->CreateAudioRecorder(engine_itf, &stream, &source, &sink, 0, NULL, NULL) != 0) goto done;
	if ((*stream)->Realize(stream, 0) != 0 || (*stream)->GetInterface(stream, SL_IID_ANDROIDSIMPLEBUFFERQUEUE, &queue) != 0) goto done;
	if ((*queue)->RegisterCallback(queue, test_queue_callback, &state) != 0) goto done;
	if ((*stream)->GetInterface(stream, SL_IID_RECORD, &record) != 0 || (*record)->SetRecordState(record, SL_RECORDSTATE_RECORDING) != 0) goto done;
	if ((*queue)->Enqueue(queue, first, bytes) != 0 || !test_wait_count(&state, 1, 2000)) goto done;
	pthread_mutex_lock(&fake_backend.mu);
	reads_after_first = fake_backend.reads;
	pthread_mutex_unlock(&fake_backend.mu);
	if (setenv("TIPSY_DISABLE_MICROPHONE", "1", 1) != 0) goto done;
	if ((*queue)->Enqueue(queue, second, bytes) != 0) goto done;
	struct timespec wait = {.tv_sec = 0, .tv_nsec = 350000000L};
	nanosleep(&wait, NULL);
	pthread_mutex_lock(&state.mu);
	uint32_t cb = state.count;
	pthread_mutex_unlock(&state.mu);
	pthread_mutex_lock(&fake_backend.mu);
	uint32_t reads_now = fake_backend.reads;
	pthread_mutex_unlock(&fake_backend.mu);
	ok = cb == 1 && reads_now == reads_after_first && !buffer_has_nonzero(second, bytes);
done:
	unsetenv("TIPSY_DISABLE_MICROPHONE");
	if (callbacks) {
		pthread_mutex_lock(&state.mu);
		*callbacks = state.count;
		pthread_mutex_unlock(&state.mu);
	}
	if (reads) {
		pthread_mutex_lock(&fake_backend.mu);
		*reads = fake_backend.reads;
		pthread_mutex_unlock(&fake_backend.mu);
	}
	if (stream) (*stream)->Destroy(stream);
	if (mix) (*mix)->Destroy(mix);
	if (engine) (*engine)->Destroy(engine);
	pthread_cond_destroy(&state.cond);
	pthread_mutex_destroy(&state.mu);
	free(first);
	free(second);
	fake_disable();
	return ok ? 0 : -1;
}

/* WebRTC legacy Android ADM shape, step for step from upstream
 * modules/audio_device/android/{audio_manager,opensles_player,opensles_recorder}.cc:
 * engine created with the THREADSAFE option and no interfaces; output mix with
 * no interfaces, realized; player = simple-buffer-queue source (2 buffers,
 * SLDataFormat_PCM 16-bit mono 48 kHz) + OutputMix sink, required
 * {ANDROIDCONFIGURATION, BUFFERQUEUE, VOLUME}, voice stream type set before
 * Realize, PLAY/BUFFERQUEUE/VOLUME interfaces, two buffers primed before
 * PLAYING and re-enqueued from the callback; recorder = IODevice source +
 * simple-buffer-queue sink, required {ANDROIDSIMPLEBUFFERQUEUE,
 * ANDROIDCONFIGURATION}, voice-communication preset before Realize,
 * RECORD/ANDROIDSIMPLEBUFFERQUEUE interfaces, count checked 0 then 2 around
 * the two enqueues, then RECORDING; stop = STOPPED then Clear (count and
 * index must read 0); destroy = RegisterCallback(NULL) then Destroy. */
typedef struct {
	test_callback_state state;
	SLBufferQueueItf queue;
	uint8_t *buffers[2];
	uint32_t bytes;
} webrtc_test_stream;

static void webrtc_test_callback(SLBufferQueueItf queue, void *context)
{
	(void)queue;
	webrtc_test_stream *s = context;
	pthread_mutex_lock(&s->state.mu);
	s->state.count++;
	uint32_t n = s->state.count;
	pthread_cond_signal(&s->state.cond);
	pthread_mutex_unlock(&s->state.mu);
	/* WebRTC re-enqueues the completed buffer from inside the callback.
	 * Stop after two re-enqueues so exactly four completions happen and the
	 * queue is provably empty when the stop sequence runs. */
	if (n < 3)
		(*s->queue)->Enqueue(s->queue, s->buffers[n % 2], s->bytes);
}

int tipsy_audio_test_webrtc_shape(uint32_t *play_callbacks, uint32_t *capture_callbacks,
	                              int *player_voice, int *recorder_voice,
	                              uint32_t *cleared_count, uint32_t *cleared_index)
{
	fake_reset(0, 0);
	const uint32_t bytes = 960; /* 480 frames x 1 ch x 16 bit: one WebRTC 10 ms block at 48 kHz */
	SLObjectItf engine = NULL, mix = NULL, player = NULL, recorder = NULL;
	SLEngineItf engine_itf = NULL;
	SLPlayItf play = NULL;
	SLRecordItf record = NULL;
	SLVolumeItf volume = NULL;
	SLAndroidConfigurationItf play_config = NULL, rec_config = NULL;
	webrtc_test_stream ps = {{PTHREAD_MUTEX_INITIALIZER, PTHREAD_COND_INITIALIZER, 0}, NULL, {NULL, NULL}, bytes};
	webrtc_test_stream rs = {{PTHREAD_MUTEX_INITIALIZER, PTHREAD_COND_INITIALIZER, 0}, NULL, {NULL, NULL}, bytes};
	SLuint32 option[2] = {SL_ENGINEOPTION_THREADSAFE, 1};
	TipsyPCMFormat pcm = {SL_DATAFORMAT_PCM, 1, 48000000u, 16, 16, SL_SPEAKER_FRONT_CENTER, SL_BYTEORDER_LITTLEENDIAN, 0};
	SLDataLocator_BufferQueue play_bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 2};
	SLDataLocator_BufferQueue rec_bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 2};
	struct { SLuint32 locatorType; SLObjectItf outputMix; } mix_locator = {SL_DATALOCATOR_OUTPUTMIX, NULL};
	SLuint32 mic_locator[4] = {SL_DATALOCATOR_IODEVICE, 1u /* SL_IODEVICE_AUDIOINPUT */, 0xffffffffu /* SL_DEFAULTDEVICEID_AUDIOINPUT */, 0};
	SLDataSource play_source = {&play_bq, &pcm};
	SLDataSink play_sink = {&mix_locator, NULL};
	SLDataSource rec_source = {mic_locator, NULL};
	SLDataSink rec_sink = {&rec_bq, &pcm};
	const SLInterfaceID play_ids[3] = {SL_IID_ANDROIDCONFIGURATION, SL_IID_BUFFERQUEUE, SL_IID_VOLUME};
	const SLboolean play_req[3] = {1, 1, 1};
	const SLInterfaceID rec_ids[2] = {SL_IID_ANDROIDSIMPLEBUFFERQUEUE, SL_IID_ANDROIDCONFIGURATION};
	const SLboolean rec_req[2] = {1, 1};
	SLint32 stream_type = SL_ANDROID_STREAM_VOICE;
	SLint32 preset = SL_ANDROID_RECORDING_PRESET_VOICE_COMMUNICATION;
	SLBufferQueueState qs = {0, 0};
	SLuint32 st = 0;
	int ok = 0;
	for (int i = 0; i < 2; i++) {
		ps.buffers[i] = calloc(1, bytes);
		rs.buffers[i] = calloc(1, bytes);
	}
	if (ps.buffers[0] == NULL || ps.buffers[1] == NULL || rs.buffers[0] == NULL || rs.buffers[1] == NULL) goto done;

	/* AudioManager::GetOpenSLEngine */
	if (tipsy_slCreateEngine(&engine, 1, option, 0, NULL, NULL) != 0 || engine == NULL) goto done;
	if ((*engine)->Realize(engine, 0) != 0) goto done;
	if ((*engine)->GetInterface(engine, SL_IID_ENGINE, &engine_itf) != 0) goto done;

	/* OpenSLESPlayer::CreateMix / CreateAudioPlayer */
	if ((*engine_itf)->CreateOutputMix(engine_itf, &mix, 0, NULL, NULL) != 0) goto done;
	if ((*mix)->Realize(mix, 0) != 0) goto done;
	mix_locator.outputMix = mix;
	if ((*engine_itf)->CreateAudioPlayer(engine_itf, &player, &play_source, &play_sink, 3, play_ids, play_req) != 0) goto done;
	if ((*player)->GetInterface(player, SL_IID_ANDROIDCONFIGURATION, &play_config) != 0) goto done;
	if ((*play_config)->SetConfiguration(play_config, (const uint8_t *)SL_ANDROID_KEY_STREAM_TYPE, &stream_type, sizeof(stream_type)) != 0) goto done;
	if ((*player)->Realize(player, 0) != 0) goto done;
	if ((*player)->GetInterface(player, SL_IID_PLAY, &play) != 0) goto done;
	if ((*player)->GetInterface(player, SL_IID_BUFFERQUEUE, &ps.queue) != 0) goto done;
	if ((*ps.queue)->RegisterCallback(ps.queue, webrtc_test_callback, &ps) != 0) goto done;
	if ((*player)->GetInterface(player, SL_IID_VOLUME, &volume) != 0) goto done;

	/* OpenSLESPlayer::StartPlayout */
	for (int i = 0; i < 2; i++)
		if ((*ps.queue)->Enqueue(ps.queue, ps.buffers[i], bytes) != 0) goto done;
	if ((*play)->SetPlayState(play, SL_PLAYSTATE_PLAYING) != 0) goto done;
	if ((*play)->GetPlayState(play, &st) != 0 || st != SL_PLAYSTATE_PLAYING) goto done;

	/* OpenSLESRecorder::CreateAudioRecorder */
	if ((*engine_itf)->CreateAudioRecorder(engine_itf, &recorder, &rec_source, &rec_sink, 2, rec_ids, rec_req) != 0) goto done;
	if ((*recorder)->GetInterface(recorder, SL_IID_ANDROIDCONFIGURATION, &rec_config) != 0) goto done;
	if ((*rec_config)->SetConfiguration(rec_config, (const uint8_t *)SL_ANDROID_KEY_RECORDING_PRESET, &preset, sizeof(preset)) != 0) goto done;
	if ((*recorder)->Realize(recorder, 0) != 0) goto done;
	if ((*recorder)->GetInterface(recorder, SL_IID_RECORD, &record) != 0) goto done;
	if ((*recorder)->GetInterface(recorder, SL_IID_ANDROIDSIMPLEBUFFERQUEUE, &rs.queue) != 0) goto done;
	if ((*rs.queue)->RegisterCallback(rs.queue, webrtc_test_callback, &rs) != 0) goto done;

	/* OpenSLESRecorder::StartRecording */
	if ((*rs.queue)->GetState(rs.queue, &qs) != 0 || qs.count != 0) goto done;
	for (int i = 0; i < 2; i++)
		if ((*rs.queue)->Enqueue(rs.queue, rs.buffers[i], bytes) != 0) goto done;
	if ((*rs.queue)->GetState(rs.queue, &qs) != 0 || qs.count != 2) goto done;
	if ((*record)->SetRecordState(record, SL_RECORDSTATE_RECORDING) != 0) goto done;
	if ((*record)->GetRecordState(record, &st) != 0 || st != SL_RECORDSTATE_RECORDING) goto done;

	/* Two primed buffers plus two callback re-enqueues per direction. */
	if (!test_wait_count(&ps.state, 4, 2000) || !test_wait_count(&rs.state, 4, 2000)) goto done;

	/* OpenSLESPlayer::StopPlayout / OpenSLESRecorder::StopRecording */
	if ((*play)->SetPlayState(play, SL_PLAYSTATE_STOPPED) != 0) goto done;
	if ((*ps.queue)->Clear(ps.queue) != 0) goto done;
	if ((*ps.queue)->GetState(ps.queue, &qs) != 0) goto done;
	if (cleared_count) *cleared_count = qs.count;
	if (cleared_index) *cleared_index = qs.index;
	if ((*record)->SetRecordState(record, SL_RECORDSTATE_STOPPED) != 0) goto done;
	if ((*rs.queue)->Clear(rs.queue) != 0) goto done;
	ok = 1;
done:
	if (player_voice) *player_voice = player != NULL ? from_object(player)->voice_stream : 0;
	if (recorder_voice) *recorder_voice = recorder != NULL ? from_object(recorder)->voice_stream : 0;
	/* DestroyAudioPlayer / DestroyAudioRecorder */
	if (ps.queue) (*ps.queue)->RegisterCallback(ps.queue, NULL, NULL);
	if (rs.queue) (*rs.queue)->RegisterCallback(rs.queue, NULL, NULL);
	if (player) (*player)->Destroy(player);
	if (recorder) (*recorder)->Destroy(recorder);
	if (mix) (*mix)->Destroy(mix);
	if (engine) (*engine)->Destroy(engine);
	pthread_mutex_lock(&ps.state.mu);
	if (play_callbacks) *play_callbacks = ps.state.count;
	pthread_mutex_unlock(&ps.state.mu);
	pthread_mutex_lock(&rs.state.mu);
	if (capture_callbacks) *capture_callbacks = rs.state.count;
	pthread_mutex_unlock(&rs.state.mu);
	pthread_cond_destroy(&ps.state.cond);
	pthread_mutex_destroy(&ps.state.mu);
	pthread_cond_destroy(&rs.state.cond);
	pthread_mutex_destroy(&rs.state.mu);
	for (int i = 0; i < 2; i++) {
		free(ps.buffers[i]);
		free(rs.buffers[i]);
	}
	fake_disable();
	return ok ? 0 : -1;
}

/* Capability-probe shape (what FMOD's OpenSL output plug-in and WebRTC can
 * ask before creating a stream), driven through the public vtables against
 * the fake host. Each check that fails sets one bit in *failed so the Go test
 * can name it. Also returns the recorder interface count and the OutputMix
 * device id so the test pins the exact Android answers. */
int tipsy_audio_test_probe_shape(uint32_t *failed, uint32_t *recorder_iids, uint32_t *mix_device,
	                             uint32_t *config_unknown_result, uint32_t *config_perf_result)
{
	fake_reset(0, 0);
	log_once_reset();
	SLObjectItf engine = NULL, mix = NULL, recorder = NULL;
	SLEngineItf engine_itf = NULL;
	SLOutputMixItf mix_itf = NULL;
	SLAndroidConfigurationItf config = NULL;
	SLPlayItf wrong = (SLPlayItf)0x1;
	SLVolumeItf volume = (SLVolumeItf)0x1;
	SLuint32 count = 0, options[2] = {SL_ENGINEOPTION_THREADSAFE, 1};
	SLInterfaceID iid = NULL;
	SLboolean ext = 1;
	SLint32 devices = 0;
	SLuint32 device_ids[1] = {0};
	uint32_t bits = 0;
	TipsyPCMFormat pcm = {SL_DATAFORMAT_PCM, 1, 48000000u, 16, 16, SL_SPEAKER_FRONT_CENTER, SL_BYTEORDER_LITTLEENDIAN, 0};
	SLDataLocator_BufferQueue bq = {SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, 2};
	SLuint32 mic_locator[4] = {SL_DATALOCATOR_IODEVICE, SL_IODEVICE_AUDIOINPUT, SL_DEFAULTDEVICEID_AUDIOINPUT, 0};
	SLDataSource rec_source = {mic_locator, NULL};
	SLDataSink rec_sink = {&bq, &pcm};
	const SLInterfaceID rec_ids[2] = {SL_IID_ANDROIDSIMPLEBUFFERQUEUE, SL_IID_ANDROIDCONFIGURATION};
	const SLboolean rec_req[2] = {1, 1};
	const SLInterfaceID engine_ids[1] = {SL_IID_ENGINE};
	const SLboolean engine_req[1] = {1};
	SLint32 perf = 1, unknown = 7;

	if (failed) *failed = 0;
	if (recorder_iids) *recorder_iids = 0;
	if (mix_device) *mix_device = 0;
	if (config_unknown_result) *config_unknown_result = 0;
	if (config_perf_result) *config_perf_result = 0;

	/* FMOD asks for SL_IID_ENGINE explicitly; the engine option is WebRTC's. */
	if (tipsy_slCreateEngine(&engine, 1, options, 1, engine_ids, engine_req) != 0 || engine == NULL) { bits |= 1u << 0; goto done; }
	if ((*engine)->Realize(engine, 0) != 0 || (*engine)->GetInterface(engine, SL_IID_ENGINE, &engine_itf) != 0) { bits |= 1u << 1; goto done; }
	/* Unsupported interface on the engine object: refused, out pointer cleared. */
	if ((*engine)->GetInterface(engine, SL_IID_PLAY, &wrong) != SL_RESULT_FEATURE_UNSUPPORTED || wrong != NULL) bits |= 1u << 2;
	/* Interface census per object class. */
	if ((*engine_itf)->QueryNumSupportedInterfaces(engine_itf, SL_OBJECTID_AUDIORECORDER, &count) != 0 || count == 0) bits |= 1u << 3;
	if (recorder_iids) *recorder_iids = count;
	for (SLuint32 i = 0; i < count; i++) {
		if ((*engine_itf)->QuerySupportedInterfaces(engine_itf, SL_OBJECTID_AUDIORECORDER, i, &iid) != 0 || iid == NULL) bits |= 1u << 4;
		else if (!supports_iid(OBJ_RECORDER, iid)) bits |= 1u << 5;
	}
	if ((*engine_itf)->QuerySupportedInterfaces(engine_itf, SL_OBJECTID_AUDIORECORDER, count, &iid) != SL_RESULT_PARAMETER_INVALID) bits |= 1u << 6;
	if ((*engine_itf)->QueryNumSupportedInterfaces(engine_itf, SL_OBJECTID_AUDIOPLAYER, &count) != 0 || count != 5) bits |= 1u << 7;
	if ((*engine_itf)->QueryNumSupportedInterfaces(engine_itf, SL_OBJECTID_ENGINE, &count) != 0 || count != 1) bits |= 1u << 8;
	if ((*engine_itf)->QueryNumSupportedInterfaces(engine_itf, 0x1006u /* MIDIPLAYER */, &count) != SL_RESULT_FEATURE_UNSUPPORTED) bits |= 1u << 9;
	if ((*engine_itf)->QueryNumSupportedExtensions(engine_itf, &count) != 0 || count != 0) bits |= 1u << 10;
	if ((*engine_itf)->IsExtensionSupported(engine_itf, (const uint8_t *)"ANDROID_SDK_LEVEL_23", &ext) != 0 || ext != 0) bits |= 1u << 11;
	/* OutputMix: one destination, the default output, both query forms. */
	if ((*engine_itf)->CreateOutputMix(engine_itf, &mix, 0, NULL, NULL) != 0 || (*mix)->Realize(mix, 0) != 0) { bits |= 1u << 12; goto done; }
	if ((*mix)->GetInterface(mix, SL_IID_OUTPUTMIX, &mix_itf) != 0) { bits |= 1u << 13; goto done; }
	if ((*mix_itf)->GetDestinationOutputDeviceIDs(mix_itf, &devices, NULL) != 0 || devices != 1) bits |= 1u << 14;
	devices = 1;
	if ((*mix_itf)->GetDestinationOutputDeviceIDs(mix_itf, &devices, device_ids) != 0 || devices != 1) bits |= 1u << 15;
	if (mix_device) *mix_device = device_ids[0];
	/* Recorder: WebRTC/FMOD shape; VOLUME is a player-only interface. */
	if ((*engine_itf)->CreateAudioRecorder(engine_itf, &recorder, &rec_source, &rec_sink, 2, rec_ids, rec_req) != 0) { bits |= 1u << 16; goto done; }
	if ((*recorder)->GetInterface(recorder, SL_IID_VOLUME, &volume) != SL_RESULT_FEATURE_UNSUPPORTED || volume != NULL) bits |= 1u << 17;
	if ((*recorder)->GetInterface(recorder, SL_IID_ANDROIDCONFIGURATION, &config) != 0) { bits |= 1u << 18; goto done; }
	if (config_perf_result) *config_perf_result = (*config)->SetConfiguration(config, (const uint8_t *)SL_ANDROID_KEY_PERFORMANCE_MODE, &perf, sizeof(perf));
	if (config_unknown_result) *config_unknown_result = (*config)->SetConfiguration(config, (const uint8_t *)"tipsyUnknownKey", &unknown, sizeof(unknown));
	/* Repeated probes must not add log lines: the once-table dedupes them. */
	for (int i = 0; i < 3; i++)
		(void)(*engine)->GetInterface(engine, SL_IID_PLAY, &wrong);
done:
	if (recorder) (*recorder)->Destroy(recorder);
	if (mix) (*mix)->Destroy(mix);
	if (engine) (*engine)->Destroy(engine);
	if (failed) *failed = bits;
	fake_disable();
	return bits == 0 ? 0 : -1;
}

/* H6 probe: spawn one real C worker through the same code the client uses and
 * read back the name it set on itself. The object is shut down exactly like
 * object_destroy before the thread is joined. */
int tipsy_test_audio_worker_thread_name(char *out, size_t cap)
{
	if (out == NULL || cap == 0)
		return -1;
	out[0] = '\0';
	fake_disable();
	tipsy_sl_object *o = object_new(OBJ_PLAYER);
	if (o == NULL)
		return -1;
	if (pthread_create(&o->thread, NULL, stream_worker, o) != 0) {
		pthread_cond_destroy(&o->cond);
		pthread_mutex_destroy(&o->mu);
		free(o);
		return -1;
	}
	o->thread_started = 1;
	char name[64];
	int ok = 0;
	for (int i = 0; i < 200; i++) {
		name[0] = '\0';
		if (pthread_getname_np(o->thread, name, sizeof(name)) == 0 &&
		    strcmp(name, "tip.opensles") == 0) {
			ok = 1;
			break;
		}
		struct timespec ts = {.tv_sec = 0, .tv_nsec = 1000000L};
		nanosleep(&ts, NULL);
	}
	if (ok)
		snprintf(out, cap, "%s", name);
	pthread_mutex_lock(&o->mu);
	o->destroying = 1;
	o->queue_generation++;
	pthread_cond_broadcast(&o->cond);
	pthread_mutex_unlock(&o->mu);
	pthread_join(o->thread, NULL);
	pthread_cond_destroy(&o->cond);
	pthread_mutex_destroy(&o->mu);
	free(o);
	return ok ? 0 : -1;
}
