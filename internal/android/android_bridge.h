/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_ANDROID_BRIDGE_H
#define TIPSY_ANDROID_BRIDGE_H

#include <stdint.h>
#include <stddef.h>
#include <stdio.h>

#ifdef __cplusplus
extern "C" {
#endif

#define TIPSY_ANW_MAGIC 0x54595359574E4401ull /* TYSYWND\x01 */
#define TIPSY_ASSET_MAGIC 0x5459415353540001ull
#define TIPSY_AMGR_MAGIC 0x5459414D47520001ull
#define TIPSY_ACFG_MAGIC 0x5459414346470001ull
#define TIPSY_ALOOPER_MAGIC 0x54594C4F4F500001ull
#define TIPSY_DLHANDLE_MAGIC 0x5459444C484E0001ull

#define ALOOPER_POLL_WAKE (-1)
#define ALOOPER_POLL_CALLBACK (-2)
#define ALOOPER_POLL_TIMEOUT (-3)
#define ALOOPER_POLL_ERROR (-4)

#define ALOOPER_PREPARE_ALLOW_NON_CALLBACKS 1

#define ALOOPER_EVENT_INPUT 1
#define ALOOPER_EVENT_OUTPUT 2
#define ALOOPER_EVENT_ERROR 4
#define ALOOPER_EVENT_HANGUP 8
#define ALOOPER_EVENT_INVALID 16

#define AASSET_MODE_UNKNOWN 0
#define AASSET_MODE_RANDOM 1
#define AASSET_MODE_STREAMING 2
#define AASSET_MODE_BUFFER 3

#define ACONFIGURATION_NAVHIDDEN_NO 0x0002
#define ACONFIGURATION_SCREENSIZE_XLARGE 0x0004
#define WINDOW_FORMAT_RGBA_8888 1

#define AMEDIA_ERROR_UNSUPPORTED ((int32_t)-10002)
#define ANDROID_BITMAP_RESULT_ALLOCATION_FAILED (-2)

typedef int (*ALooper_callbackFunc)(int fd, int events, void *data);

typedef struct TipsyNativeWindow {
	uint64_t magic;
	int32_t width;
	int32_t height;
	int32_t format;
	int32_t refs;
	uintptr_t native_handle;
	int32_t locked;
	void *lock_bits;
	int32_t stride;
} TipsyNativeWindow;

typedef struct AAsset {
	uint64_t magic;
	void *buffer;
	int64_t length;
	int64_t pos;
	int fd;
	int owned;
} AAsset;

typedef struct AAssetManager {
	uint64_t magic;
} AAssetManager;

typedef struct AConfiguration {
	uint64_t magic;
	char language[2];
	char country[2];
	int32_t width_dp;
	int32_t height_dp;
	int32_t screen_size;
	int32_t nav_hidden;
} AConfiguration;

typedef struct ALooper ALooper;

void *tipsy_host_dlsym(const char *name);
void *tipsy_android_lookup(const char *lib, const char *name);

void *tipsy_ANativeWindow_new(int32_t width, int32_t height, uintptr_t native_handle);
void tipsy_ANativeWindow_set_handle(void *win, uintptr_t native_handle);
void tipsy_set_default_window(void *win);
uintptr_t tipsy_ANativeWindow_get_handle(void *win);
int tipsy_is_anative_window(const void *win);
int32_t tipsy_ANativeWindow_getWidth(void *window);
int32_t tipsy_ANativeWindow_getHeight(void *window);
int32_t tipsy_ANativeWindow_getFormat(void *window);
int32_t tipsy_ANativeWindow_setBuffersGeometry(void *window, int32_t width, int32_t height, int32_t format);

ALooper *tipsy_ALooper_forThread(void);
ALooper *tipsy_ALooper_prepare(int opts);
int tipsy_ALooper_pollOnce(int timeoutMillis, int *outFd, int *outEvents, void **outData);

int tipsy_native_main_idle(int timeout_ms);
void tipsy_native_main_wake(void);
int tipsy_pthread_cond_wait(void *cond, void *mutex);
int tipsy_pthread_cond_timedwait(void *cond, void *mutex, void *abstime);
int tipsy_test_cond_wait_polls_looper(void);

void tipsy_bionic_compat_init(void);
long tipsy_sysconf(int name);
int tipsy_fflush_bionic_index(int idx);

/* Exact-match "./exe/cacert.pem" -> FilesDir bundle shim (bionic_compat.c).
 * tipsy_open/tipsy_openat are variadic like their glibc namesakes; the
 * _2 variants are the bionic FORTIFY entries. tipsy_cacert_wanted exposes
 * the single remapped input so Go tests can pin C/Go agreement. */
int tipsy_open(const char *path, int flags, ...);
int tipsy_open_2(const char *path, int flags);
int tipsy_openat(int dirfd, const char *path, int flags, ...);
int tipsy_openat_2(int dirfd, const char *path, int flags);
FILE *tipsy_fopen(const char *path, const char *mode);
const char *tipsy_cacert_wanted(void);

int tipsy_sigaction(int sig, const void *act, void *oact);
long tipsy_rt_sigaction(int sig, const void *act, void *oact, size_t sigsetsize);
size_t tipsy_bionic_sigaction_size(void);
size_t tipsy_glibc_sigaction_size(void);
int tipsy_test_glibc_sigaction_smashes_bionic_act(void);
int tipsy_test_bionic_sigaction_query_canary(void);
int tipsy_test_bionic_sigaction_set_query(void);
int tipsy_test_rt_sigaction_query_canary(void);

int tipsy_getaddrinfo(const char *node, const char *service, const void *hints, void **res);
void tipsy_freeaddrinfo(void *res);
int tipsy_getnameinfo(const void *sa, uint32_t salen, char *host, size_t hostlen, char *serv, size_t servlen, int flags);
const char *tipsy_gai_strerror(int errcode);
int tipsy_test_getaddrinfo_numeric_loopback(void);
int tipsy_test_getaddrinfo_bionic_addrconfig(void);
int tipsy_test_glibc_untranslated_addrconfig(void);
int tipsy_test_getaddrinfo_eai_noname(void);
int tipsy_test_getnameinfo_numeric(void);
int tipsy_test_glibc_bionic_ai_addr_null(void);

AAssetManager *tipsy_AAssetManager_singleton(void);
AAsset *tipsy_AAsset_from_buffer(void *buf, int64_t len, int owned, int fd);

void *tipsy_dlhandle_new(const char *soname);
const char *tipsy_dlhandle_soname(void *handle);
int tipsy_dlhandle_valid(void *handle);
void tipsy_dlhandle_free(void *handle);
void tipsy_register_image(uintptr_t load_bias, const char *name);
int tipsy_dl_iterate_count(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_ANDROID_BRIDGE_H */
