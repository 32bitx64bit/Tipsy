/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * libandroid.so / liblog.so NDK surface: log, looper, window, config, assets.
 */
#include "android_bridge.h"

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <time.h>
#include <unistd.h>
#include <sys/syscall.h>

extern void GoAndroid_LogWrite(int prio, char *tag, char *text);
extern void GoAndroid_LogMissing(char *name);
extern void GoAndroid_AbortMessage(char *msg);
extern AAsset *GoAndroid_AssetOpen(char *filename, int mode);

#define MAX_LOOPER_FDS 64

struct FdRec {
	int fd;
	int ident;
	int events;
	ALooper_callbackFunc callback;
	void *data;
	int used;
};

struct ALooper {
	uint64_t magic;
	int refs;
	int epfd;
	int wake_r;
	int wake_w;
	pthread_mutex_t lock;
	struct FdRec fds[MAX_LOOPER_FDS];
};

static __thread ALooper *tls_looper;
static ALooper *g_ui_looper;
static int g_cond_wait_poll;
static int g_logged_nested_poll;

struct wait_diag_path {
	_Atomic uint64_t calls;
	_Atomic uint64_t slices;
	_Atomic uint64_t samples;
	_Atomic uint64_t sampled_ns;
	_Atomic uint64_t max_ns;
};

static _Atomic int g_stutter_wait_enabled;
static _Atomic uint64_t g_stutter_wait_clock_calls;
static struct wait_diag_path g_stutter_wait[TIPSY_STUTTER_WAIT_PATHS];
static __thread uint32_t tls_stutter_wait_sample[TIPSY_STUTTER_WAIT_PATHS];

static int valid_wait_path(int path)
{
	return path >= 0 && path < TIPSY_STUTTER_WAIT_PATHS;
}

static void wait_diag_max(_Atomic uint64_t *dst, uint64_t value)
{
	uint64_t old = atomic_load_explicit(dst, memory_order_relaxed);

	while (old < value && !atomic_compare_exchange_weak_explicit(dst, &old, value,
		memory_order_relaxed, memory_order_relaxed)) {
	}
}

void tipsy_stutter_wait_set_enabled(int enabled)
{
	atomic_store_explicit(&g_stutter_wait_enabled, enabled != 0, memory_order_relaxed);
}

int tipsy_stutter_wait_enabled(void)
{
	return atomic_load_explicit(&g_stutter_wait_enabled, memory_order_relaxed);
}

uint64_t tipsy_stutter_wait_begin(int path)
{
	struct timespec ts;

	if (!atomic_load_explicit(&g_stutter_wait_enabled, memory_order_relaxed) ||
	    !valid_wait_path(path)) {
		return 0;
	}
	atomic_fetch_add_explicit(&g_stutter_wait[path].calls, 1, memory_order_relaxed);
	/* One duration sample per 64 entries. Counts and slices remain exact. */
	if ((tls_stutter_wait_sample[path]++ & 63u) != 0) {
		return 0;
	}
	atomic_fetch_add_explicit(&g_stutter_wait_clock_calls, 1, memory_order_relaxed);
	if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
		return 0;
	}
	/* Reserve zero as the unsampled token. */
	return (uint64_t)ts.tv_sec * 1000000000u + (uint64_t)ts.tv_nsec + 1u;
}

void tipsy_stutter_wait_slice(int path)
{
	if (!atomic_load_explicit(&g_stutter_wait_enabled, memory_order_relaxed) ||
	    !valid_wait_path(path)) {
		return;
	}
	atomic_fetch_add_explicit(&g_stutter_wait[path].slices, 1, memory_order_relaxed);
}

void tipsy_stutter_wait_end(int path, uint64_t started_ns)
{
	struct timespec ts;
	uint64_t ended_ns;
	uint64_t elapsed;

	if (started_ns == 0 || !valid_wait_path(path)) {
		return;
	}
	atomic_fetch_add_explicit(&g_stutter_wait_clock_calls, 1, memory_order_relaxed);
	if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
		return;
	}
	ended_ns = (uint64_t)ts.tv_sec * 1000000000u + (uint64_t)ts.tv_nsec + 1u;
	elapsed = ended_ns >= started_ns ? ended_ns - started_ns : 0;
	atomic_fetch_add_explicit(&g_stutter_wait[path].samples, 1, memory_order_relaxed);
	atomic_fetch_add_explicit(&g_stutter_wait[path].sampled_ns, elapsed, memory_order_relaxed);
	wait_diag_max(&g_stutter_wait[path].max_ns, elapsed);
}

