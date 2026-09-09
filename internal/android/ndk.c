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
#include <sys/types.h>
#include <time.h>
#include <unistd.h>
#include <sys/syscall.h>
#include <sys/wait.h>

enum { TIPSY_FUTEX_WAKE_BITSET_PRIVATE = 0x8a };
enum { TIPSY_FUTEX_WAIT_BITSET_PRIVATE = 0x89 };

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
	int watch_epfd;
	int stop_r;
	int stop_w;
	pthread_t watcher;
	int watcher_on;
	_Atomic int watcher_stop;
	pthread_cond_t *parked_cond;
	pthread_mutex_t *parked_mutex;
	int *parked_futex;
	int pending_wake;
	int looper_woke;
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

static void looper_close_watch_fds(ALooper *l)
{
	if (l->watch_epfd >= 0) {
		close(l->watch_epfd);
		l->watch_epfd = -1;
	}
	if (l->stop_r >= 0) {
		close(l->stop_r);
		l->stop_r = -1;
	}
	if (l->stop_w >= 0) {
		close(l->stop_w);
		l->stop_w = -1;
	}
}

static void looper_signal_parked(ALooper *l)
{
	pthread_cond_t *c;
	pthread_mutex_t *m;
	int *futex;

	if (l == NULL || l->magic != TIPSY_ALOOPER_MAGIC) {
		return;
	}
	pthread_mutex_lock(&l->lock);
	c = l->parked_cond;
	m = l->parked_mutex;
	futex = l->parked_futex;
	if (c == NULL && futex == NULL) {
		l->pending_wake = 1;
		pthread_mutex_unlock(&l->lock);
		return;
	}
	l->looper_woke = 1;
	pthread_mutex_unlock(&l->lock);
	if (c != NULL && m != NULL) {
		/* Take the parked mutex after dropping the looper lock so this
		 * cannot race past pthread_cond_wait (waiter holds m until then). */
		pthread_mutex_lock(m);
		pthread_cond_broadcast(c);
		pthread_mutex_unlock(m);
	} else if (c != NULL) {
		pthread_cond_broadcast(c);
	}
	if (futex != NULL) {
		(void)syscall(SYS_futex, futex, TIPSY_FUTEX_WAKE_BITSET_PRIVATE,
			0x7fffffff, NULL, NULL, 0xffffffffu);
	}
}

static void *looper_watcher(void *arg)
{
	ALooper *l = arg;
	struct epoll_event ev;

#if defined(__linux__)
	(void)pthread_setname_np(pthread_self(), "tip.looper");
#endif
	while (!atomic_load_explicit(&l->watcher_stop, memory_order_relaxed)) {
		int n = epoll_wait(l->watch_epfd, &ev, 1, -1);
		if (atomic_load_explicit(&l->watcher_stop, memory_order_relaxed)) {
			break;
		}
		if (n < 0) {
			if (errno == EINTR) {
				continue;
			}
			break;
		}
		if (n == 0) {
			continue;
		}
		if (ev.data.fd == l->stop_r) {
			break;
		}
		looper_signal_parked(l);
	}
	return NULL;
}

static void looper_start_watcher(ALooper *l)
{
	int stop[2];
	int watch;
	struct epoll_event ev;

	l->watch_epfd = -1;
	l->stop_r = -1;
	l->stop_w = -1;
	l->watcher_on = 0;
	atomic_store_explicit(&l->watcher_stop, 0, memory_order_relaxed);
	watch = epoll_create1(EPOLL_CLOEXEC);
	if (watch < 0) {
		return;
	}
	if (pipe2(stop, O_CLOEXEC | O_NONBLOCK) != 0) {
		close(watch);
		return;
	}
	l->watch_epfd = watch;
	l->stop_r = stop[0];
	l->stop_w = stop[1];
	memset(&ev, 0, sizeof ev);
	ev.events = EPOLLIN;
	ev.data.fd = l->stop_r;
	if (epoll_ctl(l->watch_epfd, EPOLL_CTL_ADD, l->stop_r, &ev) != 0) {
		goto fail;
	}
	/* Nested ET: one edge when the owner epfd becomes ready. Does not
	 * consume inner events; pollOnce(0) still sees them. */
	ev.events = EPOLLIN | EPOLLET;
	ev.data.fd = l->epfd;
	if (epoll_ctl(l->watch_epfd, EPOLL_CTL_ADD, l->epfd, &ev) != 0) {
		goto fail;
	}
	if (pthread_create(&l->watcher, NULL, looper_watcher, l) != 0) {
		goto fail;
	}
	l->watcher_on = 1;
	return;
fail:
	looper_close_watch_fds(l);
}

