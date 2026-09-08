/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Name → address table for Android sonames implemented in-process.
 */
#include "android_bridge.h"

#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <stddef.h>
#include <sys/types.h>
#include <sys/time.h>
#include <time.h>

/* ndk.c */
extern int tipsy_android_log_print(int, const char *, const char *, ...);
extern int tipsy_android_log_write(int, const char *, const char *);
extern int tipsy_android_log_vprint(int, const char *, const char *, void *);
extern void tipsy_android_log_assert(const char *, const char *, const char *, ...);
extern int tipsy_android_log_buf_write(int, int, const char *, const char *);
extern void tipsy_android_set_abort_message(const char *);
extern int tipsy_system_property_get(const char *, char *);
extern int tipsy_ALooper_pollOnce(int, int *, int *, void **);
extern int tipsy_ALooper_addFd(void *, int, int, int, void *, void *);
extern int tipsy_ALooper_removeFd(void *, int);
extern void tipsy_ALooper_acquire(void *);
extern void tipsy_ALooper_release(void *);
extern void tipsy_ALooper_wake(void *);
extern void *tipsy_AAssetManager_fromJava(void *, void *);
extern void *tipsy_AAssetManager_open(void *, const char *, int);
extern void tipsy_AAsset_close(void *);
extern const void *tipsy_AAsset_getBuffer(void *);
extern int64_t tipsy_AAsset_getLength(void *);
extern int tipsy_AAsset_openFileDescriptor(void *, int64_t *, int64_t *);
extern int tipsy_AAsset_read(void *, void *, size_t);
extern int64_t tipsy_AAsset_seek(void *, int64_t, int);
extern void *tipsy_AConfiguration_new(void);
extern void tipsy_AConfiguration_delete(void *);
extern void tipsy_AConfiguration_fromAssetManager(void *, void *);
extern void tipsy_AConfiguration_getLanguage(void *, char *);
extern void tipsy_AConfiguration_getCountry(void *, char *);
extern int32_t tipsy_AConfiguration_getScreenWidthDp(void *);
extern int32_t tipsy_AConfiguration_getScreenHeightDp(void *);
extern int32_t tipsy_AConfiguration_getScreenSize(void *);
extern int32_t tipsy_AConfiguration_getNavHidden(void *);
extern void *tipsy_ANativeWindow_fromSurface(void *, void *);
extern void tipsy_ANativeWindow_acquire(void *);
extern void tipsy_ANativeWindow_release(void *);
extern int32_t tipsy_ANativeWindow_getWidth(void *);
extern int32_t tipsy_ANativeWindow_getHeight(void *);
extern int32_t tipsy_ANativeWindow_getFormat(void *);
extern int32_t tipsy_ANativeWindow_setBuffersGeometry(void *, int32_t, int32_t, int32_t);
extern int32_t tipsy_ANativeWindow_lock(void *, void *, void *);
extern int32_t tipsy_ANativeWindow_unlockAndPost(void *);
extern int *tipsy_errno_fn(void);
extern void tipsy_arc4random_buf(void *, size_t);
extern int32_t tipsy_gettid(void);

/* dl.c */
extern void *tipsy_dlopen(const char *, int);
extern void *tipsy_dlsym(void *, const char *);
extern int tipsy_dlclose(void *);
extern char *tipsy_dlerror(void);
extern int tipsy_dladdr(const void *, void *);
extern int tipsy_dl_iterate_phdr(void *, void *);

/* host.c */
extern void *tipsy_eglCreateWindowSurface(void *, void *, void *, const int32_t *);
extern void *tipsy_eglGetProcAddress(const char *);
extern uint32_t tipsy_eglSwapInterval(void *, int32_t);
extern uint32_t tipsy_eglSwapBuffers(void *, void *);
extern void *tipsy_egl_dlsym(const char *);
extern void *tipsy_gles_dlsym(const char *);

