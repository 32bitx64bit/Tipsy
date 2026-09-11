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
	uint32_t opens;
	uint32_t writes;
	uint32_t reads;
	uint64_t written_bytes;
	uint64_t read_bytes;
} fake_backend_state;

static fake_backend_state fake_backend = {PTHREAD_MUTEX_INITIALIZER, 0, 0, 0, 0, 0, 0, 0};

static void audio_log(const char *event, const char *detail)
{
	GoAndroid_LogAudio((char *)event, (char *)(detail ? detail : ""));
}

static int iid_equal(SLInterfaceID a, SLInterfaceID b)
{
	return a != NULL && b != NULL && (a == b || memcmp(a, b, 16) == 0);
}

static int microphone_disabled(void)
{
	const char *v = getenv("TIPSY_DISABLE_MICROPHONE");
	return v != NULL && (strcmp(v, "1") == 0 || strcmp(v, "true") == 0 || strcmp(v, "yes") == 0);
}

static int backend_open(tipsy_sl_object *o)
{
	char detail[192];
	if (o->backend_fake) {
		pthread_mutex_lock(&fake_backend.mu);
		fake_backend.opens++;
		pthread_mutex_unlock(&fake_backend.mu);
		o->stream = (pa_simple *)o;
		return 0;
	}
	if (o->kind == OBJ_RECORDER && microphone_disabled()) {
		audio_log("capture denied", "disabled by TIPSY_DISABLE_MICROPHONE");
		return -1;
	}
	int error = 0;
	pa_stream_direction_t direction = o->kind == OBJ_RECORDER ? PA_STREAM_RECORD : PA_STREAM_PLAYBACK;
	o->stream = pa_simple_new(NULL, "Tipsy", direction, NULL,
	                          o->kind == OBJ_RECORDER ? "Roblox microphone" : "Roblox audio",
	                          &o->sample, NULL, NULL, &error);
	if (o->stream == NULL) {
		snprintf(detail, sizeof(detail), "%s (%u Hz, %u ch)", pa_strerror(error),
		         o->sample.rate, o->sample.channels);
		audio_log(o->kind == OBJ_RECORDER ? "capture device unavailable" : "playback device unavailable", detail);
		return -1;
	}
	snprintf(detail, sizeof(detail), "PulseAudio %u Hz, %u ch, format=%d", o->sample.rate,
	         o->sample.channels, (int)o->sample.format);
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
	if (o->backend_fake) {
		int fail = 0;
		pthread_mutex_lock(&fake_backend.mu);
		if (o->kind == OBJ_RECORDER) {
			memset(buffer, 0x5a, bytes);
			fake_backend.reads++;
			fake_backend.read_bytes += bytes;
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
		return fail ? -1 : 0;
	}
	int error = 0;
	int rc = o->kind == OBJ_RECORDER
	             ? pa_simple_read(o->stream, buffer, bytes, &error)
	             : pa_simple_write(o->stream, buffer, bytes, &error);
	if (rc < 0) {
		audio_log(o->kind == OBJ_RECORDER ? "capture stream error" : "playback stream error",
		          pa_strerror(error));
	}
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
		if (o->stream == NULL)
			rc = backend_open(o);
		if (rc == 0)
			rc = backend_transfer(o, n->io_buffer, n->size);
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
	for (SLuint32 i = 0; i < count; i++) {
		if (required != NULL && required[i] && (ids == NULL || !supports_iid(kind, ids[i])))
			return 0;
	}
	return 1;
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
	(void)self; (void)key; (void)value; (void)size;
	return SL_RESULT_SUCCESS;
}
static SLresult config_get(SLAndroidConfigurationItf self, const uint8_t *key, SLuint32 *size, void *value)
{
	(void)self; (void)key; (void)value;
	if (size == NULL) return SL_RESULT_PARAMETER_INVALID;
	*size = 0;
	return SL_RESULT_FEATURE_UNSUPPORTED;
}
static SLresult config_acquire(SLAndroidConfigurationItf self, SLuint32 type, void **proxy) { (void)self; (void)type; if (proxy) *proxy = NULL; return SL_RESULT_FEATURE_UNSUPPORTED; }
static SLresult config_release(SLAndroidConfigurationItf self, SLuint32 type) { (void)self; (void)type; return SL_RESULT_FEATURE_UNSUPPORTED; }

static const struct SLAndroidConfigurationItf_ config_vtable = {
	config_set, config_get, config_acquire, config_release,
};

static SLresult mix_get_devices(SLOutputMixItf self, SLint32 *count, SLuint32 *ids) { (void)self; (void)ids; if (!count) return SL_RESULT_PARAMETER_INVALID; *count = 0; return SL_RESULT_SUCCESS; }
static SLresult mix_register(SLOutputMixItf self, slMixDeviceChangeCallback cb, void *ctx) { from_mix(self)->mix_cb = cb; from_mix(self)->mix_ctx = ctx; return SL_RESULT_SUCCESS; }
static SLresult mix_reroute(SLOutputMixItf self, SLint32 count, SLuint32 *ids) { (void)self; (void)count; (void)ids; return SL_RESULT_FEATURE_UNSUPPORTED; }

static const struct SLOutputMixItf_ mix_vtable = {mix_get_devices, mix_register, mix_reroute};

static SLresult create_stream_object(object_kind kind, SLObjectItf *out, SLDataSource *source,
	                                 SLDataSink *sink, SLuint32 count,
	                                 const SLInterfaceID *ids, const SLboolean *required)
{
	if (out == NULL || source == NULL || sink == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*out = NULL;
	if (!required_interfaces_supported(kind, count, ids, required))
		return SL_RESULT_FEATURE_UNSUPPORTED;
	const void *format = kind == OBJ_PLAYER ? source->pFormat : sink->pFormat;
	const void *locator = kind == OBJ_PLAYER ? source->pLocator : sink->pLocator;
	if (locator == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	const SLDataLocator_BufferQueue *bq = locator;
	if (bq->locatorType != SL_DATALOCATOR_BUFFERQUEUE &&
	    bq->locatorType != SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE)
		return SL_RESULT_FEATURE_UNSUPPORTED;
	pa_sample_spec sample;
	if (format_from(format, &sample) < 0)
		return SL_RESULT_CONTENT_UNSUPPORTED;
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

static SLresult engine_query_count(SLEngineItf self, SLuint32 object, SLuint32 *count) { (void)self; (void)object; if (!count) return SL_RESULT_PARAMETER_INVALID; *count = 0; return SL_RESULT_SUCCESS; }
static SLresult engine_query_iid(SLEngineItf self, SLuint32 object, SLuint32 index, SLInterfaceID *iid) { (void)self; (void)object; (void)index; if (iid) *iid = NULL; return SL_RESULT_FEATURE_UNSUPPORTED; }
static SLresult engine_extension_count(SLEngineItf self, SLuint32 *count) { (void)self; if (!count) return SL_RESULT_PARAMETER_INVALID; *count = 0; return SL_RESULT_SUCCESS; }
static SLresult engine_extension(SLEngineItf self, SLuint32 index, uint8_t *name, SLint16 *len) { (void)self; (void)index; (void)name; if (len) *len = 0; return SL_RESULT_FEATURE_UNSUPPORTED; }
static SLresult engine_extension_supported(SLEngineItf self, const uint8_t *name, SLboolean *supported) { (void)self; (void)name; if (!supported) return SL_RESULT_PARAMETER_INVALID; *supported = 0; return SL_RESULT_SUCCESS; }

static const struct SLEngineItf_ engine_vtable = {
	NULL, NULL, engine_create_player, engine_create_recorder, NULL, NULL, NULL, engine_create_mix,
	NULL, NULL, engine_query_count, engine_query_iid, engine_extension_count, engine_extension,
	engine_extension_supported,
};

uint32_t tipsy_slCreateEngine(void *pEngine, uint32_t numOptions, const void *pEngineOptions,
	                          uint32_t numInterfaces, const void *pInterfaceIds,
	                          const void *pInterfaceRequired)
{
	(void)numOptions;
	(void)pEngineOptions;
	if (pEngine == NULL)
		return SL_RESULT_PARAMETER_INVALID;
	*(SLObjectItf *)pEngine = NULL;
	if (!required_interfaces_supported(OBJ_ENGINE, numInterfaces, pInterfaceIds, pInterfaceRequired))
		return SL_RESULT_FEATURE_UNSUPPORTED;
	tipsy_sl_object *o = object_new(OBJ_ENGINE);
	if (o == NULL)
		return SL_RESULT_MEMORY_FAILURE;
	o->engine_vt = &engine_vtable;
	*(SLObjectItf *)pEngine = (SLObjectItf)&o->object_vt;
	audio_log("OpenSL ES engine created", "host bridge ready; stream opens lazily");
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

static int test_wait_callback(test_callback_state *state)
{
	struct timespec until;
	clock_gettime(CLOCK_REALTIME, &until);
	until.tv_sec += 2;
	pthread_mutex_lock(&state->mu);
	while (state->count == 0) {
		if (pthread_cond_timedwait(&state->cond, &state->mu, &until) == ETIMEDOUT)
			break;
	}
	int ok = state->count > 0;
	pthread_mutex_unlock(&state->mu);
	return ok;
}

static void fake_reset(int fail_first_write)
{
	pthread_mutex_lock(&fake_backend.mu);
	fake_backend.enabled = 1;
	fake_backend.fail_first_write = fail_first_write;
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

static int test_stream(int recording, uint32_t rate, uint32_t channels, uint32_t bytes,
	                   uint64_t *transferred, uint32_t *callbacks)
{
	SLObjectItf engine = NULL, mix = NULL, stream = NULL;
	SLEngineItf engine_itf = NULL;
	SLBufferQueueItf queue = NULL;
	SLPlayItf play = NULL;
	SLRecordItf record = NULL;
	uint8_t *buffer = calloc(1, bytes);
	if (buffer == NULL)
		return -1;
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
	if ((*stream)->Realize(stream, 0) != 0 || (*stream)->GetInterface(stream, SL_IID_ANDROIDSIMPLEBUFFERQUEUE, &queue) != 0) goto done;
	if ((*queue)->RegisterCallback(queue, test_queue_callback, &state) != 0) goto done;
	if (recording) {
		if ((*stream)->GetInterface(stream, SL_IID_RECORD, &record) != 0 || (*record)->SetRecordState(record, SL_RECORDSTATE_RECORDING) != 0) goto done;
	} else if ((*stream)->GetInterface(stream, SL_IID_PLAY, &play) != 0 || (*play)->SetPlayState(play, SL_PLAYSTATE_PLAYING) != 0) goto done;
	if ((*queue)->Enqueue(queue, buffer, bytes) != 0 || !test_wait_callback(&state)) goto done;
	ok = 1;
done:
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

int tipsy_audio_test_playback(uint32_t rate, uint32_t channels, uint32_t bytes,
	                          uint64_t *written, uint32_t *callbacks)
{
	fake_reset(0);
	int rc = test_stream(0, rate, channels, bytes, written, callbacks);
	fake_disable();
	return rc;
}

int tipsy_audio_test_capture(uint32_t rate, uint32_t channels, uint32_t bytes,
	                         uint64_t *read_bytes, uint32_t *callbacks)
{
	fake_reset(0);
	int rc = test_stream(1, rate, channels, bytes, read_bytes, callbacks);
	fake_disable();
	return rc;
}

int tipsy_audio_test_invalid_format(void)
{
	fake_reset(0);
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
	fake_reset(1);
	uint64_t transferred = 0;
	int rc = test_stream(0, 48000, 2, 1920, &transferred, callbacks);
	pthread_mutex_lock(&fake_backend.mu);
	if (opens) *opens = fake_backend.opens;
	if (writes) *writes = fake_backend.writes;
	pthread_mutex_unlock(&fake_backend.mu);
	fake_disable();
	return rc == 0 && transferred == 1920 ? 0 : -1;
}

int tipsy_audio_test_host_playback(uint32_t rate, uint32_t channels, uint32_t bytes,
	                               uint64_t *written, uint32_t *callbacks)
{
	fake_disable();
	return test_stream(0, rate, channels, bytes, written, callbacks);
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