static void looper_stop_watcher(ALooper *l)
{
	if (l == NULL) {
		return;
	}
	atomic_store_explicit(&l->watcher_stop, 1, memory_order_relaxed);
	if (l->stop_w >= 0) {
		char b = 1;
		(void)write(l->stop_w, &b, 1);
	}
	if (l->watcher_on) {
		(void)pthread_join(l->watcher, NULL);
		l->watcher_on = 0;
	}
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
	l->watch_epfd = -1;
	l->stop_r = -1;
	l->stop_w = -1;
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
	looper_start_watcher(l);
	return l;
}

static void looper_free(ALooper *l)
{
	int i;
	if (l == NULL) {
		return;
	}
	looper_stop_watcher(l);
	for (i = 0; i < MAX_LOOPER_FDS; i++) {
		if (l->fds[i].used) {
			epoll_ctl(l->epfd, EPOLL_CTL_DEL, l->fds[i].fd, NULL);
		}
	}
	looper_close_watch_fds(l);
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
	looper_signal_parked(looper);
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

static int looper_should_nest(void)
{
	return tls_looper != NULL || g_cond_wait_poll;
}

static int looper_can_event_park(void)
{
	return tls_looper != NULL && tls_looper->magic == TIPSY_ALOOPER_MAGIC &&
		tls_looper->watcher_on;
}

int tipsy_looper_can_park(void)
{
	return looper_can_event_park();
}

static int looper_arm_cond(pthread_cond_t *c, pthread_mutex_t *m)
{
	ALooper *l = tls_looper;
	int pending;

	if (l == NULL || l->magic != TIPSY_ALOOPER_MAGIC) {
		return 0;
	}
	pthread_mutex_lock(&l->lock);
	l->parked_cond = c;
	l->parked_mutex = m;
	pending = l->pending_wake || l->looper_woke;
	if (pending) {
		l->pending_wake = 0;
		l->looper_woke = 0;
		l->parked_cond = NULL;
		l->parked_mutex = NULL;
	}
	pthread_mutex_unlock(&l->lock);
	return pending;
}

static int looper_disarm_cond(void)
{
	ALooper *l = tls_looper;
	int woke = 0;

	if (l == NULL || l->magic != TIPSY_ALOOPER_MAGIC) {
		return 0;
	}
	pthread_mutex_lock(&l->lock);
	l->parked_cond = NULL;
	l->parked_mutex = NULL;
	woke = l->looper_woke || l->pending_wake;
	l->looper_woke = 0;
	l->pending_wake = 0;
	pthread_mutex_unlock(&l->lock);
	return woke;
}

static void looper_poll_unlocked(pthread_mutex_t *m)
{
	pthread_mutex_unlock(m);
	poll_looper_nested();
	pthread_mutex_lock(m);
}

int tipsy_looper_park_futex(int *uaddr)
{
	ALooper *l = tls_looper;
	int pending;

	if (l == NULL || l->magic != TIPSY_ALOOPER_MAGIC || uaddr == NULL) {
		return 0;
	}
	pthread_mutex_lock(&l->lock);
	l->parked_futex = uaddr;
	pending = l->pending_wake || l->looper_woke;
	if (pending) {
		l->pending_wake = 0;
		l->looper_woke = 0;
		l->parked_futex = NULL;
	}
	pthread_mutex_unlock(&l->lock);
	return pending;
}

void tipsy_looper_unpark_futex(void)
{
	ALooper *l = tls_looper;

	if (l == NULL || l->magic != TIPSY_ALOOPER_MAGIC) {
		return;
	}
	pthread_mutex_lock(&l->lock);
	l->parked_futex = NULL;
	pthread_mutex_unlock(&l->lock);
}

int tipsy_looper_consume_wake(void)
{
	ALooper *l = tls_looper;
	int woke = 0;

	if (l == NULL || l->magic != TIPSY_ALOOPER_MAGIC) {
		return 0;
	}
	pthread_mutex_lock(&l->lock);
	woke = l->looper_woke || l->pending_wake;
	l->looper_woke = 0;
	l->pending_wake = 0;
	pthread_mutex_unlock(&l->lock);
	return woke;
}

int tipsy_pthread_cond_wait(void *cond, void *mutex)
{
	pthread_cond_t *c = cond;
	pthread_mutex_t *m = mutex;
	struct timespec ts;
	int rc;
	int woke;
	uint64_t diag_started;

	if (c == NULL || m == NULL) {
		errno = EINVAL;
		return EINVAL;
	}
	/* Official Looper is per-thread. Main (Java UI) and the GameActivity
	 * native thread each have a TLS ALooper. Nest pollOnce on whichever
	 * thread owns one so posted fds run during cond_wait. Workers with
	 * no looper keep a vanilla wait. */
	if (!looper_should_nest()) {
		return pthread_cond_wait(c, m);
	}
	diag_started = tipsy_stutter_wait_begin(TIPSY_STUTTER_WAIT_COND);
	if (looper_can_event_park()) {
		if (looper_arm_cond(c, m)) {
			looper_poll_unlocked(m);
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
			return 0;
		}
		rc = pthread_cond_wait(c, m);
		woke = looper_disarm_cond();
		if (rc == EINVAL) {
			rc = pthread_cond_wait(c, m);
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
			return rc;
		}
		if (woke) {
			looper_poll_unlocked(m);
		}
		tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_COND, diag_started);
		if (rc == 0 || woke) {
			return 0;
		}
		return rc;
	}
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
	looper_poll_unlocked(m);
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
	int woke;
	uint64_t diag_started;

	if (c == NULL || m == NULL || deadline == NULL) {
		errno = EINVAL;
		return EINVAL;
	}
	if (!looper_should_nest()) {
		return pthread_cond_timedwait(c, m, deadline);
	}
	diag_started = tipsy_stutter_wait_begin(TIPSY_STUTTER_WAIT_TIMEDCOND);
	if (looper_can_event_park()) {
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
			if (looper_arm_cond(c, m)) {
				looper_poll_unlocked(m);
				continue;
			}
			rc = pthread_cond_timedwait(c, m, deadline);
			woke = looper_disarm_cond();
			if (rc == 0) {
				if (woke) {
					looper_poll_unlocked(m);
				}
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
				return 0;
			}
			if (rc == ETIMEDOUT) {
				if (woke) {
					looper_poll_unlocked(m);
					continue;
				}
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
				return ETIMEDOUT;
			}
			if (rc == EINVAL) {
				rc = pthread_cond_timedwait(c, m, deadline);
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
				return rc;
			}
			tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_TIMEDCOND, diag_started);
			return rc;
		}
	}
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
		looper_poll_unlocked(m);
	}
}