/* stubs.c */
extern void *tipsy_AMediaCodec_createDecoderByType(const char *);
extern void *tipsy_AMediaCodec_createEncoderByType(const char *);
extern void *tipsy_AMediaCodec_createCodecByName(const char *);
extern int32_t tipsy_AMediaCodec_delete(void *);
extern int32_t tipsy_AMediaCodec_configure(void *, void *, void *, void *, uint32_t);
extern int32_t tipsy_AMediaCodec_start(void *);
extern int32_t tipsy_AMediaCodec_stop(void *);
extern int32_t tipsy_AMediaCodec_flush(void *);
extern void *tipsy_AMediaCodec_getInputBuffer(void *, size_t, size_t *);
extern void *tipsy_AMediaCodec_getOutputBuffer(void *, size_t, size_t *);
extern long tipsy_AMediaCodec_dequeueInputBuffer(void *, int64_t);
extern int32_t tipsy_AMediaCodec_queueInputBuffer(void *, size_t, long, size_t, uint64_t, uint32_t);
extern long tipsy_AMediaCodec_dequeueOutputBuffer(void *, void *, int64_t);
extern int32_t tipsy_AMediaCodec_releaseOutputBuffer(void *, size_t, int);
extern void *tipsy_AMediaCodec_getOutputFormat(void *);
extern void *tipsy_AMediaCodec_getInputFormat(void *);
extern int32_t tipsy_AMediaCodec_setOutputSurface(void *, void *);
extern int32_t tipsy_AMediaCodec_releaseOutputBufferAtTime(void *, size_t, int64_t);
extern int32_t tipsy_AMediaCodec_getName(void *, char **);
extern void tipsy_AMediaCodec_releaseName(void *, char *);
extern int32_t tipsy_AMediaCodec_setParameters(void *, void *);
extern void *tipsy_AMediaFormat_new(void);
extern int32_t tipsy_AMediaFormat_delete(void *);
extern const char *tipsy_AMediaFormat_toString(void *);
extern int32_t tipsy_AMediaFormat_getInt32(void *, const char *, int32_t *);
extern int32_t tipsy_AMediaFormat_getInt64(void *, const char *, int64_t *);
extern int32_t tipsy_AMediaFormat_getFloat(void *, const char *, float *);
extern int32_t tipsy_AMediaFormat_getBuffer(void *, const char *, void **, size_t *);
extern int32_t tipsy_AMediaFormat_getString(void *, const char *, const char **);
extern void tipsy_AMediaFormat_setInt32(void *, const char *, int32_t);
extern void tipsy_AMediaFormat_setInt64(void *, const char *, int64_t);
extern void tipsy_AMediaFormat_setFloat(void *, const char *, float);
extern void tipsy_AMediaFormat_setString(void *, const char *, const char *);
extern void tipsy_AMediaFormat_setBuffer(void *, const char *, void *, size_t);
extern uint32_t tipsy_slCreateEngine(void *, uint32_t, const void *, uint32_t, const void *, const void *);
extern uint32_t tipsy_xaCreateEngine(void *, uint32_t, const void *, uint32_t, const void *, const void *);
extern int32_t tipsy_AndroidBitmap_getInfo(void *, void *, void *);
extern int32_t tipsy_AndroidBitmap_lockPixels(void *, void *, void **);
extern int32_t tipsy_AndroidBitmap_unlockPixels(void *, void *);
extern int64_t tipsy_media_stub_err(void);
extern void *tipsy_media_stub_null(void);

extern const char *AMEDIAFORMAT_KEY_AAC_PROFILE;
extern const char *AMEDIAFORMAT_KEY_BIT_RATE;
extern const char *AMEDIAFORMAT_KEY_CHANNEL_COUNT;
extern const char *AMEDIAFORMAT_KEY_CHANNEL_MASK;
extern const char *AMEDIAFORMAT_KEY_COLOR_FORMAT;
extern const char *AMEDIAFORMAT_KEY_DURATION;
extern const char *AMEDIAFORMAT_KEY_FLAC_COMPRESSION_LEVEL;
extern const char *AMEDIAFORMAT_KEY_FRAME_RATE;
extern const char *AMEDIAFORMAT_KEY_HEIGHT;
extern const char *AMEDIAFORMAT_KEY_IS_ADTS;
extern const char *AMEDIAFORMAT_KEY_I_FRAME_INTERVAL;
extern const char *AMEDIAFORMAT_KEY_LANGUAGE;
extern const char *AMEDIAFORMAT_KEY_MAX_HEIGHT;
extern const char *AMEDIAFORMAT_KEY_MAX_INPUT_SIZE;
extern const char *AMEDIAFORMAT_KEY_MAX_WIDTH;
extern const char *AMEDIAFORMAT_KEY_MIME;
extern const char *AMEDIAFORMAT_KEY_SAMPLE_RATE;
extern const char *AMEDIAFORMAT_KEY_WIDTH;
extern const char *AMEDIAFORMAT_KEY_STRIDE;
extern const char *AMEDIAFORMAT_KEY_SLICE_HEIGHT;
extern const char *AMEDIAFORMAT_KEY_ROTATION;
extern const void *SL_IID_ENGINE;
extern const void *SL_IID_PLAY;
extern const void *SL_IID_BUFFERQUEUE;
extern const void *SL_IID_VOLUME;
extern const void *SL_IID_OUTPUTMIX;
extern const void *SL_IID_ANDROIDCONFIGURATION;
extern const void *SL_IID_ANDROIDSIMPLEBUFFERQUEUE;
extern const void *SL_IID_RECORD;
extern const void *SL_IID_SEEK;
extern const void *SL_IID_PREFETCHSTATUS;
extern const void *SL_IID_ANDROIDBUFFERQUEUESOURCE;
extern const void *XA_IID_ENGINE;
extern const void *XA_IID_PLAY;
extern const void *XA_IID_OUTPUTMIX;