void tipsy_stutter_wait_snapshot(TipsyStutterWaitStats *out, int reset)
{
	int path;

	if (out == NULL) {
		return;
	}
	memset(out, 0, sizeof(*out));
	for (path = 0; path < TIPSY_STUTTER_WAIT_PATHS; path++) {
#define WAIT_STAT(field) (reset ? \
	atomic_exchange_explicit(&g_stutter_wait[path].field, 0, memory_order_relaxed) : \
	atomic_load_explicit(&g_stutter_wait[path].field, memory_order_relaxed))
		out->path[path].calls = WAIT_STAT(calls);
		out->path[path].slices = WAIT_STAT(slices);
		out->path[path].samples = WAIT_STAT(samples);
		out->path[path].sampled_ns = WAIT_STAT(sampled_ns);
		out->path[path].max_ns = WAIT_STAT(max_ns);
#undef WAIT_STAT
	}
}

uint64_t tipsy_test_stutter_wait_clock_calls(void)
{
	return atomic_load_explicit(&g_stutter_wait_clock_calls, memory_order_relaxed);
}

void tipsy_test_stutter_wait_record(int path, uint64_t duration_ns, uint64_t slices)
{
	uint64_t started;
	uint64_t n;

	started = tipsy_stutter_wait_begin(path);
	for (n = 0; n < slices; n++) {
		tipsy_stutter_wait_slice(path);
	}
	if (started != 0) {
		atomic_fetch_add_explicit(&g_stutter_wait[path].samples, 1, memory_order_relaxed);
		atomic_fetch_add_explicit(&g_stutter_wait[path].sampled_ns, duration_ns, memory_order_relaxed);
		wait_diag_max(&g_stutter_wait[path].max_ns, duration_ns);
	}
}

void tipsy_test_stutter_wait_reset_tls(void)
{
	memset(tls_stutter_wait_sample, 0, sizeof(tls_stutter_wait_sample));
}

#define ANDROID_LOG_VERBOSE 2
#define ANDROID_LOG_DEBUG 3

static const char jni_roblox_settings_tag[] = "rbx.JNIRobloxSettings";

/* C-visible slog Debug gate. Default off so VERBOSE/DEBUG skip without a Go call. */
static _Atomic int android_debug_log_enabled;
static _Atomic uint64_t android_log_skip_count;

void tipsy_android_set_debug_log(int enabled)
{
	atomic_store_explicit(&android_debug_log_enabled, enabled != 0, memory_order_relaxed);
}

int tipsy_android_debug_log_enabled(void)
{
	return atomic_load_explicit(&android_debug_log_enabled, memory_order_relaxed);
}

uint64_t tipsy_android_log_skip_count(void)
{
	return atomic_load_explicit(&android_log_skip_count, memory_order_relaxed);
}

void tipsy_android_reset_log_counters(void)
{
	atomic_store_explicit(&android_log_skip_count, 0, memory_order_relaxed);
}

static int android_log_is_settings_tag(const char *tag)
{
	return tag != NULL && strcmp(tag, jni_roblox_settings_tag) == 0;
}

static int android_log_should_drop(int prio, const char *tag)
{
	if (prio != ANDROID_LOG_VERBOSE && prio != ANDROID_LOG_DEBUG) {
		return 0;
	}
	if (android_log_is_settings_tag(tag)) {
		return 0;
	}
	return atomic_load_explicit(&android_debug_log_enabled, memory_order_relaxed) == 0;
}
static AAssetManager g_amgr = { .magic = TIPSY_AMGR_MAGIC };

int tipsy_on_native_main(void) __attribute__((weak));

static int on_native_main(void)
{
	return tipsy_on_native_main != NULL && tipsy_on_native_main() != 0;
}

int tipsy_is_anative_window(const void *win)
{
	const TipsyNativeWindow *w = (const TipsyNativeWindow *)win;
	return w != NULL && w->magic == TIPSY_ANW_MAGIC;
}

void *tipsy_ANativeWindow_new(int32_t width, int32_t height, uintptr_t native_handle)
{
	TipsyNativeWindow *w = calloc(1, sizeof(*w));
	if (w == NULL) {
		return NULL;
	}
	w->magic = TIPSY_ANW_MAGIC;
	w->width = width > 0 ? width : 1920;
	w->height = height > 0 ? height : 1080;
	w->format = WINDOW_FORMAT_RGBA_8888;
	w->refs = 1;
	w->native_handle = native_handle;
	return w;
}

void tipsy_ANativeWindow_set_handle(void *win, uintptr_t native_handle)
{
	TipsyNativeWindow *w = (TipsyNativeWindow *)win;
	if (!tipsy_is_anative_window(w)) {
		return;
	}
	w->native_handle = native_handle;
}

uintptr_t tipsy_ANativeWindow_get_handle(void *win)
{
	TipsyNativeWindow *w = (TipsyNativeWindow *)win;
	if (!tipsy_is_anative_window(w)) {
		return (uintptr_t)win;
	}
	return w->native_handle;
}

static TipsyNativeWindow *as_window(void *win)
{
	if (!tipsy_is_anative_window(win)) {
		return NULL;
	}
	return (TipsyNativeWindow *)win;
}

static TipsyNativeWindow *g_bound_window;

void tipsy_set_default_window(void *win)
{
	TipsyNativeWindow *w = as_window(win);
	if (w != NULL) {
		g_bound_window = w;
	}
}

