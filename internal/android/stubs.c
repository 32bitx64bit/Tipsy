/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Stubs for libmediandk, libOpenMAXAL, and libjnigraphics. OpenSL ES lives
 * in opensles.c because it has a real host-audio bridge.
 * Relocations succeed; calls log and return NULL / error codes.
 */
#include "android_bridge.h"

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

extern void GoAndroid_LogMissing(char *name);

#define AMEDIA_UNSUPPORTED ((int32_t)-10002)

static int32_t media_err(const char *name)
{
	GoAndroid_LogMissing((char *)name);
	return AMEDIA_UNSUPPORTED;
}

static void *media_null(const char *name)
{
	GoAndroid_LogMissing((char *)name);
	return NULL;
}

void *tipsy_AMediaCodec_createDecoderByType(const char *mime)
{
	(void)mime;
	return media_null("AMediaCodec_createDecoderByType");
}
void *tipsy_AMediaCodec_createEncoderByType(const char *mime)
{
	(void)mime;
	return media_null("AMediaCodec_createEncoderByType");
}
void *tipsy_AMediaCodec_createCodecByName(const char *name)
{
	(void)name;
	return media_null("AMediaCodec_createCodecByName");
}
int32_t tipsy_AMediaCodec_delete(void *codec)
{
	(void)codec;
	return media_err("AMediaCodec_delete");
}
int32_t tipsy_AMediaCodec_configure(void *codec, void *format, void *surface, void *crypto, uint32_t flags)
{
	(void)codec;
	(void)format;
	(void)surface;
	(void)crypto;
	(void)flags;
	return media_err("AMediaCodec_configure");
}
int32_t tipsy_AMediaCodec_start(void *codec)
{
	(void)codec;
	return media_err("AMediaCodec_start");
}
int32_t tipsy_AMediaCodec_stop(void *codec)
{
	(void)codec;
	return media_err("AMediaCodec_stop");
}
int32_t tipsy_AMediaCodec_flush(void *codec)
{
	(void)codec;
	return media_err("AMediaCodec_flush");
}
void *tipsy_AMediaCodec_getInputBuffer(void *codec, size_t idx, size_t *out_size)
{
	(void)codec;
	(void)idx;
	if (out_size) {
		*out_size = 0;
	}
	return media_null("AMediaCodec_getInputBuffer");
}
void *tipsy_AMediaCodec_getOutputBuffer(void *codec, size_t idx, size_t *out_size)
{
	(void)codec;
	(void)idx;
	if (out_size) {
		*out_size = 0;
	}
	return media_null("AMediaCodec_getOutputBuffer");
}
ssize_t tipsy_AMediaCodec_dequeueInputBuffer(void *codec, int64_t timeoutUs)
{
	(void)codec;
	(void)timeoutUs;
	return media_err("AMediaCodec_dequeueInputBuffer");
}
int32_t tipsy_AMediaCodec_queueInputBuffer(void *codec, size_t idx, off_t offset, size_t size, uint64_t time, uint32_t flags)
{
	(void)codec;
	(void)idx;
	(void)offset;
	(void)size;
	(void)time;
	(void)flags;
	return media_err("AMediaCodec_queueInputBuffer");
}
ssize_t tipsy_AMediaCodec_dequeueOutputBuffer(void *codec, void *info, int64_t timeoutUs)
{
	(void)codec;
	(void)info;
	(void)timeoutUs;
	return media_err("AMediaCodec_dequeueOutputBuffer");
}
int32_t tipsy_AMediaCodec_releaseOutputBuffer(void *codec, size_t idx, int render)
{
	(void)codec;
	(void)idx;
	(void)render;
	return media_err("AMediaCodec_releaseOutputBuffer");
}
void *tipsy_AMediaCodec_getOutputFormat(void *codec)
{
	(void)codec;
	return media_null("AMediaCodec_getOutputFormat");
}
void *tipsy_AMediaCodec_getInputFormat(void *codec)
{
	(void)codec;
	return media_null("AMediaCodec_getInputFormat");
}
int32_t tipsy_AMediaCodec_setOutputSurface(void *codec, void *surface)
{
	(void)codec;
	(void)surface;
	return media_err("AMediaCodec_setOutputSurface");
}
int32_t tipsy_AMediaCodec_releaseOutputBufferAtTime(void *codec, size_t idx, int64_t timestampNs)
{
	(void)codec;
	(void)idx;
	(void)timestampNs;
	return media_err("AMediaCodec_releaseOutputBufferAtTime");
}
int32_t tipsy_AMediaCodec_getName(void *codec, char **out)
{
	(void)codec;
	if (out) {
		*out = NULL;
	}
	return media_err("AMediaCodec_getName");
}
void tipsy_AMediaCodec_releaseName(void *codec, char *name)
{
	(void)codec;
	(void)name;
}
int32_t tipsy_AMediaCodec_setParameters(void *codec, void *params)
{
	(void)codec;
	(void)params;
	return media_err("AMediaCodec_setParameters");
}