extern size_t tipsy_strlen_chk(const char *, size_t);
extern char *tipsy_strchr_chk(const char *, int, size_t);
extern char *tipsy_strncpy_chk2(char *, const char *, size_t, size_t, size_t);
extern char *tipsy_strncpy_chk(char *, const char *, size_t, size_t);
extern void *tipsy_memcpy_chk(void *, const void *, size_t, size_t);
extern void *tipsy_memmove_chk(void *, const void *, size_t, size_t);
extern void *tipsy_memset_chk(void *, int, size_t, size_t);
extern size_t tipsy_fwrite_chk(const void *, size_t, size_t, void *, size_t);
extern ssize_t tipsy_write_chk(int, const void *, size_t, size_t);
extern ssize_t tipsy_sendto_chk(int, const void *, size_t, int, const void *, unsigned, size_t);
extern ssize_t tipsy_read_chk(int, void *, size_t, size_t);
extern int tipsy_open_2(const char *, int);
extern int tipsy_open(const char *, int, ...);
extern int tipsy_openat(int, const char *, int, ...);
extern FILE *tipsy_fopen(const char *, const char *);
extern const char *tipsy_cacert_wanted(void);
extern void tipsy_FD_SET_chk(int, void *, size_t);
extern void tipsy_FD_CLR_chk(int, void *, size_t);
extern int tipsy_FD_ISSET_chk(int, void *, size_t);
extern void tipsy_assert2(const char *, int, const char *, const char *);
extern int tipsy_gnu_strerror_r(int, char *, size_t);
extern char *tipsy_strcpy_chk(char *, const char *, size_t);
extern char *tipsy_strcat_chk(char *, const char *, size_t);
extern int tipsy_snprintf_chk(char *, size_t, int, size_t, const char *, ...);
extern int tipsy_vsnprintf_chk(char *, size_t, int, size_t, const char *, void *);
extern int tipsy_poll_chk(void *, unsigned long, int, size_t);
extern void tipsy_FD_ZERO_chk(void *, size_t);
extern int tipsy_openat_2(int, const char *, int);
extern unsigned char tipsy_sF[];
extern uint64_t tipsy_stack_chk_guard;
extern void tipsy_gcov_nop(void);
extern void *tipsy_zstd_trace_begin(const void *);
extern void tipsy_zstd_trace_end(void *, const void *);
extern long tipsy_sysconf(int);
extern int tipsy_fflush(void *);
extern int tipsy_pthread_cond_wait(void *, void *);
extern int tipsy_pthread_cond_timedwait(void *, void *, void *);
extern int tipsy_pthread_mutex_lock(void *);
extern int tipsy_pthread_mutex_trylock(void *);
extern int tipsy_pthread_mutex_timedlock(void *, const void *);
extern int tipsy_pthread_mutex_unlock(void *);
extern int tipsy_pthread_cond_signal(void *);
extern int tipsy_pthread_cond_broadcast(void *);
extern int tipsy_pthread_setaffinity_np(uintptr_t, size_t, const void *);
extern int tipsy_sched_yield(void);
extern int tipsy_sched_getaffinity(int, size_t, void *);
extern int tipsy_sched_setaffinity(int, size_t, const void *);
extern int tipsy_nice(int);
extern size_t tipsy_fwrite(const void *, size_t, size_t, void *);
extern size_t tipsy_fread(void *, size_t, size_t, void *);
extern size_t tipsy_fread_chk(void *, size_t, size_t, void *, size_t);
extern int tipsy_fprintf(void *, const char *, ...);
extern int tipsy_vfprintf(void *, const char *, void *);
extern int tipsy_fclose(void *);
extern int tipsy_fileno(void *);
extern int tipsy_fputc(int, void *);
extern int tipsy_fputs(const char *, void *);
extern char *tipsy_fgets(char *, int, void *);
extern int tipsy_fseek(void *, long, int);
extern int tipsy_fseeko(void *, long, int);
extern long tipsy_ftell(void *);
extern long tipsy_ftello(void *);
extern int tipsy_getaddrinfo(const char *, const char *, const void *, void **);
extern void tipsy_freeaddrinfo(void *);
extern int tipsy_getnameinfo(const void *, uint32_t, char *, size_t, char *, size_t, int);
extern const char *tipsy_gai_strerror(int);
extern int tipsy_sigaction(int, const void *, void *);
extern long tipsy_rt_sigaction(int, const void *, void *, size_t);