void *tipsy_ANativeWindow_fromSurface(void *env, void *surface)
{
	(void)env;
	(void)surface;
	if (g_bound_window != NULL) {
		__sync_add_and_fetch(&g_bound_window->refs, 1);
		return g_bound_window;
	}
	static TipsyNativeWindow *def;
	if (def == NULL) {
		def = tipsy_ANativeWindow_new(1920, 1080, 0);
	} else {
		__sync_add_and_fetch(&def->refs, 1);
	}
	return def;
}

void tipsy_ANativeWindow_acquire(void *window)
{
	TipsyNativeWindow *w = as_window(window);
	if (w != NULL) {
		__sync_add_and_fetch(&w->refs, 1);
	}
}

void tipsy_ANativeWindow_release(void *window)
{
	TipsyNativeWindow *w = as_window(window);
	if (w == NULL) {
		return;
	}
	if (__sync_sub_and_fetch(&w->refs, 1) == 0) {
		free(w->lock_bits);
		w->magic = 0;
		free(w);
	}
}

int32_t tipsy_ANativeWindow_getWidth(void *window)
{
	TipsyNativeWindow *w = as_window(window);
	return w != NULL ? w->width : 0;
}

int32_t tipsy_ANativeWindow_getHeight(void *window)
{
	TipsyNativeWindow *w = as_window(window);
	return w != NULL ? w->height : 0;
}

int32_t tipsy_ANativeWindow_getFormat(void *window)
{
	TipsyNativeWindow *w = as_window(window);
	return w != NULL ? w->format : 0;
}

int32_t tipsy_ANativeWindow_setBuffersGeometry(void *window, int32_t width, int32_t height, int32_t format)
{
	TipsyNativeWindow *w = as_window(window);
	if (w == NULL) {
		return -1;
	}
	if (width > 0) {
		w->width = width;
	}
	if (height > 0) {
		w->height = height;
	}
	if (format > 0) {
		w->format = format;
	}
	return 0;
}

int32_t tipsy_ANativeWindow_lock(void *window, void *outBuffer, void *inOutDirty)
{
	(void)outBuffer;
	(void)inOutDirty;
	GoAndroid_LogMissing("ANativeWindow_lock");
	TipsyNativeWindow *w = as_window(window);
	if (w == NULL) {
		return -1;
	}
	w->locked = 1;
	return -1; /* stub until graphics wires a real buffer */
}

int32_t tipsy_ANativeWindow_unlockAndPost(void *window)
{
	TipsyNativeWindow *w = as_window(window);
	if (w == NULL) {
		return -1;
	}
	w->locked = 0;
	GoAndroid_LogMissing("ANativeWindow_unlockAndPost");
	return -1;
}

int tipsy_android_log_write(int prio, const char *tag, const char *text)
{
	if (android_log_should_drop(prio, tag)) {
		atomic_fetch_add_explicit(&android_log_skip_count, 1, memory_order_relaxed);
		return 1;
	}
	GoAndroid_LogWrite(prio, (char *)(tag ? tag : ""), (char *)(text ? text : ""));
	return 1;
}

int tipsy_android_log_print(int prio, const char *tag, const char *fmt, ...)
{
	char buf[2048];
	va_list ap;
	if (android_log_should_drop(prio, tag)) {
		atomic_fetch_add_explicit(&android_log_skip_count, 1, memory_order_relaxed);
		return 1;
	}
	va_start(ap, fmt);
	vsnprintf(buf, sizeof buf, fmt ? fmt : "", ap);
	va_end(ap);
	return tipsy_android_log_write(prio, tag, buf);
}

int tipsy_android_log_vprint(int prio, const char *tag, const char *fmt, va_list ap)
{
	char buf[2048];
	if (android_log_should_drop(prio, tag)) {
		atomic_fetch_add_explicit(&android_log_skip_count, 1, memory_order_relaxed);
		return 1;
	}
	vsnprintf(buf, sizeof buf, fmt ? fmt : "", ap);
	return tipsy_android_log_write(prio, tag, buf);
}

void tipsy_android_log_assert(const char *cond, const char *tag, const char *fmt, ...)
{
	char buf[2048];
	va_list ap;
	va_start(ap, fmt);
	vsnprintf(buf, sizeof buf, fmt ? fmt : "", ap);
	va_end(ap);
	tipsy_android_log_write(7 /* FATAL */, tag, buf);
	(void)cond;
	/* Do not abort the host process. */
}

int tipsy_android_log_buf_write(int bufID, int prio, const char *tag, const char *text)
{
	(void)bufID;
	return tipsy_android_log_write(prio, tag, text);
}

int tipsy_test_android_log_print(int prio, const char *tag, const char *text)
{
	return tipsy_android_log_print(prio, tag, "%s", text ? text : "");
}

int tipsy_test_android_log_write(int prio, const char *tag, const char *text)
{
	return tipsy_android_log_write(prio, tag, text);
}