void *tipsy_AMediaFormat_new(void)
{
	return media_null("AMediaFormat_new");
}
int32_t tipsy_AMediaFormat_delete(void *fmt)
{
	(void)fmt;
	return media_err("AMediaFormat_delete");
}
const char *tipsy_AMediaFormat_toString(void *fmt)
{
	(void)fmt;
	media_null("AMediaFormat_toString");
	return "";
}
int32_t tipsy_AMediaFormat_getInt32(void *fmt, const char *name, int32_t *out)
{
	(void)fmt;
	(void)name;
	(void)out;
	return 0;
}
int32_t tipsy_AMediaFormat_getInt64(void *fmt, const char *name, int64_t *out)
{
	(void)fmt;
	(void)name;
	(void)out;
	return 0;
}
int32_t tipsy_AMediaFormat_getFloat(void *fmt, const char *name, float *out)
{
	(void)fmt;
	(void)name;
	(void)out;
	return 0;
}
int32_t tipsy_AMediaFormat_getBuffer(void *fmt, const char *name, void **data, size_t *size)
{
	(void)fmt;
	(void)name;
	(void)data;
	(void)size;
	return 0;
}
int32_t tipsy_AMediaFormat_getString(void *fmt, const char *name, const char **out)
{
	(void)fmt;
	(void)name;
	if (out) {
		*out = NULL;
	}
	return 0;
}
void tipsy_AMediaFormat_setInt32(void *fmt, const char *name, int32_t value)
{
	(void)fmt;
	(void)name;
	(void)value;
}
void tipsy_AMediaFormat_setInt64(void *fmt, const char *name, int64_t value)
{
	(void)fmt;
	(void)name;
	(void)value;
}
void tipsy_AMediaFormat_setFloat(void *fmt, const char *name, float value)
{
	(void)fmt;
	(void)name;
	(void)value;
}
void tipsy_AMediaFormat_setString(void *fmt, const char *name, const char *value)
{
	(void)fmt;
	(void)name;
	(void)value;
}
void tipsy_AMediaFormat_setBuffer(void *fmt, const char *name, void *data, size_t size)
{
	(void)fmt;
	(void)name;
	(void)data;
	(void)size;
}

