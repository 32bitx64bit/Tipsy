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

enum {
	TIPSY_STUTTER_WAIT_COND = 0,
	TIPSY_STUTTER_WAIT_TIMEDCOND = 1,
	TIPSY_STUTTER_WAIT_FUTEX_PUMP = 2,
	TIPSY_STUTTER_WAIT_PATHS = 3
};

typedef struct TipsyStutterWaitPathStats {
	uint64_t calls;
	uint64_t slices;
	uint64_t samples;
	uint64_t sampled_ns;
	uint64_t max_ns;
} TipsyStutterWaitPathStats;

typedef struct TipsyStutterWaitStats {
	TipsyStutterWaitPathStats path[TIPSY_STUTTER_WAIT_PATHS];
} TipsyStutterWaitStats;

/* Opt-in bionic ABI telemetry. These are resolver-only guest exports: when
 * disabled, libroblox keeps its direct glibc binding and host locks never
 * enter this surface. */
enum {
	TIPSY_BIONIC_SYNC_THREAD_RBX_WORKER = 0,
	TIPSY_BIONIC_SYNC_THREAD_MAIN = 1,
	TIPSY_BIONIC_SYNC_THREAD_OTHER = 2,
	TIPSY_BIONIC_SYNC_THREADS = 3,
	TIPSY_BIONIC_SYNC_MODULE_ROBLOX = 0,
	TIPSY_BIONIC_SYNC_MODULE_OTHER = 1,
	TIPSY_BIONIC_SYNC_MODULE_UNKNOWN = 2,
	TIPSY_BIONIC_SYNC_MODULES = 3,
	TIPSY_BIONIC_SYNC_MUTEX_LOCK = 0,
	TIPSY_BIONIC_SYNC_MUTEX_TRYLOCK = 1,
	TIPSY_BIONIC_SYNC_MUTEX_TIMEDLOCK = 2,
	TIPSY_BIONIC_SYNC_MUTEX_UNLOCK = 3,
	TIPSY_BIONIC_SYNC_COND_SIGNAL = 4,
	TIPSY_BIONIC_SYNC_COND_BROADCAST = 5,
	TIPSY_BIONIC_SYNC_PTHREAD_SETAFFINITY = 6,
	TIPSY_BIONIC_SYNC_SCHED_YIELD = 7,
	TIPSY_BIONIC_SYNC_SCHED_GETAFFINITY = 8,
	TIPSY_BIONIC_SYNC_SCHED_SETAFFINITY = 9,
	TIPSY_BIONIC_SYNC_NICE = 10,
	TIPSY_BIONIC_SYNC_OPS = 11
};

typedef struct TipsyBionicSyncPathStats {
	uint64_t calls;
	uint64_t contention;
	uint64_t errors;
	uint64_t samples;
	uint64_t sampled_ns;
	uint64_t max_ns;
} TipsyBionicSyncPathStats;

typedef struct TipsyBionicSyncStats {
	TipsyBionicSyncPathStats path[TIPSY_BIONIC_SYNC_THREADS]
		[TIPSY_BIONIC_SYNC_MODULES][TIPSY_BIONIC_SYNC_OPS];
} TipsyBionicSyncStats;

void *tipsy_host_dlsym(const char *name);
void *tipsy_host_dlsym_library(const char *lib, const char *name);
void *tipsy_android_lookup(const char *lib, const char *name);

/* Android EGL presentation policy. VSync off requests interval zero and
 * VSync on requests interval one, independently of the client's interval.
 * A rejected policy interval falls back to the exact client request. */
void tipsy_egl_set_vsync(int enabled);
int tipsy_egl_vsync_enabled(void);
void tipsy_egl_set_present_stats(int enabled);
int tipsy_egl_present_stats_enabled(void);
void tipsy_egl_swap_stats(uint64_t *successful_swaps, uint64_t *first_ns,
                          uint64_t *last_ns);
void tipsy_egl_reset_swap_stats(void);
void tipsy_test_egl_record_swap(uint64_t now_ns);
void tipsy_test_egl_note_successful_swap(void);
int tipsy_test_egl_proc_is_wrapped(const char *name);
int tipsy_test_egl_swap_interval_policy(int vsync, int requested,
										int policy_result, int policy_error,
										int client_result, int client_error,
                                        int *first_interval, int *second_interval,
                                        int *calls, int *reported_error);

/* liblog.so: C-side VERBOSE/DEBUG skip when slog Debug is off. */
void tipsy_android_set_debug_log(int enabled);
int tipsy_android_debug_log_enabled(void);
uint64_t tipsy_android_log_skip_count(void);
void tipsy_android_reset_log_counters(void);
int tipsy_test_android_log_print(int prio, const char *tag, const char *text);
int tipsy_test_android_log_write(int prio, const char *tag, const char *text);
int tipsy_test_android_log_vprint(int prio, const char *tag, const char *text);
void tipsy_test_android_log_assert(const char *cond, const char *tag, const char *text);
int tipsy_test_android_log_buf_write(int bufID, int prio, const char *tag, const char *text);

/* Android Vulkan loader adapter (identity-handle WSI). */
void *tipsy_vk_dlsym(const char *name);
int tipsy_vk_bind_wsi(uintptr_t display, uintptr_t xid);
void tipsy_vk_unbind_wsi(void);
int tipsy_vk_wsi_bound(void);
void tipsy_vk_set_vsync(int enabled);
int tipsy_vk_vsync_enabled(void);
void tipsy_vk_set_present_stats(int enabled);
int tipsy_vk_present_stats_enabled(void);
uint64_t tipsy_vk_set_present_timing(int enabled);
uint32_t tipsy_vk_present_timing_snapshot(uint64_t after, uint64_t *out_ns,
	uint32_t capacity, uint64_t *out_cursor, uint64_t *out_overwritten);