static int android_log_vprint_wrap(int prio, const char *tag, const char *fmt, ...)
{
	va_list ap;
	int rc;
	va_start(ap, fmt);
	rc = tipsy_android_log_vprint(prio, tag, fmt, ap);
	va_end(ap);
	return rc;
}

int tipsy_test_android_log_vprint(int prio, const char *tag, const char *text)
{
	return android_log_vprint_wrap(prio, tag, "%s", text ? text : "");
}

void tipsy_test_android_log_assert(const char *cond, const char *tag, const char *text)
{
	tipsy_android_log_assert(cond, tag, "%s", text ? text : "");
}

int tipsy_test_android_log_buf_write(int bufID, int prio, const char *tag, const char *text)
{
	return tipsy_android_log_buf_write(bufID, prio, tag, text);
}

void tipsy_android_set_abort_message(const char *msg)
{
	GoAndroid_AbortMessage((char *)(msg ? msg : ""));
}

int tipsy_system_property_get(const char *name, char *value)
{
	(void)name;
	if (value != NULL) {
		value[0] = '\0';
	}
	return 0;
}

static ALooper *looper_create(void)
{
	ALooper *l = calloc(1, sizeof(*l));
	int fds[2];
	if (l == NULL) {
		return NULL;
	}
	l->magic = TIPSY_ALOOPER_MAGIC;
	l->refs = 1;
	l->epfd = epoll_create1(EPOLL_CLOEXEC);
	if (l->epfd < 0) {
		free(l);
		return NULL;
	}
	if (pipe2(fds, O_CLOEXEC | O_NONBLOCK) != 0) {
		close(l->epfd);
		free(l);
		return NULL;
	}
	l->wake_r = fds[0];
	l->wake_w = fds[1];
	pthread_mutex_init(&l->lock, NULL);
	{
		struct epoll_event ev;
		memset(&ev, 0, sizeof ev);
		ev.events = EPOLLIN;
		ev.data.fd = l->wake_r;
		epoll_ctl(l->epfd, EPOLL_CTL_ADD, l->wake_r, &ev);
	}
	return l;
}

static void looper_free(ALooper *l)
{
	int i;
	if (l == NULL) {
		return;
	}
	for (i = 0; i < MAX_LOOPER_FDS; i++) {
		if (l->fds[i].used) {
			epoll_ctl(l->epfd, EPOLL_CTL_DEL, l->fds[i].fd, NULL);
		}
	}
	close(l->wake_r);
	close(l->wake_w);
	close(l->epfd);
	pthread_mutex_destroy(&l->lock);
	l->magic = 0;
	free(l);
}

ALooper *tipsy_ALooper_forThread(void)
{
	/* Android's Java main thread always has a Looper before GameActivity
	 * runs. Our C "Main" pthread is that stand-in; create on first ask. */
	if (tls_looper == NULL) {
		return tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	}
	return tls_looper;
}

ALooper *tipsy_ALooper_prepare(int opts)
{
	(void)opts;
	if (tls_looper == NULL) {
		tls_looper = looper_create();
		/* Only the C "Main" pthread is the Java-UI stand-in. The
		 * GameActivity native thread also ALooper_prepare()s. */
		if (tls_looper != NULL && on_native_main()) {
			g_ui_looper = tls_looper;
		}
	}
	return tls_looper;
}

void tipsy_ALooper_acquire(ALooper *looper)
{
	if (looper != NULL && looper->magic == TIPSY_ALOOPER_MAGIC) {
		__sync_add_and_fetch(&looper->refs, 1);
	}
}

void tipsy_ALooper_release(ALooper *looper)
{
	if (looper == NULL || looper->magic != TIPSY_ALOOPER_MAGIC) {
		return;
	}
	if (__sync_sub_and_fetch(&looper->refs, 1) == 0) {
		if (tls_looper == looper) {
			tls_looper = NULL;
		}
		looper_free(looper);
	}
}

int tipsy_ALooper_addFd(ALooper *looper, int fd, int ident, int events, ALooper_callbackFunc callback, void *data)
{
	struct epoll_event ev;
	int i;
	int slot = -1;

	if (looper == NULL || looper->magic != TIPSY_ALOOPER_MAGIC || fd < 0) {
		return -1;
	}
	pthread_mutex_lock(&looper->lock);
	for (i = 0; i < MAX_LOOPER_FDS; i++) {
		if (looper->fds[i].used && looper->fds[i].fd == fd) {
			slot = i;
			break;
		}
		if (!looper->fds[i].used && slot < 0) {
			slot = i;
		}
	}
	if (slot < 0) {
		pthread_mutex_unlock(&looper->lock);
		return -1;
	}
	looper->fds[slot].fd = fd;
	looper->fds[slot].ident = ident;
	looper->fds[slot].events = events;
	looper->fds[slot].callback = callback;
	looper->fds[slot].data = data;
	looper->fds[slot].used = 1;
	memset(&ev, 0, sizeof ev);
	ev.events = 0;
	if (events & ALOOPER_EVENT_INPUT) {
		ev.events |= EPOLLIN;
	}
	if (events & ALOOPER_EVENT_OUTPUT) {
		ev.events |= EPOLLOUT;
	}
	if (ev.events == 0) {
		ev.events = EPOLLIN;
	}
	ev.data.fd = fd;
	if (epoll_ctl(looper->epfd, EPOLL_CTL_ADD, fd, &ev) != 0) {
		if (epoll_ctl(looper->epfd, EPOLL_CTL_MOD, fd, &ev) != 0) {
			looper->fds[slot].used = 0;
			pthread_mutex_unlock(&looper->lock);
			return -1;
		}
	}
	pthread_mutex_unlock(&looper->lock);
	return 1;
}