static int g_test_cb_fired;
static pthread_cond_t g_test_cv = PTHREAD_COND_INITIALIZER;
static pthread_mutex_t g_test_mu = PTHREAD_MUTEX_INITIALIZER;
static _Atomic int g_test_helper_rc;

static int timespec_expired(const struct timespec *deadline)
{
	struct timespec now;

	if (clock_gettime(CLOCK_REALTIME, &now) != 0) {
		return 1;
	}
	return now.tv_sec > deadline->tv_sec ||
		(now.tv_sec == deadline->tv_sec && now.tv_nsec >= deadline->tv_nsec);
}

static int test_wait_helper_rc(int spins)
{
	int i;
	int rc;

	for (i = 0; i < spins; i++) {
		rc = atomic_load_explicit(&g_test_helper_rc, memory_order_relaxed);
		if (rc != 0) {
			return rc;
		}
		usleep(10000);
	}
	return -2;
}

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

static int test_cond_wait_until_cb(void)
{
	struct timespec deadline;

	clock_gettime(CLOCK_REALTIME, &deadline);
	deadline.tv_sec += 2;
	while (!g_test_cb_fired) {
		(void)tipsy_pthread_cond_wait(&g_test_cv, &g_test_mu);
		if (timespec_expired(&deadline)) {
			break;
		}
	}
	return g_test_cb_fired;
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
	if (!l->watcher_on) {
		close(fds[0]);
		close(fds[1]);
		g_cond_wait_poll = 0;
		return -4;
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
	(void)test_cond_wait_until_cb();
	pthread_mutex_unlock(&g_test_mu);
	pthread_join(th, NULL);
	g_cond_wait_poll = 0;
	tipsy_ALooper_removeFd(l, fds[0]);
	close(fds[0]);
	close(fds[1]);
	return g_test_cb_fired;
}

static void *test_wake_looper(void *arg)
{
	ALooper *l = arg;

	usleep(5000);
	tipsy_ALooper_wake(l);
	return NULL;
}

int tipsy_test_cond_wait_wake_unblocks(void)
{
	ALooper *l;
	pthread_t th;
	int unblocked = 0;
	struct timespec deadline;

	g_cond_wait_poll = 1;
	l = tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	if (l == NULL || !l->watcher_on) {
		g_cond_wait_poll = 0;
		return -1;
	}
	pthread_mutex_lock(&g_test_mu);
	if (pthread_create(&th, NULL, test_wake_looper, l) != 0) {
		pthread_mutex_unlock(&g_test_mu);
		g_cond_wait_poll = 0;
		return -2;
	}
	clock_gettime(CLOCK_REALTIME, &deadline);
	deadline.tv_sec += 2;
	(void)tipsy_pthread_cond_wait(&g_test_cv, &g_test_mu);
	if (!timespec_expired(&deadline)) {
		unblocked = 1;
	}
	pthread_mutex_unlock(&g_test_mu);
	pthread_join(th, NULL);
	g_cond_wait_poll = 0;
	return unblocked;
}

int tipsy_test_cond_wait_lost_wakeup(void)
{
	ALooper *l;
	int fds[2];
	char b = 1;
	struct timespec start, end;
	long elapsed_ms;

	g_test_cb_fired = 0;
	g_cond_wait_poll = 1;
	l = tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	if (l == NULL || !l->watcher_on || pipe2(fds, O_CLOEXEC | O_NONBLOCK) != 0) {
		g_cond_wait_poll = 0;
		return -1;
	}
	if (tipsy_ALooper_addFd(l, fds[0], 1, ALOOPER_EVENT_INPUT, test_cond_cb, NULL) < 0) {
		close(fds[0]);
		close(fds[1]);
		g_cond_wait_poll = 0;
		return -2;
	}
	/* Event before arm: watcher sets pending_wake; cond_wait must not hang. */
	(void)write(fds[1], &b, 1);
	usleep(20000);
	clock_gettime(CLOCK_MONOTONIC, &start);
	pthread_mutex_lock(&g_test_mu);
	(void)test_cond_wait_until_cb();
	pthread_mutex_unlock(&g_test_mu);
	clock_gettime(CLOCK_MONOTONIC, &end);
	g_cond_wait_poll = 0;
	tipsy_ALooper_removeFd(l, fds[0]);
	close(fds[0]);
	close(fds[1]);
	if (!g_test_cb_fired) {
		return -3;
	}
	elapsed_ms = (end.tv_sec - start.tv_sec) * 1000L +
		(end.tv_nsec - start.tv_nsec) / 1000000L;
	if (elapsed_ms > 1500) {
		return -4;
	}
	return 1;
}

static void *test_idle_wake(void *arg)
{
	usleep(5000);
	tipsy_ALooper_wake(arg);
	return NULL;
}

int tipsy_test_idle_unblocks_on_wake(void)
{
	ALooper *l;
	pthread_t th;
	struct timespec start, end;
	long elapsed_ms;
	int rc;

	l = tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	if (l == NULL) {
		return -1;
	}
	if (pthread_create(&th, NULL, test_idle_wake, l) != 0) {
		return -2;
	}
	clock_gettime(CLOCK_MONOTONIC, &start);
	rc = tipsy_native_main_idle(-1);
	clock_gettime(CLOCK_MONOTONIC, &end);
	pthread_join(th, NULL);
	if (rc == ALOOPER_POLL_ERROR) {
		return -3;
	}
	elapsed_ms = (end.tv_sec - start.tv_sec) * 1000L +
		(end.tv_nsec - start.tv_nsec) / 1000000L;
	if (elapsed_ms > 1500) {
		return -4;
	}
	return 1;
}

static int test_fallback_cb(int fd, int events, void *data)
{
	char b;
	pthread_cond_t *cv = data;

	(void)events;
	(void)read(fd, &b, 1);
	g_test_cb_fired = 1;
	pthread_cond_signal(cv);
	return 1;
}

static void *test_fallback_thread(void *arg)
{
	ALooper *l;
	int fds[2];
	pthread_t writer;
	pthread_cond_t cv = PTHREAD_COND_INITIALIZER;
	pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
	struct timespec deadline;

	(void)arg;
	g_test_cb_fired = 0;
	g_cond_wait_poll = 1;
	l = tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	if (l == NULL) {
		atomic_store_explicit(&g_test_helper_rc, -1, memory_order_relaxed);
		return NULL;
	}
	if (!l->watcher_on) {
		atomic_store_explicit(&g_test_helper_rc, -4, memory_order_relaxed);
		return NULL;
	}
	looper_stop_watcher(l);
	if (l->watcher_on) {
		atomic_store_explicit(&g_test_helper_rc, -5, memory_order_relaxed);
		return NULL;
	}
	if (pipe2(fds, O_CLOEXEC | O_NONBLOCK) != 0 ||
	    tipsy_ALooper_addFd(l, fds[0], 1, ALOOPER_EVENT_INPUT, test_fallback_cb, &cv) < 0) {
		atomic_store_explicit(&g_test_helper_rc, -2, memory_order_relaxed);
		return NULL;
	}
	pthread_mutex_lock(&mu);
	if (pthread_create(&writer, NULL, test_write_pipe, (void *)(intptr_t)fds[1]) != 0) {
		pthread_mutex_unlock(&mu);
		atomic_store_explicit(&g_test_helper_rc, -3, memory_order_relaxed);
		return NULL;
	}
	clock_gettime(CLOCK_REALTIME, &deadline);
	deadline.tv_sec += 2;
	while (!g_test_cb_fired) {
		(void)tipsy_pthread_cond_wait(&cv, &mu);
		if (timespec_expired(&deadline)) {
			break;
		}
	}
	pthread_mutex_unlock(&mu);
	pthread_join(writer, NULL);
	tipsy_ALooper_removeFd(l, fds[0]);
	close(fds[0]);
	close(fds[1]);
	g_cond_wait_poll = 0;
	atomic_store_explicit(&g_test_helper_rc, g_test_cb_fired ? 1 : -6, memory_order_relaxed);
	return NULL;
}

int tipsy_test_cond_wait_fallback_without_watcher(void)
{
	pthread_t th;
	int rc;

	atomic_store_explicit(&g_test_helper_rc, 0, memory_order_relaxed);
	if (pthread_create(&th, NULL, test_fallback_thread, NULL) != 0) {
		return -3;
	}
	rc = test_wait_helper_rc(250);
	if (rc != -2) {
		pthread_join(th, NULL);
	}
	return rc;
}

static void *test_shutdown_thread(void *arg)
{
	ALooper *l;

	(void)arg;
	l = tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	if (l == NULL || !l->watcher_on) {
		atomic_store_explicit(&g_test_helper_rc, -1, memory_order_relaxed);
		return NULL;
	}
	tipsy_ALooper_acquire(l);
	tipsy_ALooper_release(l);
	tipsy_ALooper_release(l);
	atomic_store_explicit(&g_test_helper_rc, 1, memory_order_relaxed);
	return NULL;
}

int tipsy_test_looper_watcher_shutdown(void)
{
	pthread_t th;
	int rc;

	atomic_store_explicit(&g_test_helper_rc, 0, memory_order_relaxed);
	if (pthread_create(&th, NULL, test_shutdown_thread, NULL) != 0) {
		return -3;
	}
	rc = test_wait_helper_rc(200);
	if (rc != -2) {
		pthread_join(th, NULL);
	}
	return rc;
}

static int test_futex_wake(int *word)
{
	return (int)syscall(SYS_futex, word, TIPSY_FUTEX_WAKE_BITSET_PRIVATE,
		0x7fffffff, NULL, NULL, 0xffffffffu);
}

/* Mirrors native/call.c park_poll_futex with a 2s liveness bound so tests
 * fail closed instead of hanging the suite. */
static int test_park_poll_futex(int *uaddr, unsigned val, int *looper_retries)
{
	struct timespec deadline;
	long rc;

	if (clock_gettime(CLOCK_MONOTONIC, &deadline) != 0) {
		return -1;
	}
	deadline.tv_sec += 2;
	if (looper_retries != NULL) {
		*looper_retries = 0;
	}
	for (;;) {
		struct timespec now;

		if (clock_gettime(CLOCK_MONOTONIC, &now) != 0) {
			return -1;
		}
		if (now.tv_sec > deadline.tv_sec ||
		    (now.tv_sec == deadline.tv_sec && now.tv_nsec >= deadline.tv_nsec)) {
			errno = ETIMEDOUT;
			return -1;
		}
		if (tipsy_looper_park_futex(uaddr)) {
			(void)tipsy_native_main_idle(0);
			if (looper_retries != NULL) {
				(*looper_retries)++;
			}
			continue;
		}
		rc = syscall(SYS_futex, uaddr, TIPSY_FUTEX_WAIT_BITSET_PRIVATE,
			(int)val, &deadline, NULL, 0xffffffffu);
		tipsy_looper_unpark_futex();
		if (tipsy_looper_consume_wake()) {
			(void)tipsy_native_main_idle(0);
			if (looper_retries != NULL) {
				(*looper_retries)++;
			}
			continue;
		}
		if (rc == 0 || errno == EAGAIN) {
			return 0;
		}
		if (errno == EINTR) {
			(void)tipsy_native_main_idle(0);
			continue;
		}
		return -1;
	}
}

static void *test_futex_real_wake_helper(void *arg)
{
	int *word = arg;

	usleep(5000);
	(void)test_futex_wake(word);
	return NULL;
}

static void *test_futex_looper_wake_helper(void *arg)
{
	ALooper *l = arg;

	usleep(5000);
	tipsy_ALooper_wake(l);
	return NULL;
}

static void *test_futex_delayed_real_wake(void *arg)
{
	int *word = arg;

	usleep(35000);
	(void)test_futex_wake(word);
	return NULL;
}

static void test_looper_quiesce(void)
{
	(void)tipsy_looper_consume_wake();
	if (tls_looper != NULL) {
		(void)tipsy_native_main_idle(0);
		(void)tipsy_looper_consume_wake();
	}
}

int tipsy_test_futex_real_wake_vs_looper_wake(void)
{
	ALooper *l;
	pthread_t th;
	pthread_t th2;
	int word;
	int looper_retries = 0;
	int rc;

	l = tipsy_ALooper_prepare(ALOOPER_PREPARE_ALLOW_NON_CALLBACKS);
	if (l == NULL || !l->watcher_on) {
		return -1;
	}
	test_looper_quiesce();

	word = 0;
	if (pthread_create(&th, NULL, test_futex_real_wake_helper, &word) != 0) {
		return -2;
	}
	rc = test_park_poll_futex(&word, 0, &looper_retries);
	pthread_join(th, NULL);
	if (rc != 0) {
		return -3;
	}
	if (looper_retries != 0) {
		return -4;
	}

	test_looper_quiesce();
	word = 0;
	looper_retries = 0;
	if (pthread_create(&th, NULL, test_futex_looper_wake_helper, l) != 0) {
		return -5;
	}
	if (pthread_create(&th2, NULL, test_futex_delayed_real_wake, &word) != 0) {
		(void)test_futex_wake(&word);
		pthread_join(th, NULL);
		return -6;
	}
	rc = test_park_poll_futex(&word, 0, &looper_retries);
	pthread_join(th, NULL);
	pthread_join(th2, NULL);
	if (rc != 0) {
		return -7;
	}
	if (looper_retries < 1) {
		return -8;
	}
	return 1;
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


static __thread pid_t tls_tid;
static pthread_once_t gettid_once = PTHREAD_ONCE_INIT;
static int gettid_atfork_ok;

static void tipsy_gettid_atfork_child(void)
{
	tls_tid = 0;
}

static void tipsy_gettid_init(void)
{
	gettid_atfork_ok = pthread_atfork(NULL, NULL, tipsy_gettid_atfork_child) == 0;
}

int32_t tipsy_gettid(void)
{
	pid_t tid = tls_tid;
	if (tid != 0) {
		return (int32_t)tid;
	}
	pthread_once(&gettid_once, tipsy_gettid_init);
	tid = (pid_t)syscall(SYS_gettid);
	if (gettid_atfork_ok) {
		tls_tid = tid;
	}
	return (int32_t)tid;
}

int32_t tipsy_test_gettid_sys(void)
{
	return (int32_t)syscall(SYS_gettid);
}

int tipsy_test_gettid_same_thread(void)
{
	int32_t sys = (int32_t)syscall(SYS_gettid);
	int32_t a = tipsy_gettid();
	int32_t b = tipsy_gettid();
	if (a != sys || b != sys || a <= 0) {
		return -1;
	}
	return 0;
}

struct gettid_probe {
	int32_t wrap;
	int32_t sys;
};

static void *tipsy_test_gettid_thread(void *arg)
{
	struct gettid_probe *p = arg;
	p->wrap = tipsy_gettid();
	p->sys = (int32_t)syscall(SYS_gettid);
	return NULL;
}

int tipsy_test_gettid_two_threads(void)
{
	struct gettid_probe a = {0};
	struct gettid_probe b = {0};
	pthread_t ta, tb;

	if (pthread_create(&ta, NULL, tipsy_test_gettid_thread, &a) != 0) {
		return -1;
	}
	if (pthread_create(&tb, NULL, tipsy_test_gettid_thread, &b) != 0) {
		return -2;
	}
	pthread_join(ta, NULL);
	pthread_join(tb, NULL);
	if (a.wrap != a.sys || b.wrap != b.sys || a.wrap <= 0 || b.wrap <= 0) {
		return -3;
	}
	if (a.wrap == b.wrap) {
		return -4;
	}
	return 0;
}

int tipsy_test_gettid_atfork_child(void)
{
	int32_t parent_wrap = tipsy_gettid();
	int32_t parent_sys = (int32_t)syscall(SYS_gettid);
	pid_t pid;
	int st = 0;

	if (parent_wrap != parent_sys || parent_wrap <= 0) {
		return -1;
	}
	pid = fork();
	if (pid < 0) {
		return -2;
	}
	if (pid == 0) {
		int32_t child_wrap = tipsy_gettid();
		int32_t child_sys = (int32_t)syscall(SYS_gettid);
		_exit((child_wrap == child_sys && child_wrap != parent_wrap && child_wrap > 0) ? 0 : 1);
	}
	if (waitpid(pid, &st, 0) != pid) {
		return -3;
	}
	if (!WIFEXITED(st) || WEXITSTATUS(st) != 0) {
		return -4;
	}
	if (tipsy_gettid() != parent_wrap) {
		return -5;
	}
	return 0;
}

int64_t tipsy_test_gettid_ns(int n, int cached)
{
	struct timespec a, b;
	int i;

	if (n < 0) {
		return -1;
	}
	clock_gettime(CLOCK_MONOTONIC, &a);
	if (cached) {
		for (i = 0; i < n; i++) {
			(void)tipsy_gettid();
		}
	} else {
		for (i = 0; i < n; i++) {
			(void)syscall(SYS_gettid);
		}
	}
	clock_gettime(CLOCK_MONOTONIC, &b);
	return (int64_t)(b.tv_sec - a.tv_sec) * 1000000000LL + (int64_t)(b.tv_nsec - a.tv_nsec);
}