struct sym {
	const char *lib;
	const char *name;
	void *addr;
};

/* glibc may return vDSO addresses for these names. Keep the Android provider
 * owner exact by routing through small Tipsy-owned ABI-compatible wrappers. */
static time_t tipsy_time(time_t *result)
{
	return time(result);
}

static int tipsy_gettimeofday(struct timeval *value, void *timezone_value)
{
	return gettimeofday(value, (struct timezone *)timezone_value);
}

static const struct sym table[] = {
	{ "liblog.so", "__android_log_print", (void *)tipsy_android_log_print },
	{ "liblog.so", "__android_log_write", (void *)tipsy_android_log_write },
	{ "liblog.so", "__android_log_vprint", (void *)tipsy_android_log_vprint },
	{ "liblog.so", "__android_log_assert", (void *)tipsy_android_log_assert },
	{ "liblog.so", "__android_log_buf_write", (void *)tipsy_android_log_buf_write },
	{ "libc.so", "android_set_abort_message", (void *)tipsy_android_set_abort_message },
	{ "libc.so", "__system_property_get", (void *)tipsy_system_property_get },
	{ "libc.so", "__errno", (void *)tipsy_errno_fn },
	{ "libc.so", "arc4random_buf", (void *)tipsy_arc4random_buf },
	{ "libc.so", "gettid", (void *)tipsy_gettid },
	{ "libc.so", "time", (void *)tipsy_time },
	{ "libc.so", "gettimeofday", (void *)tipsy_gettimeofday },
	{ "libc.so", "__strlen_chk", (void *)tipsy_strlen_chk },
	{ "libc.so", "__strchr_chk", (void *)tipsy_strchr_chk },
	{ "libc.so", "__strncpy_chk2", (void *)tipsy_strncpy_chk2 },
	{ "libc.so", "__strncpy_chk", (void *)tipsy_strncpy_chk },
	{ "libc.so", "__memcpy_chk", (void *)tipsy_memcpy_chk },
	{ "libc.so", "__memmove_chk", (void *)tipsy_memmove_chk },
	{ "libc.so", "__memset_chk", (void *)tipsy_memset_chk },
	{ "libc.so", "__fwrite_chk", (void *)tipsy_fwrite_chk },
	{ "libc.so", "__write_chk", (void *)tipsy_write_chk },
	{ "libc.so", "__sendto_chk", (void *)tipsy_sendto_chk },
	{ "libc.so", "__read_chk", (void *)tipsy_read_chk },
	{ "libc.so", "__open_2", (void *)tipsy_open_2 },
	{ "libc.so", "open", (void *)tipsy_open },
	{ "libc.so", "openat", (void *)tipsy_openat },
	{ "libc.so", "fopen", (void *)tipsy_fopen },
	{ "libc.so", "__FD_SET_chk", (void *)tipsy_FD_SET_chk },
	{ "libc.so", "__FD_CLR_chk", (void *)tipsy_FD_CLR_chk },
	{ "libc.so", "__FD_ISSET_chk", (void *)tipsy_FD_ISSET_chk },
	{ "libc.so", "__assert2", (void *)tipsy_assert2 },
	{ "libc.so", "__gnu_strerror_r", (void *)tipsy_gnu_strerror_r },
	{ "libc.so", "__strcpy_chk", (void *)tipsy_strcpy_chk },
	{ "libc.so", "__strcat_chk", (void *)tipsy_strcat_chk },
	{ "libc.so", "__snprintf_chk", (void *)tipsy_snprintf_chk },
	{ "libc.so", "__vsnprintf_chk", (void *)tipsy_vsnprintf_chk },
	{ "libc.so", "__poll_chk", (void *)tipsy_poll_chk },
	{ "libc.so", "__FD_ZERO_chk", (void *)tipsy_FD_ZERO_chk },
	{ "libc.so", "__openat_2", (void *)tipsy_openat_2 },
	{ "libc.so", "__sF", (void *)tipsy_sF },
	{ "libc.so", "__stack_chk_guard", (void *)&tipsy_stack_chk_guard },
	{ "libc.so", "__gcov_dump", (void *)tipsy_gcov_nop },
	{ "libc.so", "__gcov_flush", (void *)tipsy_gcov_nop },
	{ "libc.so", "ZSTD_trace_compress_begin", (void *)tipsy_zstd_trace_begin },
	{ "libc.so", "ZSTD_trace_compress_end", (void *)tipsy_zstd_trace_end },
	{ "libc.so", "ZSTD_trace_decompress_begin", (void *)tipsy_zstd_trace_begin },
	{ "libc.so", "ZSTD_trace_decompress_end", (void *)tipsy_zstd_trace_end },
	{ "libc.so", "sysconf", (void *)tipsy_sysconf },
	{ "libc.so", "pthread_cond_wait", (void *)tipsy_pthread_cond_wait },
	{ "libc.so", "pthread_cond_timedwait", (void *)tipsy_pthread_cond_timedwait },
	{ "libc.so", "pthread_mutex_lock", (void *)tipsy_pthread_mutex_lock },
	{ "libc.so", "pthread_mutex_trylock", (void *)tipsy_pthread_mutex_trylock },
	{ "libc.so", "pthread_mutex_timedlock", (void *)tipsy_pthread_mutex_timedlock },
	{ "libc.so", "pthread_mutex_unlock", (void *)tipsy_pthread_mutex_unlock },
	{ "libc.so", "pthread_cond_signal", (void *)tipsy_pthread_cond_signal },
	{ "libc.so", "pthread_cond_broadcast", (void *)tipsy_pthread_cond_broadcast },
	{ "libc.so", "pthread_setaffinity_np", (void *)tipsy_pthread_setaffinity_np },
	{ "libc.so", "sched_yield", (void *)tipsy_sched_yield },
	{ "libc.so", "sched_getaffinity", (void *)tipsy_sched_getaffinity },
	{ "libc.so", "sched_setaffinity", (void *)tipsy_sched_setaffinity },
	{ "libc.so", "nice", (void *)tipsy_nice },
	{ "libc.so", "fflush", (void *)tipsy_fflush },
	{ "libc.so", "fwrite", (void *)tipsy_fwrite },
	{ "libc.so", "fread", (void *)tipsy_fread },
	{ "libc.so", "__fread_chk", (void *)tipsy_fread_chk },
	{ "libc.so", "fprintf", (void *)tipsy_fprintf },
	{ "libc.so", "vfprintf", (void *)tipsy_vfprintf },
	{ "libc.so", "fclose", (void *)tipsy_fclose },
	{ "libc.so", "fileno", (void *)tipsy_fileno },
	{ "libc.so", "fputc", (void *)tipsy_fputc },
	{ "libc.so", "fputs", (void *)tipsy_fputs },
	{ "libc.so", "fgets", (void *)tipsy_fgets },
	{ "libc.so", "fseek", (void *)tipsy_fseek },
	{ "libc.so", "fseeko", (void *)tipsy_fseeko },
	{ "libc.so", "ftell", (void *)tipsy_ftell },
	{ "libc.so", "ftello", (void *)tipsy_ftello },
	{ "libc.so", "getaddrinfo", (void *)tipsy_getaddrinfo },
	{ "libc.so", "freeaddrinfo", (void *)tipsy_freeaddrinfo },
	{ "libc.so", "getnameinfo", (void *)tipsy_getnameinfo },
	{ "libc.so", "gai_strerror", (void *)tipsy_gai_strerror },
	{ "libc.so", "sigaction", (void *)tipsy_sigaction },
	{ "libc.so", "sigaction64", (void *)tipsy_sigaction },
	{ "libc.so", "rt_sigaction", (void *)tipsy_rt_sigaction },
	{ "libc.so", "__rt_sigaction", (void *)tipsy_rt_sigaction },

	{ "libdl.so", "dlopen", (void *)tipsy_dlopen },
	{ "libdl.so", "dlsym", (void *)tipsy_dlsym },
	{ "libdl.so", "dlclose", (void *)tipsy_dlclose },
	{ "libdl.so", "dlerror", (void *)tipsy_dlerror },
	{ "libdl.so", "dladdr", (void *)tipsy_dladdr },
	{ "libdl.so", "dl_iterate_phdr", (void *)tipsy_dl_iterate_phdr },

	{ "libandroid.so", "ALooper_prepare", (void *)tipsy_ALooper_prepare },
	{ "libandroid.so", "ALooper_pollOnce", (void *)tipsy_ALooper_pollOnce },
	{ "libandroid.so", "ALooper_forThread", (void *)tipsy_ALooper_forThread },
	{ "libandroid.so", "ALooper_addFd", (void *)tipsy_ALooper_addFd },
	{ "libandroid.so", "ALooper_removeFd", (void *)tipsy_ALooper_removeFd },
	{ "libandroid.so", "ALooper_acquire", (void *)tipsy_ALooper_acquire },
	{ "libandroid.so", "ALooper_release", (void *)tipsy_ALooper_release },
	{ "libandroid.so", "ALooper_wake", (void *)tipsy_ALooper_wake },
	{ "libandroid.so", "AAssetManager_fromJava", (void *)tipsy_AAssetManager_fromJava },
	{ "libandroid.so", "AAssetManager_open", (void *)tipsy_AAssetManager_open },
	{ "libandroid.so", "AAsset_close", (void *)tipsy_AAsset_close },
	{ "libandroid.so", "AAsset_getBuffer", (void *)tipsy_AAsset_getBuffer },
	{ "libandroid.so", "AAsset_getLength", (void *)tipsy_AAsset_getLength },
	{ "libandroid.so", "AAsset_openFileDescriptor", (void *)tipsy_AAsset_openFileDescriptor },
	{ "libandroid.so", "AAsset_read", (void *)tipsy_AAsset_read },
	{ "libandroid.so", "AAsset_seek", (void *)tipsy_AAsset_seek },
	{ "libandroid.so", "AConfiguration_new", (void *)tipsy_AConfiguration_new },
	{ "libandroid.so", "AConfiguration_delete", (void *)tipsy_AConfiguration_delete },
	{ "libandroid.so", "AConfiguration_fromAssetManager", (void *)tipsy_AConfiguration_fromAssetManager },
	{ "libandroid.so", "AConfiguration_getLanguage", (void *)tipsy_AConfiguration_getLanguage },
	{ "libandroid.so", "AConfiguration_getCountry", (void *)tipsy_AConfiguration_getCountry },
	{ "libandroid.so", "AConfiguration_getScreenWidthDp", (void *)tipsy_AConfiguration_getScreenWidthDp },
	{ "libandroid.so", "AConfiguration_getScreenHeightDp", (void *)tipsy_AConfiguration_getScreenHeightDp },
	{ "libandroid.so", "AConfiguration_getScreenSize", (void *)tipsy_AConfiguration_getScreenSize },
	{ "libandroid.so", "AConfiguration_getNavHidden", (void *)tipsy_AConfiguration_getNavHidden },
	{ "libandroid.so", "ANativeWindow_fromSurface", (void *)tipsy_ANativeWindow_fromSurface },
	{ "libandroid.so", "ANativeWindow_acquire", (void *)tipsy_ANativeWindow_acquire },
	{ "libandroid.so", "ANativeWindow_release", (void *)tipsy_ANativeWindow_release },
	{ "libandroid.so", "ANativeWindow_getWidth", (void *)tipsy_ANativeWindow_getWidth },
	{ "libandroid.so", "ANativeWindow_getHeight", (void *)tipsy_ANativeWindow_getHeight },
	{ "libandroid.so", "ANativeWindow_getFormat", (void *)tipsy_ANativeWindow_getFormat },
	{ "libandroid.so", "ANativeWindow_setBuffersGeometry", (void *)tipsy_ANativeWindow_setBuffersGeometry },
	{ "libandroid.so", "ANativeWindow_lock", (void *)tipsy_ANativeWindow_lock },
	{ "libandroid.so", "ANativeWindow_unlockAndPost", (void *)tipsy_ANativeWindow_unlockAndPost },

	{ "libEGL.so", "eglCreateWindowSurface", (void *)tipsy_eglCreateWindowSurface },
	{ "libEGL.so", "eglSwapInterval", (void *)tipsy_eglSwapInterval },
	{ "libEGL.so", "eglSwapBuffers", (void *)tipsy_eglSwapBuffers },
	{ "libEGL.so", "eglGetProcAddress", (void *)tipsy_eglGetProcAddress },

	{ "libmediandk.so", "AMediaCodec_createDecoderByType", (void *)tipsy_AMediaCodec_createDecoderByType },
	{ "libmediandk.so", "AMediaCodec_createEncoderByType", (void *)tipsy_AMediaCodec_createEncoderByType },
	{ "libmediandk.so", "AMediaCodec_createCodecByName", (void *)tipsy_AMediaCodec_createCodecByName },
	{ "libmediandk.so", "AMediaCodec_delete", (void *)tipsy_AMediaCodec_delete },
	{ "libmediandk.so", "AMediaCodec_configure", (void *)tipsy_AMediaCodec_configure },
	{ "libmediandk.so", "AMediaCodec_start", (void *)tipsy_AMediaCodec_start },
	{ "libmediandk.so", "AMediaCodec_stop", (void *)tipsy_AMediaCodec_stop },
	{ "libmediandk.so", "AMediaCodec_flush", (void *)tipsy_AMediaCodec_flush },
	{ "libmediandk.so", "AMediaCodec_getInputBuffer", (void *)tipsy_AMediaCodec_getInputBuffer },
	{ "libmediandk.so", "AMediaCodec_getOutputBuffer", (void *)tipsy_AMediaCodec_getOutputBuffer },
	{ "libmediandk.so", "AMediaCodec_dequeueInputBuffer", (void *)tipsy_AMediaCodec_dequeueInputBuffer },
	{ "libmediandk.so", "AMediaCodec_queueInputBuffer", (void *)tipsy_AMediaCodec_queueInputBuffer },
	{ "libmediandk.so", "AMediaCodec_dequeueOutputBuffer", (void *)tipsy_AMediaCodec_dequeueOutputBuffer },
	{ "libmediandk.so", "AMediaCodec_releaseOutputBuffer", (void *)tipsy_AMediaCodec_releaseOutputBuffer },
	{ "libmediandk.so", "AMediaCodec_getOutputFormat", (void *)tipsy_AMediaCodec_getOutputFormat },
	{ "libmediandk.so", "AMediaCodec_getInputFormat", (void *)tipsy_AMediaCodec_getInputFormat },
	{ "libmediandk.so", "AMediaCodec_setOutputSurface", (void *)tipsy_AMediaCodec_setOutputSurface },
	{ "libmediandk.so", "AMediaCodec_releaseOutputBufferAtTime", (void *)tipsy_AMediaCodec_releaseOutputBufferAtTime },
	{ "libmediandk.so", "AMediaCodec_getName", (void *)tipsy_AMediaCodec_getName },
	{ "libmediandk.so", "AMediaCodec_releaseName", (void *)tipsy_AMediaCodec_releaseName },
	{ "libmediandk.so", "AMediaCodec_setParameters", (void *)tipsy_AMediaCodec_setParameters },
	{ "libmediandk.so", "AMediaFormat_new", (void *)tipsy_AMediaFormat_new },
	{ "libmediandk.so", "AMediaFormat_delete", (void *)tipsy_AMediaFormat_delete },
	{ "libmediandk.so", "AMediaFormat_toString", (void *)tipsy_AMediaFormat_toString },
	{ "libmediandk.so", "AMediaFormat_getInt32", (void *)tipsy_AMediaFormat_getInt32 },
	{ "libmediandk.so", "AMediaFormat_getInt64", (void *)tipsy_AMediaFormat_getInt64 },
	{ "libmediandk.so", "AMediaFormat_getFloat", (void *)tipsy_AMediaFormat_getFloat },
	{ "libmediandk.so", "AMediaFormat_getBuffer", (void *)tipsy_AMediaFormat_getBuffer },
	{ "libmediandk.so", "AMediaFormat_getString", (void *)tipsy_AMediaFormat_getString },
	{ "libmediandk.so", "AMediaFormat_setInt32", (void *)tipsy_AMediaFormat_setInt32 },
	{ "libmediandk.so", "AMediaFormat_setInt64", (void *)tipsy_AMediaFormat_setInt64 },
	{ "libmediandk.so", "AMediaFormat_setFloat", (void *)tipsy_AMediaFormat_setFloat },
	{ "libmediandk.so", "AMediaFormat_setString", (void *)tipsy_AMediaFormat_setString },
	{ "libmediandk.so", "AMediaFormat_setBuffer", (void *)tipsy_AMediaFormat_setBuffer },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_AAC_PROFILE", (void *)&AMEDIAFORMAT_KEY_AAC_PROFILE },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_BIT_RATE", (void *)&AMEDIAFORMAT_KEY_BIT_RATE },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_CHANNEL_COUNT", (void *)&AMEDIAFORMAT_KEY_CHANNEL_COUNT },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_CHANNEL_MASK", (void *)&AMEDIAFORMAT_KEY_CHANNEL_MASK },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_COLOR_FORMAT", (void *)&AMEDIAFORMAT_KEY_COLOR_FORMAT },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_DURATION", (void *)&AMEDIAFORMAT_KEY_DURATION },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_FLAC_COMPRESSION_LEVEL", (void *)&AMEDIAFORMAT_KEY_FLAC_COMPRESSION_LEVEL },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_FRAME_RATE", (void *)&AMEDIAFORMAT_KEY_FRAME_RATE },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_HEIGHT", (void *)&AMEDIAFORMAT_KEY_HEIGHT },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_IS_ADTS", (void *)&AMEDIAFORMAT_KEY_IS_ADTS },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_I_FRAME_INTERVAL", (void *)&AMEDIAFORMAT_KEY_I_FRAME_INTERVAL },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_LANGUAGE", (void *)&AMEDIAFORMAT_KEY_LANGUAGE },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_MAX_HEIGHT", (void *)&AMEDIAFORMAT_KEY_MAX_HEIGHT },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_MAX_INPUT_SIZE", (void *)&AMEDIAFORMAT_KEY_MAX_INPUT_SIZE },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_MAX_WIDTH", (void *)&AMEDIAFORMAT_KEY_MAX_WIDTH },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_MIME", (void *)&AMEDIAFORMAT_KEY_MIME },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_SAMPLE_RATE", (void *)&AMEDIAFORMAT_KEY_SAMPLE_RATE },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_WIDTH", (void *)&AMEDIAFORMAT_KEY_WIDTH },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_STRIDE", (void *)&AMEDIAFORMAT_KEY_STRIDE },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_SLICE_HEIGHT", (void *)&AMEDIAFORMAT_KEY_SLICE_HEIGHT },
	{ "libmediandk.so", "AMEDIAFORMAT_KEY_ROTATION", (void *)&AMEDIAFORMAT_KEY_ROTATION },

	{ "libOpenSLES.so", "slCreateEngine", (void *)tipsy_slCreateEngine },
	{ "libOpenSLES.so", "SL_IID_ENGINE", (void *)&SL_IID_ENGINE },
	{ "libOpenSLES.so", "SL_IID_PLAY", (void *)&SL_IID_PLAY },
	{ "libOpenSLES.so", "SL_IID_BUFFERQUEUE", (void *)&SL_IID_BUFFERQUEUE },
	{ "libOpenSLES.so", "SL_IID_VOLUME", (void *)&SL_IID_VOLUME },
	{ "libOpenSLES.so", "SL_IID_OUTPUTMIX", (void *)&SL_IID_OUTPUTMIX },
	{ "libOpenSLES.so", "SL_IID_ANDROIDCONFIGURATION", (void *)&SL_IID_ANDROIDCONFIGURATION },
	{ "libOpenSLES.so", "SL_IID_ANDROIDSIMPLEBUFFERQUEUE", (void *)&SL_IID_ANDROIDSIMPLEBUFFERQUEUE },
	{ "libOpenSLES.so", "SL_IID_RECORD", (void *)&SL_IID_RECORD },
	{ "libOpenSLES.so", "SL_IID_SEEK", (void *)&SL_IID_SEEK },
	{ "libOpenSLES.so", "SL_IID_PREFETCHSTATUS", (void *)&SL_IID_PREFETCHSTATUS },
	{ "libOpenSLES.so", "SL_IID_ANDROIDBUFFERQUEUESOURCE", (void *)&SL_IID_ANDROIDBUFFERQUEUESOURCE },

	{ "libOpenMAXAL.so", "xaCreateEngine", (void *)tipsy_xaCreateEngine },
	{ "libOpenMAXAL.so", "XA_IID_ENGINE", (void *)&XA_IID_ENGINE },
	{ "libOpenMAXAL.so", "XA_IID_PLAY", (void *)&XA_IID_PLAY },
	{ "libOpenMAXAL.so", "XA_IID_OUTPUTMIX", (void *)&XA_IID_OUTPUTMIX },

	{ "libjnigraphics.so", "AndroidBitmap_getInfo", (void *)tipsy_AndroidBitmap_getInfo },
	{ "libjnigraphics.so", "AndroidBitmap_lockPixels", (void *)tipsy_AndroidBitmap_lockPixels },
	{ "libjnigraphics.so", "AndroidBitmap_unlockPixels", (void *)tipsy_AndroidBitmap_unlockPixels },
};