int tipsy_ALooper_removeFd(ALooper *looper, int fd)
{
	int i;
	int found = 0;
	if (looper == NULL || looper->magic != TIPSY_ALOOPER_MAGIC) {
		return 0;
	}
	pthread_mutex_lock(&looper->lock);
	for (i = 0; i < MAX_LOOPER_FDS; i++) {
		if (looper->fds[i].used && looper->fds[i].fd == fd) {
			looper->fds[i].used = 0;
			found = 1;
			break;
		}
	}
	epoll_ctl(looper->epfd, EPOLL_CTL_DEL, fd, NULL);
	pthread_mutex_unlock(&looper->lock);
	return found;
}

int tipsy_ALooper_pollOnce(int timeoutMillis, int *outFd, int *outEvents, void **outData)
{
	ALooper *l = tls_looper;
	struct epoll_event ev;
	int n;
	int i;
	int ident;
	int events;
	void *data;
	ALooper_callbackFunc cb;

	if (l == NULL || l->magic != TIPSY_ALOOPER_MAGIC) {
		return ALOOPER_POLL_ERROR;
	}
	/* timeout 0 must not block; negative waits forever. */
	n = epoll_wait(l->epfd, &ev, 1, timeoutMillis < 0 ? -1 : timeoutMillis);
	if (n == 0) {
		return ALOOPER_POLL_TIMEOUT;
	}
	if (n < 0) {
		if (errno == EINTR) {
			return ALOOPER_POLL_WAKE;
		}
		return ALOOPER_POLL_ERROR;
	}
	if (ev.data.fd == l->wake_r) {
		char drain[32];
		while (read(l->wake_r, drain, sizeof drain) > 0) {
		}
		return ALOOPER_POLL_WAKE;
	}
	pthread_mutex_lock(&l->lock);
	ident = 0;
	cb = NULL;
	data = NULL;
	events = 0;
	for (i = 0; i < MAX_LOOPER_FDS; i++) {
		if (l->fds[i].used && l->fds[i].fd == ev.data.fd) {
			ident = l->fds[i].ident;
			cb = l->fds[i].callback;
			data = l->fds[i].data;
			break;
		}
	}
	pthread_mutex_unlock(&l->lock);
	if (ev.events & EPOLLIN) {
		events |= ALOOPER_EVENT_INPUT;
	}
	if (ev.events & EPOLLOUT) {
		events |= ALOOPER_EVENT_OUTPUT;
	}
	if (ev.events & (EPOLLERR | EPOLLHUP)) {
		events |= ALOOPER_EVENT_ERROR;
	}
	if (cb != NULL) {
		cb(ev.data.fd, events, data);
		return ALOOPER_POLL_CALLBACK;
	}
	if (outFd != NULL) {
		*outFd = ev.data.fd;
	}
	if (outEvents != NULL) {
		*outEvents = events;
	}
	if (outData != NULL) {
		*outData = data;
	}
	return ident;
}

void tipsy_ALooper_wake(ALooper *looper)
{
	char b = 1;
	if (looper == NULL || looper->magic != TIPSY_ALOOPER_MAGIC) {
		return;
	}
	(void)write(looper->wake_w, &b, 1);
}

static void poll_looper_nested(void);

int tipsy_native_main_idle(int timeout_ms)
{
	int rc;

	if (tls_looper == NULL || tls_looper->magic != TIPSY_ALOOPER_MAGIC) {
		return ALOOPER_POLL_ERROR;
	}
	rc = tipsy_ALooper_pollOnce(timeout_ms, NULL, NULL, NULL);
	if (rc != ALOOPER_POLL_ERROR) {
		poll_looper_nested();
	}
	return rc;
}

void tipsy_native_main_wake(void)
{
	if (g_ui_looper != NULL) {
		tipsy_ALooper_wake(g_ui_looper);
	}
}