/* NDK AMEDIAFORMAT_KEY_* data symbols (const char *). */
const char *AMEDIAFORMAT_KEY_AAC_PROFILE = "aac-profile";
const char *AMEDIAFORMAT_KEY_BIT_RATE = "bitrate";
const char *AMEDIAFORMAT_KEY_CHANNEL_COUNT = "channel-count";
const char *AMEDIAFORMAT_KEY_CHANNEL_MASK = "channel-mask";
const char *AMEDIAFORMAT_KEY_COLOR_FORMAT = "color-format";
const char *AMEDIAFORMAT_KEY_DURATION = "durationUs";
const char *AMEDIAFORMAT_KEY_FLAC_COMPRESSION_LEVEL = "flac-compression-level";
const char *AMEDIAFORMAT_KEY_FRAME_RATE = "frame-rate";
const char *AMEDIAFORMAT_KEY_HEIGHT = "height";
const char *AMEDIAFORMAT_KEY_IS_ADTS = "is-adts";
const char *AMEDIAFORMAT_KEY_I_FRAME_INTERVAL = "i-frame-interval";
const char *AMEDIAFORMAT_KEY_LANGUAGE = "language";
const char *AMEDIAFORMAT_KEY_MAX_HEIGHT = "max-height";
const char *AMEDIAFORMAT_KEY_MAX_INPUT_SIZE = "max-input-size";
const char *AMEDIAFORMAT_KEY_MAX_WIDTH = "max-width";
const char *AMEDIAFORMAT_KEY_MIME = "mime";
const char *AMEDIAFORMAT_KEY_SAMPLE_RATE = "sample-rate";
const char *AMEDIAFORMAT_KEY_WIDTH = "width";
const char *AMEDIAFORMAT_KEY_STRIDE = "stride";
const char *AMEDIAFORMAT_KEY_SLICE_HEIGHT = "slice-height";
const char *AMEDIAFORMAT_KEY_ROTATION = "rotation-degrees";

static uint32_t xa_iid_engine[4] = { 101, 0, 0, 0 };
static uint32_t xa_iid_play[4] = { 102, 0, 0, 0 };
static uint32_t xa_iid_outputmix[4] = { 103, 0, 0, 0 };

const void *XA_IID_ENGINE = xa_iid_engine;
const void *XA_IID_PLAY = xa_iid_play;
const void *XA_IID_OUTPUTMIX = xa_iid_outputmix;

/* SL_RESULT_FEATURE_UNSUPPORTED = 0x0000000C, SL_RESULT_RESOURCE_ERROR = 0x00000009 */
#define SL_RESULT_FEATURE_UNSUPPORTED ((uint32_t)0x0000000C)

uint32_t tipsy_xaCreateEngine(void *pEngine, uint32_t numOptions, const void *pEngineOptions,
			      uint32_t numInterfaces, const void *pInterfaceIds, const void *pInterfaceRequired)
{
	(void)numOptions;
	(void)pEngineOptions;
	(void)numInterfaces;
	(void)pInterfaceIds;
	(void)pInterfaceRequired;
	GoAndroid_LogMissing("xaCreateEngine");
	if (pEngine != NULL) {
		*(void **)pEngine = NULL;
	}
	return SL_RESULT_FEATURE_UNSUPPORTED;
}

int32_t tipsy_AndroidBitmap_getInfo(void *env, void *jbitmap, void *info)
{
	(void)env;
	(void)jbitmap;
	(void)info;
	GoAndroid_LogMissing("AndroidBitmap_getInfo");
	return ANDROID_BITMAP_RESULT_ALLOCATION_FAILED;
}

int32_t tipsy_AndroidBitmap_lockPixels(void *env, void *jbitmap, void **addrPtr)
{
	(void)env;
	(void)jbitmap;
	if (addrPtr) {
		*addrPtr = NULL;
	}
	GoAndroid_LogMissing("AndroidBitmap_lockPixels");
	return ANDROID_BITMAP_RESULT_ALLOCATION_FAILED;
}

int32_t tipsy_AndroidBitmap_unlockPixels(void *env, void *jbitmap)
{
	(void)env;
	(void)jbitmap;
	GoAndroid_LogMissing("AndroidBitmap_unlockPixels");
	return ANDROID_BITMAP_RESULT_ALLOCATION_FAILED;
}

/* Generic fallbacks so unknown media symbols still relocate. */
int64_t tipsy_media_stub_err(void)
{
	GoAndroid_LogMissing("AMediaCodec_*");
	return AMEDIA_UNSUPPORTED;
}

void *tipsy_media_stub_null(void)
{
	GoAndroid_LogMissing("AMediaCodec_*");
	return NULL;
}