void *tipsy_android_lookup(const char *lib, const char *name)
{
	size_t i;
	if (name == NULL) {
		return NULL;
	}
	for (i = 0; i < sizeof table / sizeof table[0]; i++) {
		if (strcmp(table[i].name, name) != 0) {
			continue;
		}
		if (tipsy_bionic_sync_is_export(table[i].name) &&
		    !tipsy_bionic_sync_enabled()) {
			continue;
		}
		if (lib == NULL || lib[0] == '\0' || strcmp(table[i].lib, lib) == 0) {
			return table[i].addr;
		}
	}
	if (lib != NULL && strcmp(lib, "libEGL.so") == 0) {
		return tipsy_egl_dlsym(name);
	}
	if (lib != NULL && strcmp(lib, "libGLESv2.so") == 0) {
		return tipsy_gles_dlsym(name);
	}
	if (lib != NULL && (strcmp(lib, "libvulkan.so") == 0 || strcmp(lib, "libvulkan.so.1") == 0)) {
		return tipsy_vk_dlsym(name);
	}
	if ((lib == NULL || lib[0] == '\0') && name[0] == 'v' && name[1] == 'k') {
		return tipsy_vk_dlsym(name);
	}
	return NULL;
}

void *tipsy_media_generic_err(void)
{
	return (void *)tipsy_media_stub_err;
}

void *tipsy_media_generic_null(void)
{
	return (void *)tipsy_media_stub_null;
}