static void poll_looper_nested(void)
{
	int n;

	if (tls_looper == NULL) {
		return;
	}
	if (!g_logged_nested_poll) {
		g_logged_nested_poll = 1;
		GoAndroid_LogWrite(4, "tipsy", on_native_main() ?
			"ALooper nested poll during Main cond_wait" :
			"ALooper nested poll during cond_wait");
	}
	for (n = 0; n < 64; n++) {
		int rc = tipsy_ALooper_pollOnce(0, NULL, NULL, NULL);
		if (rc == ALOOPER_POLL_TIMEOUT || rc == ALOOPER_POLL_ERROR) {
			break;
		}
	}
}

int tipsy_pthread_cond_wait(void *cond, void *mutex)
{
	pthread_cond_t *c = cond;
	pthread_mutex_t *m = mutex;
	struct timespec ts;
	int rc;
	uint64_t diag_started;

	if (c == NULL || m == NULL) {
		errno = EINVAL;
		return EINVAL;
	}
	/* Official Looper is per-thread. Main (Java UI) and the GameActivity
	 * native thread each have a TLS ALooper. Nest pollOnce on whichever
	 * thread owns one so posted fds run during cond_wait. Workers with
	 * no looper keep a vanilla wait. */
	if (tls_looper == NULL && !g_cond_wait_poll) {
		return pthread_cond_wait(c, m);
	}
	diag_started = tipsy_stutter_wait_begin(TIPSY_STUTTER_WAIT_COND);
	if (clock_gettime(CLOCK_REALTIME, &ts) != 0) {
		rc = pthread_cond_wait(c, m);
		tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
		return rc;
	}
	ts.tv_nsec += 16L * 1000000L;
	if (ts.tv_nsec >= 1000000000L) {
		ts.tv_sec++;
		ts.tv_nsec -= 1000000000L;
	}
	rc = pthread_cond_timedwait(c, m, &ts);
	tipsy_stutter_wait_slice(TIPSY_STUTTER_WAIT_COND);
	if (rc == 0) {
		tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
		return 0;
	}
	if (rc != ETIMEDOUT) {
		if (rc == EINVAL) {
			rc = pthread_cond_wait(c, m);
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
			return rc;
		}
		tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
		return rc;
	}
	/* Mutex is held after timeout. Release so complete() / looper
	 * callbacks can take it; return 0 as a legal spurious wakeup so
	 * the caller rechecks bit 2 of +0x70. */
	pthread_mutex_unlock(m);
	poll_looper_nested();
	pthread_mutex_lock(m);
	tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
	return 0;
}

int tipsy_pthread_cond_timedwait(void *cond, void *mutex, void *abstime)
{
	pthread_cond_t *c = cond;
	pthread_mutex_t *m = mutex;
	const struct timespec *deadline = abstime;
	struct timespec ts, now;
	int rc;
	uint64_t diag_started;

	if (c == NULL || m == NULL || deadline == NULL) {
		errno = EINVAL;
		return EINVAL;
	}
	if (tls_looper == NULL && !g_cond_wait_poll) {
		return pthread_cond_timedwait(c, m, deadline);
	}
	diag_started = tipsy_stutter_wait_begin(TIPSY_STUTTER_WAIT_TIMEDCOND);
	for (;;) {
		if (clock_gettime(CLOCK_REALTIME, &now) != 0) {
			rc = pthread_cond_timedwait(c, m, deadline);
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
			return rc;
		}
		if (now.tv_sec > deadline->tv_sec ||
		    (now.tv_sec == deadline->tv_sec && now.tv_nsec >= deadline->tv_nsec)) {
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
			return ETIMEDOUT;
		}
		ts = now;
		ts.tv_nsec += 16L * 1000000L;
		if (ts.tv_nsec >= 1000000000L) {
			ts.tv_sec++;
			ts.tv_nsec -= 1000000000L;
		}
		if (ts.tv_sec > deadline->tv_sec ||
		    (ts.tv_sec == deadline->tv_sec && ts.tv_nsec > deadline->tv_nsec)) {
			ts = *deadline;
		}
		rc = pthread_cond_timedwait(c, m, &ts);
		tipsy_stutter_wait_slice(TIPSY_STUTTER_WAIT_TIMEDCOND);
		if (rc == 0) {
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
			return 0;
		}
		if (rc != ETIMEDOUT) {
			if (rc == EINVAL) {
				rc = pthread_cond_timedwait(c, m, deadline);
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
				return rc;
			}
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
			return rc;
		}
		pthread_mutex_unlock(m);
		poll_looper_nested();
		pthread_mutex_lock(m);
	}
}

static int g_test_cb_fired;
static pthread_cond_t g_test_cv = PTHREAD_COND_INITIALIZER;
static pthread_mutex_t g_test_mu = PTHREAD_MUTEX_INITIALIZER;

static int test_cond_cb(int fd, int events, void *data)
{
	char b;

	(void)events;
	(void)data;
	(void)read(fd, &b, 1);
	g_test_cb_fired = 1;
	pthread_cond_signal(&g_test_cv);
	return 1;
}

static void *test_write_pipe(void *arg)
{
	int fd = (int)(intptr_t)arg;
	char b = 1;

	usleep(5000);
	(void)write(fd, &b, 1);
	return NULL;
}