void tipsy_vk_present_stats(uint64_t *successful_presents, uint64_t *first_ns,
                            uint64_t *last_ns);
void tipsy_vk_reset_present_stats(void);
void tipsy_test_vk_record_present(uint64_t now_ns);
void tipsy_test_vk_note_present_success(void);
void tipsy_test_vk_note_present_result(int32_t result, uint64_t now_ns);
int tipsy_test_vk_proc_is_wrapped(const char *name);
int tipsy_test_vk_proc_is_host_passthrough(const char *name);
int tipsy_test_vk_android_surface_advertised(const char **host_names, uint32_t n);
int tipsy_test_vk_rewrite_enabled_extensions(const char **in, uint32_t n, int has_xcb,
                                            int has_xlib, const char **out);
int tipsy_test_vk_filter_present_modes(const uint32_t *in, uint32_t n, int vsync,
                                      uint32_t *out, uint32_t *out_n);
int tipsy_test_vk_create_swapchain_policy(int vsync, uint32_t requested,
                                         int32_t policy_result, int32_t fallback_result,
                                         uint32_t *first_mode, uint32_t *second_mode,
                                         int *calls, int32_t *final_result);
int tipsy_vk_host_has_xcb_surface(void);
int tipsy_vk_host_has_xlib_surface(void);

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
int tipsy_looper_can_park(void);
int tipsy_looper_park_futex(int *uaddr);
void tipsy_looper_unpark_futex(void);
int tipsy_looper_consume_wake(void);
int tipsy_pthread_cond_wait(void *cond, void *mutex);
int tipsy_pthread_cond_timedwait(void *cond, void *mutex, void *abstime);
int tipsy_test_cond_wait_polls_looper(void);
int tipsy_test_cond_wait_wake_unblocks(void);
int tipsy_test_cond_wait_lost_wakeup(void);
int tipsy_test_cond_wait_fallback_without_watcher(void);
int tipsy_test_looper_watcher_shutdown(void);
int tipsy_test_idle_unblocks_on_wake(void);
int tipsy_test_futex_real_wake_vs_looper_wake(void);
void tipsy_stutter_wait_set_enabled(int enabled);
int tipsy_stutter_wait_enabled(void);
uint64_t tipsy_stutter_wait_begin(int path);
void tipsy_stutter_wait_slice(int path);
void tipsy_stutter_wait_end(int path, uint64_t started_ns);
void tipsy_stutter_wait_snapshot(TipsyStutterWaitStats *out, int reset);
uint64_t tipsy_test_stutter_wait_clock_calls(void);
void tipsy_test_stutter_wait_record(int path, uint64_t duration_ns, uint64_t slices);
void tipsy_test_stutter_wait_reset_tls(void);

void tipsy_bionic_compat_init(void);
long tipsy_sysconf(int name);
int tipsy_fflush_bionic_index(int idx);

void tipsy_bionic_sync_set_enabled(int enabled);
int tipsy_bionic_sync_enabled(void);
int tipsy_bionic_sync_is_export(const char *name);
void tipsy_bionic_sync_snapshot(TipsyBionicSyncStats *out, int reset);
uint64_t tipsy_test_bionic_sync_clock_calls(void);
void tipsy_test_bionic_sync_reset_tls(void);
int tipsy_test_bionic_sync_mutex(void);
int tipsy_test_bionic_sync_condition(void);
int tipsy_test_bionic_sync_host_dladdr(uintptr_t address);
int tipsy_test_bionic_sync_named_call(uintptr_t entry, uintptr_t function, const char *name);
int tipsy_test_bionic_sync_module_class(uintptr_t address);
int tipsy_test_bionic_sync_rename_call(uintptr_t entry, uintptr_t function);

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

void *tipsy_dlopen(const char *filename, int flags);
void *tipsy_dlsym(void *handle, const char *symbol);
int tipsy_dlclose(void *handle);
char *tipsy_dlerror(void);
void *tipsy_dlhandle_new(const char *soname);
const char *tipsy_dlhandle_soname(void *handle);
int tipsy_dlhandle_valid(void *handle);
void tipsy_dlhandle_free(void *handle);
void tipsy_register_image(uintptr_t load_bias, const char *name);
void tipsy_unregister_image(uintptr_t load_bias);
uint64_t tipsy_image_generation(void);
int tipsy_image_code_range(uintptr_t address, uintptr_t *start, uintptr_t *end, uint64_t *generation);
int tipsy_dl_iterate_count(void);

/* OpenSL ES host bridge test probes. These exercise the same public interface
 * vtables used by the client while selecting a deterministic in-memory host. */
int tipsy_audio_test_playback(uint32_t rate, uint32_t channels, uint32_t bytes,
                              uint64_t *written, uint32_t *callbacks);
int tipsy_audio_test_capture(uint32_t rate, uint32_t channels, uint32_t bytes,
                             uint64_t *read_bytes, uint32_t *callbacks);
int tipsy_audio_test_invalid_format(void);
int tipsy_audio_test_retry(uint32_t *opens, uint32_t *writes, uint32_t *callbacks);
int tipsy_audio_test_host_playback(uint32_t rate, uint32_t channels, uint32_t bytes,
                                   uint64_t *written, uint32_t *callbacks);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_ANDROID_BRIDGE_H */