int tipsy_test_cond_wait_polls_looper(void)
{
	ALooper *l;
	int fds[2];
	pthread_t th;

	g_test_cb_fired = 0;
	g_cond_wait_poll = 1;
	l = tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	if (l == NULL || pipe2(fds, O_CLOEXEC | O_NONBLOCK) != 0) {
		g_cond_wait_poll = 0;
		return -1;
	}
	if (tipsy_ALooper_addFd(l, fds[0], 1, ALOOPER_EVENT_INPUT, test_cond_cb, NULL) < 0) {
		close(fds[0]);
		close(fds[1]);
		g_cond_wait_poll = 0;
		return -2;
	}
	pthread_mutex_lock(&g_test_mu);
	if (pthread_create(&th, NULL, test_write_pipe, (void *)(intptr_t)fds[1]) != 0) {
		pthread_mutex_unlock(&g_test_mu);
		tipsy_ALooper_removeFd(l, fds[0]);
		close(fds[0]);
		close(fds[1]);
		g_cond_wait_poll = 0;
		return -3;
	}
	{
		struct timespec deadline, now;

		clock_gettime(CLOCK_REALTIME, &deadline);
		deadline.tv_sec += 2;
		while (!g_test_cb_fired) {
			(void)tipsy_pthread_cond_wait(&g_test_cv, &g_test_mu);
			clock_gettime(CLOCK_REALTIME, &now);
			if (now.tv_sec > deadline.tv_sec ||
			    (now.tv_sec == deadline.tv_sec && now.tv_nsec >= deadline.tv_nsec)) {
				break;
			}
		}
	}
	pthread_mutex_unlock(&g_test_mu);
	pthread_join(th, NULL);
	g_cond_wait_poll = 0;
	tipsy_ALooper_removeFd(l, fds[0]);
	close(fds[0]);
	close(fds[1]);
	return g_test_cb_fired;
}

AAssetManager *tipsy_AAssetManager_singleton(void)
{
	return &g_amgr;
}

AAssetManager *tipsy_AAssetManager_fromJava(void *env, void *assetManager)
{
	(void)env;
	(void)assetManager;
	return &g_amgr;
}

AAsset *tipsy_AAsset_from_buffer(void *buf, int64_t len, int owned, int fd)
{
	AAsset *a = calloc(1, sizeof(*a));
	if (a == NULL) {
		return NULL;
	}
	a->magic = TIPSY_ASSET_MAGIC;
	a->buffer = buf;
	a->length = len;
	a->pos = 0;
	a->fd = fd;
	a->owned = owned;
	return a;
}

AAsset *tipsy_AAssetManager_open(AAssetManager *mgr, const char *filename, int mode)
{
	(void)mgr;
	return GoAndroid_AssetOpen((char *)(filename ? filename : ""), mode);
}

void tipsy_AAsset_close(AAsset *asset)
{
	if (asset == NULL || asset->magic != TIPSY_ASSET_MAGIC) {
		return;
	}
	if (asset->owned && asset->buffer != NULL) {
		free(asset->buffer);
	}
	if (asset->fd >= 0) {
		close(asset->fd);
	}
	asset->magic = 0;
	free(asset);
}

const void *tipsy_AAsset_getBuffer(AAsset *asset)
{
	if (asset == NULL || asset->magic != TIPSY_ASSET_MAGIC) {
		return NULL;
	}
	return asset->buffer;
}

int64_t tipsy_AAsset_getLength(AAsset *asset)
{
	if (asset == NULL || asset->magic != TIPSY_ASSET_MAGIC) {
		return 0;
	}
	return asset->length;
}

int tipsy_AAsset_openFileDescriptor(AAsset *asset, int64_t *outStart, int64_t *outLength)
{
	int fd;
	if (asset == NULL || asset->magic != TIPSY_ASSET_MAGIC) {
		return -1;
	}
	if (outStart != NULL) {
		*outStart = 0;
	}
	if (outLength != NULL) {
		*outLength = asset->length;
	}
	if (asset->fd >= 0) {
		return dup(asset->fd);
	}
	fd = (int)syscall(SYS_memfd_create, "tipsy-asset", 0);
	if (fd < 0) {
		return -1;
	}
	if (asset->buffer != NULL && asset->length > 0) {
		if (write(fd, asset->buffer, (size_t)asset->length) != asset->length) {
			close(fd);
			return -1;
		}
		lseek(fd, 0, SEEK_SET);
	}
	return fd;
}

int tipsy_AAsset_read(AAsset *asset, void *buf, size_t count)
{
	int64_t remain;
	size_t n;
	if (asset == NULL || asset->magic != TIPSY_ASSET_MAGIC || buf == NULL) {
		return -1;
	}
	remain = asset->length - asset->pos;
	if (remain <= 0) {
		return 0;
	}
	n = (size_t)remain;
	if (n > count) {
		n = count;
	}
	if (asset->buffer != NULL) {
		memcpy(buf, (char *)asset->buffer + asset->pos, n);
	}
	asset->pos += (int64_t)n;
	return (int)n;
}

int64_t tipsy_AAsset_seek(AAsset *asset, int64_t offset, int whence)
{
	int64_t np;
	if (asset == NULL || asset->magic != TIPSY_ASSET_MAGIC) {
		return -1;
	}
	switch (whence) {
	case SEEK_SET:
		np = offset;
		break;
	case SEEK_CUR:
		np = asset->pos + offset;
		break;
	case SEEK_END:
		np = asset->length + offset;
		break;
	default:
		return -1;
	}
	if (np < 0 || np > asset->length) {
		return -1;
	}
	asset->pos = np;
	return np;
}

AConfiguration *tipsy_AConfiguration_new(void)
{
	AConfiguration *c = calloc(1, sizeof(*c));
	if (c == NULL) {
		return NULL;
	}
	c->magic = TIPSY_ACFG_MAGIC;
	c->language[0] = 'e';
	c->language[1] = 'n';
	c->country[0] = 'U';
	c->country[1] = 'S';
	c->width_dp = 1920;
	c->height_dp = 1080;
	c->screen_size = ACONFIGURATION_SCREENSIZE_XLARGE;
	c->nav_hidden = ACONFIGURATION_NAVHIDDEN_NO;
	return c;
}

void tipsy_AConfiguration_delete(AConfiguration *config)
{
	if (config == NULL || config->magic != TIPSY_ACFG_MAGIC) {
		return;
	}
	config->magic = 0;
	free(config);
}

void tipsy_AConfiguration_fromAssetManager(AConfiguration *out, AAssetManager *am)
{
	(void)am;
	if (out == NULL) {
		return;
	}
	if (out->magic != TIPSY_ACFG_MAGIC) {
		out->magic = TIPSY_ACFG_MAGIC;
	}
	out->language[0] = 'e';
	out->language[1] = 'n';
	out->country[0] = 'U';
	out->country[1] = 'S';
	out->width_dp = 1920;
	out->height_dp = 1080;
	out->screen_size = ACONFIGURATION_SCREENSIZE_XLARGE;
	out->nav_hidden = ACONFIGURATION_NAVHIDDEN_NO;
}

void tipsy_AConfiguration_getLanguage(AConfiguration *config, char *outLanguage)
{
	if (outLanguage == NULL) {
		return;
	}
	if (config != NULL && config->magic == TIPSY_ACFG_MAGIC) {
		outLanguage[0] = config->language[0];
		outLanguage[1] = config->language[1];
	} else {
		outLanguage[0] = 'e';
		outLanguage[1] = 'n';
	}
}

void tipsy_AConfiguration_getCountry(AConfiguration *config, char *outCountry)
{
	if (outCountry == NULL) {
		return;
	}
	if (config != NULL && config->magic == TIPSY_ACFG_MAGIC) {
		outCountry[0] = config->country[0];
		outCountry[1] = config->country[1];
	} else {
		outCountry[0] = 'U';
		outCountry[1] = 'S';
	}
}

int32_t tipsy_AConfiguration_getScreenWidthDp(AConfiguration *config)
{
	if (config != NULL && config->magic == TIPSY_ACFG_MAGIC) {
		return config->width_dp;
	}
	return 1920;
}

int32_t tipsy_AConfiguration_getScreenHeightDp(AConfiguration *config)
{
	if (config != NULL && config->magic == TIPSY_ACFG_MAGIC) {
		return config->height_dp;
	}
	return 1080;
}

int32_t tipsy_AConfiguration_getScreenSize(AConfiguration *config)
{
	if (config != NULL && config->magic == TIPSY_ACFG_MAGIC) {
		return config->screen_size;
	}
	return ACONFIGURATION_SCREENSIZE_XLARGE;
}

int32_t tipsy_AConfiguration_getNavHidden(AConfiguration *config)
{
	if (config != NULL && config->magic == TIPSY_ACFG_MAGIC) {
		return config->nav_hidden;
	}
	return ACONFIGURATION_NAVHIDDEN_NO;
}

/* Bionic libc extras not always present under the same name in glibc. */
int *tipsy_errno_fn(void)
{
	return __errno_location();
}

void tipsy_arc4random_buf(void *buf, size_t n)
{
	int fd;
	size_t off = 0;
	if (buf == NULL || n == 0) {
		return;
	}
#ifdef SYS_getrandom
	{
		long r = syscall(SYS_getrandom, buf, n, 0);
		if (r == (long)n) {
			return;
		}
	}
#endif
	fd = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
	if (fd < 0) {
		memset(buf, 0, n);
		return;
	}
	while (off < n) {
		ssize_t r = read(fd, (char *)buf + off, n - off);
		if (r <= 0) {
			break;
		}
		off += (size_t)r;
	}
	close(fd);
}

int32_t tipsy_gettid(void)
{
	return (int32_t)syscall(SYS_gettid);
}
