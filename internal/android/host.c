/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Host glibc/Mesa dlsym helpers and EGL window-surface translation.
 */
#include "android_bridge.h"
#include "../graphics/guest_swap.h"

#include <dlfcn.h>
#include <link.h>
#include <pthread.h>
#include <string.h>
#include <stdlib.h>
#include <stdint.h>
#include <stdio.h>
#include <stdatomic.h>
#include <time.h>

extern void GoAndroid_LogMissing(char *name);

static void *lib_libc;
static void *lib_libm;
static void *lib_libz;
static void *lib_egl;
static void *lib_gles;
static pthread_once_t host_abi_once = PTHREAD_ONCE_INIT;
static pthread_once_t host_egl_once = PTHREAD_ONCE_INIT;

typedef void *EGLDisplay;
typedef void *EGLConfig;
typedef void *EGLSurface;
typedef int32_t EGLint;
typedef uint32_t EGLBoolean;

typedef EGLSurface (*egl_create_window_surface_fn)(EGLDisplay, EGLConfig, void *, const EGLint *);
typedef void *(*egl_get_proc_address_fn)(const char *);
typedef EGLBoolean (*egl_swap_interval_fn)(EGLDisplay, EGLint);
typedef EGLBoolean (*egl_swap_buffers_fn)(EGLDisplay, EGLSurface);
typedef EGLBoolean (*egl_destroy_surface_fn)(EGLDisplay, EGLSurface);
typedef EGLint (*egl_get_error_fn)(void);

static egl_create_window_surface_fn host_eglCreateWindowSurface;
static egl_get_proc_address_fn host_eglGetProcAddress;
static egl_swap_interval_fn host_eglSwapInterval;
static egl_swap_buffers_fn host_eglSwapBuffers;
static egl_destroy_surface_fn host_eglDestroySurface;
static egl_get_error_fn host_eglGetError;
static _Atomic int egl_vsync_enabled;
/* Default off. Go enables this when the 2s graphics Info logger will emit. */
static _Atomic int egl_present_stats_enabled;
static _Atomic uint64_t egl_successful_swaps;
static _Atomic uint64_t egl_first_swap_ns;
static _Atomic uint64_t egl_last_swap_ns;

#define TIPSY_EGL_FALSE ((EGLBoolean)0)
#define TIPSY_EGL_TRUE ((EGLBoolean)1)
#define TIPSY_EGL_SUCCESS ((EGLint)0x3000)

/* guest_swap.go lives in the optional graphics package. The Android ABI layer
 * must remain independently test-linkable, so absent graphics exports are a
 * normal no-op rather than a linker error. */
#pragma weak GoEGLGuestSurfaceCreated
#pragma weak GoEGLGuestSwap
#pragma weak GoEGLGuestSurfaceDestroyed

typedef uint64_t (*tipsy_egl_guest_surface_created_fn)(uintptr_t, uintptr_t, uintptr_t);
typedef int (*tipsy_egl_guest_swap_fn)(uintptr_t, uintptr_t, uintptr_t, uint64_t);
typedef void (*tipsy_egl_guest_surface_destroyed_fn)(uintptr_t, uintptr_t, uintptr_t, uint64_t);

struct tipsy_egl_guest_surface {
	uintptr_t window;
	uintptr_t display;
	uintptr_t surface;
	uint64_t generation;
	/* A retained entry has two independent duties: a one-shot boot handoff and
	 * its eventual lifecycle destroy.  Do not discard the entry after graphics
	 * accepts the handoff, or numeric EGL handle reuse could lose the destroy
	 * generation. */
	int handoff_state;
	struct tipsy_egl_guest_surface *next;
};

enum {
	TIPSY_EGL_GUEST_HANDOFF_PENDING = 0,
	TIPSY_EGL_GUEST_HANDOFF_INFLIGHT = 1,
	TIPSY_EGL_GUEST_HANDOFF_COMPLETE = 2,
};

static pthread_mutex_t egl_guest_surfaces_mu = PTHREAD_MUTEX_INITIALIZER;
static struct tipsy_egl_guest_surface *egl_guest_surfaces;
/* The common post-boot path has no pending guest handoff.  This count lets a
 * successful host swap avoid taking the registry mutex or crossing C-to-Go
 * merely to rediscover that completed state. */
static _Atomic uint64_t egl_guest_pending_handoffs;

static uint64_t tipsy_egl_guest_surface_created(uintptr_t window, uintptr_t display,
	uintptr_t surface)
{
	if (GoEGLGuestSurfaceCreated == NULL) {
		return 0;
	}
	return GoEGLGuestSurfaceCreated(window, display, surface);
}

static int tipsy_egl_guest_swap(uintptr_t window, uintptr_t display, uintptr_t surface,
	uint64_t generation)
{
	if (GoEGLGuestSwap == NULL) {
		return 0;
	}
	return GoEGLGuestSwap(window, display, surface, generation);
}

static void tipsy_egl_guest_surface_destroyed(uintptr_t window, uintptr_t display,
	uintptr_t surface, uint64_t generation)
{
	if (GoEGLGuestSurfaceDestroyed != NULL) {
		GoEGLGuestSurfaceDestroyed(window, display, surface, generation);
	}
}

static tipsy_egl_guest_surface_created_fn egl_guest_surface_created_fn =
	tipsy_egl_guest_surface_created;
static tipsy_egl_guest_swap_fn egl_guest_swap_fn = tipsy_egl_guest_swap;
static tipsy_egl_guest_surface_destroyed_fn egl_guest_surface_destroyed_fn =
	tipsy_egl_guest_surface_destroyed;

/* All callbacks above are external (and can re-enter this shim), so the
 * registry lock only protects local identity storage. A swap/destroy takes a
 * value snapshot, drops the lock, and then calls graphics. Graphics performs
 * the final generation check, which makes a destroy racing that callback a
 * harmless rejected stale signal rather than a use-after-free. */
static struct tipsy_egl_guest_surface *tipsy_egl_guest_surface_take(uintptr_t display,
	uintptr_t surface)
{
	struct tipsy_egl_guest_surface **cursor;
	struct tipsy_egl_guest_surface *found = NULL;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (cursor = &egl_guest_surfaces; *cursor != NULL; cursor = &(*cursor)->next) {
		if ((*cursor)->display == display && (*cursor)->surface == surface) {
			found = *cursor;
			*cursor = found->next;
			found->next = NULL;
			if (found->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
				atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
					memory_order_release);
			}
			break;
		}
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	return found;
}

static void tipsy_egl_guest_surface_discard_window(uintptr_t window)
{
	struct tipsy_egl_guest_surface **cursor;
	struct tipsy_egl_guest_surface *discarded = NULL;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (cursor = &egl_guest_surfaces; *cursor != NULL;) {
		struct tipsy_egl_guest_surface *entry = *cursor;
		if (entry->window != window) {
			cursor = &entry->next;
			continue;
		}
		*cursor = entry->next;
		entry->next = discarded;
		discarded = entry;
		if (entry->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
			atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
		}
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	while (discarded != NULL) {
		struct tipsy_egl_guest_surface *next = discarded->next;
		free(discarded);
		discarded = next;
	}
}

static void tipsy_egl_guest_surface_store(struct tipsy_egl_guest_surface *entry)
{
	struct tipsy_egl_guest_surface **cursor;
	struct tipsy_egl_guest_surface *old = NULL;

	if (entry == NULL) {
		return;
	}
	/* Graphics allows one active guest surface per XID. A new surface for that
	 * window therefore invalidates all prior Android records, including a host
	 * address the driver later recycles. */
	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (cursor = &egl_guest_surfaces; *cursor != NULL;) {
		struct tipsy_egl_guest_surface *candidate = *cursor;
		if (candidate->window != entry->window) {
			cursor = &candidate->next;
			continue;
		}
		*cursor = candidate->next;
		candidate->next = old;
		old = candidate;
		if (candidate->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
			atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
		}
	}
	entry->next = egl_guest_surfaces;
	egl_guest_surfaces = entry;
	if (entry->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
		atomic_fetch_add_explicit(&egl_guest_pending_handoffs, 1,
			memory_order_release);
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	while (old != NULL) {
		struct tipsy_egl_guest_surface *next = old->next;
		free(old);
		old = next;
	}
}

/* Claim at most one pending signal before entering Go.  The in-flight state
 * suppresses a concurrent successful swap; if graphics rejects the signal,
 * finish below restores this exact still-live surface to pending. */
static int tipsy_egl_guest_surface_claim(uintptr_t display, uintptr_t surface,
	struct tipsy_egl_guest_surface *out)
{
	struct tipsy_egl_guest_surface *entry;
	int found = 0;

	if (out == NULL) {
		return 0;
	}
	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (entry = egl_guest_surfaces; entry != NULL; entry = entry->next) {
		if (entry->display == display && entry->surface == surface &&
		    entry->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
			entry->handoff_state = TIPSY_EGL_GUEST_HANDOFF_INFLIGHT;
			atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
			*out = *entry;
			out->next = NULL;
			found = 1;
			break;
		}
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	return found;
}

static void tipsy_egl_guest_surface_finish(uintptr_t display, uintptr_t surface,
	uint64_t generation, int accepted)
{
	struct tipsy_egl_guest_surface *entry;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (entry = egl_guest_surfaces; entry != NULL; entry = entry->next) {
		if (entry->display != display || entry->surface != surface ||
		    entry->generation != generation ||
		    entry->handoff_state != TIPSY_EGL_GUEST_HANDOFF_INFLIGHT) {
			continue;
		}
		if (accepted) {
			entry->handoff_state = TIPSY_EGL_GUEST_HANDOFF_COMPLETE;
		} else {
			entry->handoff_state = TIPSY_EGL_GUEST_HANDOFF_PENDING;
			atomic_fetch_add_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
		}
		break;
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
}

static void tipsy_egl_guest_surface_clear_all(void)
{
	struct tipsy_egl_guest_surface *entry;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	entry = egl_guest_surfaces;
	egl_guest_surfaces = NULL;
	atomic_store_explicit(&egl_guest_pending_handoffs, 0, memory_order_release);
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	while (entry != NULL) {
		struct tipsy_egl_guest_surface *next = entry->next;
		free(entry);
		entry = next;
	}
}

extern void GoAndroid_LogEGLSwapInterval(int enabled, int requested, int effective,
	int primary_ok, int primary_error, int fallback_ok, int fallback_error);

static void *open_lib(const char *name)
{
	return dlopen(name, RTLD_NOW | RTLD_LOCAL);
}

static void open_host_abi_libraries(void)
{
	lib_libc = open_lib("libc.so.6");
	lib_libm = open_lib("libm.so.6");
	lib_libz = open_lib("libz.so.1");
}

/* Resolve from one exact host ELF object. dlsym(handle, ...) also searches an
 * object's dependencies, so confirm the returned address belongs to the
 * requested object's own link_map before treating it as an owned Android ABI
 * capability. */
void *tipsy_host_dlsym_library(const char *lib, const char *name)
{
	void *handle = NULL;
	void *p;
	Dl_info info;
	struct link_map *map = NULL;

	if (lib == NULL || name == NULL) {
		return NULL;
	}
	pthread_once(&host_abi_once, open_host_abi_libraries);
	if (strcmp(lib, "libc.so") == 0) {
		handle = lib_libc;
	} else if (strcmp(lib, "libm.so") == 0) {
		handle = lib_libm;
	} else if (strcmp(lib, "libz.so") == 0) {
		handle = lib_libz;
	}
	if (handle == NULL) {
		return NULL;
	}
	p = dlsym(handle, name);
	if (p == NULL || dlinfo(handle, RTLD_DI_LINKMAP, &map) != 0 || map == NULL ||
	    dladdr(p, &info) == 0 || info.dli_fbase != (void *)map->l_addr) {
		return NULL;
	}
	return p;
}

void *tipsy_host_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	p = dlsym(RTLD_DEFAULT, name);
	if (p != NULL) {
		return p;
	}
	pthread_once(&host_abi_once, open_host_abi_libraries);
	if (lib_libc != NULL) {
		p = dlsym(lib_libc, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_libm != NULL) {
		p = dlsym(lib_libm, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_libz != NULL) {
		p = dlsym(lib_libz, name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

/* One-time EGL/GLES host library resolution. pthread_once caches both success
 * and failure, so a missing host EGL is not retried from every engine swap. */
static _Atomic int egl_init_calls;

static void open_egl_libraries(void)
{
	atomic_fetch_add_explicit(&egl_init_calls, 1, memory_order_relaxed);
	lib_egl = open_lib("libEGL.so.1");
	if (lib_egl == NULL) {
		lib_egl = open_lib("libEGL.so");
	}
	lib_gles = open_lib("libGLESv2.so.2");
	if (lib_gles == NULL) {
		lib_gles = open_lib("libGLESv2.so");
	}
	if (lib_egl != NULL) {
		host_eglCreateWindowSurface = (egl_create_window_surface_fn)dlsym(lib_egl, "eglCreateWindowSurface");
		host_eglGetProcAddress = (egl_get_proc_address_fn)dlsym(lib_egl, "eglGetProcAddress");
		host_eglSwapInterval = (egl_swap_interval_fn)dlsym(lib_egl, "eglSwapInterval");
		host_eglSwapBuffers = (egl_swap_buffers_fn)dlsym(lib_egl, "eglSwapBuffers");
		host_eglDestroySurface = (egl_destroy_surface_fn)dlsym(lib_egl, "eglDestroySurface");
		host_eglGetError = (egl_get_error_fn)dlsym(lib_egl, "eglGetError");
	}
}

static void ensure_egl(void)
{
	pthread_once(&host_egl_once, open_egl_libraries);
}

static void tipsy_egl_record_successful_swap(uint64_t now_ns)
{
	uint64_t expected = 0;
	atomic_compare_exchange_strong_explicit(&egl_first_swap_ns, &expected, now_ns,
		memory_order_acq_rel, memory_order_acquire);
	atomic_store_explicit(&egl_last_swap_ns, now_ns, memory_order_release);
	atomic_fetch_add_explicit(&egl_successful_swaps, 1, memory_order_acq_rel);
}

static uint64_t tipsy_monotonic_ns(void)
{
	struct timespec ts;
	if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
		return 0;
	}
	return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

static void tipsy_egl_note_successful_swap(void)
{
	uint64_t now_ns;

	if (!atomic_load_explicit(&egl_present_stats_enabled, memory_order_relaxed)) {
		return;
	}
	now_ns = tipsy_monotonic_ns();
	if (now_ns != 0) {
		tipsy_egl_record_successful_swap(now_ns);
	}
}

void tipsy_egl_set_present_stats(int enabled)
{
	atomic_store_explicit(&egl_present_stats_enabled, enabled != 0, memory_order_relaxed);
}

int tipsy_egl_present_stats_enabled(void)
{
	return atomic_load_explicit(&egl_present_stats_enabled, memory_order_relaxed);
}

void tipsy_egl_reset_swap_stats(void)
{
	atomic_store_explicit(&egl_successful_swaps, 0, memory_order_release);
	atomic_store_explicit(&egl_first_swap_ns, 0, memory_order_release);
	atomic_store_explicit(&egl_last_swap_ns, 0, memory_order_release);
}

void tipsy_egl_swap_stats(uint64_t *successful_swaps, uint64_t *first_ns,
	uint64_t *last_ns)
{
	if (successful_swaps != NULL) {
		*successful_swaps = atomic_load_explicit(&egl_successful_swaps, memory_order_acquire);
	}
	if (first_ns != NULL) {
		*first_ns = atomic_load_explicit(&egl_first_swap_ns, memory_order_acquire);
	}
	if (last_ns != NULL) {
		*last_ns = atomic_load_explicit(&egl_last_swap_ns, memory_order_acquire);
	}
}

void tipsy_test_egl_record_swap(uint64_t now_ns)
{
	tipsy_egl_record_successful_swap(now_ns);
}

void tipsy_test_egl_note_successful_swap(void)
{
	tipsy_egl_note_successful_swap();
}

void tipsy_egl_set_vsync(int enabled)
{
	atomic_store_explicit(&egl_vsync_enabled, enabled != 0, memory_order_release);
}

int tipsy_egl_vsync_enabled(void)
{
	return atomic_load_explicit(&egl_vsync_enabled, memory_order_acquire);
}

EGLBoolean tipsy_eglSwapInterval(EGLDisplay dpy, EGLint interval)
{
	EGLBoolean primary_ok;
	EGLBoolean fallback_ok = TIPSY_EGL_FALSE;
	EGLint primary_error;
	EGLint fallback_error = TIPSY_EGL_SUCCESS;
	int vsync;
	EGLint desired;

	ensure_egl();
	if (host_eglSwapInterval == NULL) {
		GoAndroid_LogMissing("eglSwapInterval");
		return TIPSY_EGL_FALSE;
	}
	vsync = tipsy_egl_vsync_enabled();
	desired = vsync ? 1 : 0;
	primary_ok = host_eglSwapInterval(dpy, desired);
	if (primary_ok == TIPSY_EGL_TRUE) {
		/* A new client presentation epoch (notably the transition from
		 * Landing to an experience surface) owns a fresh rate window. */
		tipsy_egl_reset_swap_stats();
		GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 1,
			TIPSY_EGL_SUCCESS, 0, TIPSY_EGL_SUCCESS);
		return TIPSY_EGL_TRUE;
	}

	if (desired == interval) {
		/* Do not consume a failed passthrough call's thread-local EGL error;
		 * the official client owns the next eglGetError. */
		GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 0, 0, 0,
			TIPSY_EGL_SUCCESS);
		return TIPSY_EGL_FALSE;
	}

	primary_error = host_eglGetError != NULL ? host_eglGetError() : 0;
	/* Preserve the official client's exact request when the host rejects the
	 * independent VSync policy. */
	fallback_ok = host_eglSwapInterval(dpy, interval);
	/* As with ordinary passthrough failure, leave fallback's EGL error for
	 * the client. Zero means deliberately unconsumed, not EGL_SUCCESS. */
	fallback_error = fallback_ok == TIPSY_EGL_TRUE ? TIPSY_EGL_SUCCESS : 0;
	if (fallback_ok == TIPSY_EGL_TRUE) {
		tipsy_egl_reset_swap_stats();
	}
	GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 0, primary_error,
		fallback_ok == TIPSY_EGL_TRUE, fallback_error);
	return fallback_ok;
}

EGLBoolean tipsy_eglSwapBuffers(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_surface guest;
	EGLBoolean ok;

	ensure_egl();
	if (host_eglSwapBuffers == NULL) {
		GoAndroid_LogMissing("eglSwapBuffers");
		return TIPSY_EGL_FALSE;
	}
	ok = host_eglSwapBuffers(dpy, surface);
	if (ok == TIPSY_EGL_TRUE) {
		tipsy_egl_note_successful_swap();
		if (atomic_load_explicit(&egl_guest_pending_handoffs, memory_order_acquire) != 0 &&
		    tipsy_egl_guest_surface_claim((uintptr_t)dpy, (uintptr_t)surface, &guest)) {
			/* The bridge is deliberately after the host return: a failed client
			 * present never reaches the graphics sentinel. A nonzero response is
			 * one accepted boot handoff; retain the local identity only for the
			 * later generation-checked destroy/replacement lifecycle. */
			int accepted = egl_guest_swap_fn(guest.window, guest.display, guest.surface,
				guest.generation);
			tipsy_egl_guest_surface_finish(guest.display, guest.surface,
				guest.generation, accepted != 0);
		}
	}
	return ok;
}

EGLSurface tipsy_eglCreateWindowSurface(EGLDisplay dpy, EGLConfig config, void *native_window, const EGLint *attrib_list)
{
	struct tipsy_egl_guest_surface *guest;
	EGLSurface surface;
	void *host_win = native_window;
	uintptr_t xid = 0;
	uint64_t generation;

	ensure_egl();
	if (tipsy_is_anative_window(native_window)) {
		xid = tipsy_ANativeWindow_get_handle(native_window);
		host_win = (void *)xid;
	}
	if (host_eglCreateWindowSurface == NULL) {
		GoAndroid_LogMissing("eglCreateWindowSurface");
		return NULL;
	}
	surface = host_eglCreateWindowSurface(dpy, config, host_win, attrib_list);
	if (surface == NULL || xid == 0 || dpy == NULL) {
		return surface;
	}
	/* Reserve local storage before publishing the guest surface. If allocation
	 * fails, leave graphics unnotified: a non-retainable generation must never
	 * later be signaled or destroyed as though it were tracked. */
	guest = calloc(1, sizeof(*guest));
	if (guest == NULL) {
		return surface;
	}
	/* Prevent an old Android record for the same XID from signaling while the
	 * graphics router replaces its surface mapping in the callback below. */
	tipsy_egl_guest_surface_discard_window(xid);
	generation = egl_guest_surface_created_fn(xid, (uintptr_t)dpy, (uintptr_t)surface);
	if (generation == 0) {
		free(guest);
		return surface;
	}
	guest->window = xid;
	guest->display = (uintptr_t)dpy;
	guest->surface = (uintptr_t)surface;
	guest->generation = generation;
	guest->handoff_state = TIPSY_EGL_GUEST_HANDOFF_PENDING;
	tipsy_egl_guest_surface_store(guest);
	return surface;
}

EGLBoolean tipsy_eglDestroySurface(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_surface *guest;
	EGLBoolean ok;

	ensure_egl();
	if (host_eglDestroySurface == NULL) {
		GoAndroid_LogMissing("eglDestroySurface");
		return TIPSY_EGL_FALSE;
	}
	/* Invalidate before forwarding to the host. This means a numeric host
	 * surface reuse cannot revive the old generation, even if destruction and a
	 * successful swap race on separate callers. */
	guest = tipsy_egl_guest_surface_take((uintptr_t)dpy, (uintptr_t)surface);
	if (guest != NULL) {
		egl_guest_surface_destroyed_fn(guest->window, guest->display, guest->surface,
			guest->generation);
		free(guest);
	}
	ok = host_eglDestroySurface(dpy, surface);
	return ok;
}

void *tipsy_eglGetProcAddress(const char *name)
{
	void *p;

	ensure_egl();
	if (name != NULL) {
		if (strcmp(name, "eglCreateWindowSurface") == 0) {
			return (void *)tipsy_eglCreateWindowSurface;
		}
		if (strcmp(name, "eglSwapInterval") == 0) {
			return (void *)tipsy_eglSwapInterval;
		}
		if (strcmp(name, "eglSwapBuffers") == 0) {
			return (void *)tipsy_eglSwapBuffers;
		}
		if (strcmp(name, "eglDestroySurface") == 0) {
			return (void *)tipsy_eglDestroySurface;
		}
		if (strcmp(name, "eglGetProcAddress") == 0) {
			return (void *)tipsy_eglGetProcAddress;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_egl != NULL && name != NULL) {
		p = dlsym(lib_egl, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_gles != NULL && name != NULL) {
		p = dlsym(lib_gles, name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

int tipsy_test_egl_proc_is_wrapped(const char *name)
{
	void *got = tipsy_eglGetProcAddress(name);
	if (name == NULL) {
		return 0;
	}
	if (strcmp(name, "eglSwapInterval") == 0) {
		return got == (void *)tipsy_eglSwapInterval;
	}
	if (strcmp(name, "eglSwapBuffers") == 0) {
		return got == (void *)tipsy_eglSwapBuffers;
	}
	if (strcmp(name, "eglCreateWindowSurface") == 0) {
		return got == (void *)tipsy_eglCreateWindowSurface;
	}
	if (strcmp(name, "eglDestroySurface") == 0) {
		return got == (void *)tipsy_eglDestroySurface;
	}
	return 0;
}

int tipsy_test_egl_init_calls(void)
{
	return atomic_load_explicit(&egl_init_calls, memory_order_relaxed);
}

void *tipsy_egl_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	if (strcmp(name, "eglCreateWindowSurface") == 0) {
		return (void *)tipsy_eglCreateWindowSurface;
	}
	if (strcmp(name, "eglSwapInterval") == 0) {
		return (void *)tipsy_eglSwapInterval;
	}
	if (strcmp(name, "eglSwapBuffers") == 0) {
		return (void *)tipsy_eglSwapBuffers;
	}
	if (strcmp(name, "eglDestroySurface") == 0) {
		return (void *)tipsy_eglDestroySurface;
	}
	if (strcmp(name, "eglGetProcAddress") == 0) {
		return (void *)tipsy_eglGetProcAddress;
	}
	ensure_egl();
	if (lib_egl != NULL) {
		p = dlsym(lib_egl, name);
		if (p != NULL) {
			return p;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

struct tipsy_egl_policy_test {
	int policy_result;
	int policy_error;
	int client_result;
	int client_error;
	int desired;
	int requested;
	int first_interval;
	int second_interval;
	int calls;
	int reported_error;
};

static struct tipsy_egl_policy_test *active_egl_policy_test;

static EGLBoolean tipsy_test_egl_swap_interval(EGLDisplay dpy, EGLint interval)
{
	struct tipsy_egl_policy_test *t = active_egl_policy_test;
	(void)dpy;
	if (t == NULL) {
		return TIPSY_EGL_FALSE;
	}
	if (t->calls == 0) {
		t->first_interval = interval;
	} else if (t->calls == 1) {
		t->second_interval = interval;
	}
	t->calls++;
	if (interval == t->desired) {
		t->reported_error = t->policy_error;
		return t->policy_result ? TIPSY_EGL_TRUE : TIPSY_EGL_FALSE;
	}
	t->reported_error = t->client_error;
	return t->client_result ? TIPSY_EGL_TRUE : TIPSY_EGL_FALSE;
}

static EGLint tipsy_test_egl_get_error(void)
{
	EGLint result;
	if (active_egl_policy_test == NULL) {
		return 0;
	}
	result = active_egl_policy_test->reported_error;
	active_egl_policy_test->reported_error = 0;
	return result;
}

int tipsy_test_egl_swap_interval_policy(int vsync, int requested,
	int policy_result, int policy_error, int client_result, int client_error,
	int *first_interval, int *second_interval, int *calls, int *reported_error)
{
	struct tipsy_egl_policy_test t = {0};
	egl_swap_interval_fn saved_swap;
	egl_get_error_fn saved_error;
	EGLBoolean result;

	/* Complete one-time host resolution before injecting the test doubles. */
	ensure_egl();
	t.policy_result = policy_result;
	t.policy_error = policy_error;
	t.client_result = client_result;
	t.client_error = client_error;
	t.desired = vsync ? 1 : 0;
	t.requested = requested;
	saved_swap = host_eglSwapInterval;
	saved_error = host_eglGetError;
	active_egl_policy_test = &t;
	host_eglSwapInterval = tipsy_test_egl_swap_interval;
	host_eglGetError = tipsy_test_egl_get_error;
	tipsy_egl_set_vsync(vsync);
	result = tipsy_eglSwapInterval((EGLDisplay)(uintptr_t)1, requested);
	host_eglSwapInterval = saved_swap;
	host_eglGetError = saved_error;
	active_egl_policy_test = NULL;
	if (first_interval != NULL) {
		*first_interval = t.first_interval;
	}
	if (second_interval != NULL) {
		*second_interval = t.second_interval;
	}
	if (calls != NULL) {
		*calls = t.calls;
	}
	if (reported_error != NULL) {
		*reported_error = t.reported_error;
	}
	return result == TIPSY_EGL_TRUE;
}

struct tipsy_egl_guest_handoff_test {
	EGLSurface next_surface;
	EGLBoolean swap_result;
	EGLBoolean destroy_result;
	int guest_swap_result;
	uint64_t next_generation;
	int reenter_created;
	int reenter_destroyed;
	int create_calls;
	uintptr_t create_host_windows[4];
	int swap_calls;
	int destroy_calls;
	int destroy_callback_order_violations;
	uint32_t callback_count;
	TipsyEGLGuestHandoffEvent callbacks[6];
};

static struct tipsy_egl_guest_handoff_test *active_egl_guest_handoff_test;

static void tipsy_test_egl_guest_handoff_record(uint32_t kind, uintptr_t window,
	uintptr_t display, uintptr_t surface, uint64_t generation)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	TipsyEGLGuestHandoffEvent *event;

	if (t == NULL || t->callback_count >= sizeof(t->callbacks) / sizeof(t->callbacks[0])) {
		return;
	}
	event = &t->callbacks[t->callback_count++];
	event->kind = kind;
	event->window = window;
	event->display = display;
	event->surface = surface;
	event->generation = generation;
}

static EGLSurface tipsy_test_egl_guest_handoff_create(EGLDisplay dpy, EGLConfig config,
	void *host_window, const EGLint *attrib_list)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	(void)dpy;
	(void)config;
	(void)attrib_list;
	if (t == NULL) {
		return NULL;
	}
	if (t->create_calls < (int)(sizeof(t->create_host_windows) /
		sizeof(t->create_host_windows[0]))) {
		t->create_host_windows[t->create_calls] = (uintptr_t)host_window;
	}
	t->create_calls++;
	return t->next_surface;
}

static EGLBoolean tipsy_test_egl_guest_handoff_swap(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	(void)dpy;
	(void)surface;
	if (t == NULL) {
		return TIPSY_EGL_FALSE;
	}
	t->swap_calls++;
	return t->swap_result;
}

static EGLBoolean tipsy_test_egl_guest_handoff_destroy(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	(void)dpy;
	(void)surface;
	if (t == NULL) {
		return TIPSY_EGL_FALSE;
	}
	if ((uintptr_t)dpy == 0x1001 && (uintptr_t)surface == 0x3001 &&
		(t->callback_count == 0 ||
		 t->callbacks[t->callback_count - 1].kind != TIPSY_EGL_GUEST_HANDOFF_DESTROYED)) {
		t->destroy_callback_order_violations++;
	}
	t->destroy_calls++;
	return t->destroy_result;
}

static uint64_t tipsy_test_egl_guest_handoff_created(uintptr_t window, uintptr_t display,
	uintptr_t surface)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	uint64_t generation;

	if (t == NULL) {
		return 0;
	}
	tipsy_test_egl_guest_handoff_record(TIPSY_EGL_GUEST_HANDOFF_CREATED, window,
		display, surface, t->next_generation);
	generation = t->next_generation;
	if (t->reenter_created) {
		t->reenter_created = 0;
		/* Creation has not retained an Android generation yet. An external
		 * callback may re-enter the shim, but that early successful host swap
		 * must not be guessed as a guest signal. */
		(void)tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface);
	}
	return generation;
}

static int tipsy_test_egl_guest_handoff_swap_callback(uintptr_t window, uintptr_t display,
	uintptr_t surface, uint64_t generation)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	tipsy_test_egl_guest_handoff_record(TIPSY_EGL_GUEST_HANDOFF_SWAP, window,
		display, surface, generation);
	return t != NULL ? t->guest_swap_result : 0;
}

static void tipsy_test_egl_guest_handoff_destroyed(uintptr_t window, uintptr_t display,
	uintptr_t surface, uint64_t generation)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;

	tipsy_test_egl_guest_handoff_record(TIPSY_EGL_GUEST_HANDOFF_DESTROYED, window,
		display, surface, generation);
	if (t != NULL && t->reenter_destroyed) {
		t->reenter_destroyed = 0;
		/* The entry was removed before this external callback, so a reentrant
		 * successful host swap is stale and must not produce a second signal. */
		(void)tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface);
	}
}

static EGLSurface tipsy_test_egl_guest_handoff_create_window(uintptr_t display,
	uintptr_t xid)
{
	TipsyNativeWindow window = {0};

	window.magic = TIPSY_ANW_MAGIC;
	window.native_handle = xid;
	return tipsy_eglCreateWindowSurface((EGLDisplay)display, NULL, &window, NULL);
}

int tipsy_test_egl_guest_handoff_fixture(TipsyEGLGuestHandoffFixture *out)
{
	const uintptr_t display = 0x1001;
	const uintptr_t other_display = 0x1002;
	const uintptr_t xid = 0x2001;
	const uintptr_t surface = 0x3001;
	const uintptr_t other_surface = 0x3002;
	const uint64_t first_generation = 41;
	const uint64_t second_generation = 42;
	struct tipsy_egl_guest_handoff_test t = {0};
	egl_create_window_surface_fn saved_create;
	egl_swap_buffers_fn saved_swap;
	egl_destroy_surface_fn saved_destroy;
	tipsy_egl_guest_surface_created_fn saved_created;
	tipsy_egl_guest_swap_fn saved_guest_swap;
	tipsy_egl_guest_surface_destroyed_fn saved_destroyed;
	int passed = 1;

	if (out == NULL) {
		return 0;
	}
	memset(out, 0, sizeof(*out));
	/* Complete host resolution before substituting every direct shim edge. */
	ensure_egl();
	tipsy_egl_guest_surface_clear_all();
	saved_create = host_eglCreateWindowSurface;
	saved_swap = host_eglSwapBuffers;
	saved_destroy = host_eglDestroySurface;
	saved_created = egl_guest_surface_created_fn;
	saved_guest_swap = egl_guest_swap_fn;
	saved_destroyed = egl_guest_surface_destroyed_fn;
	active_egl_guest_handoff_test = &t;
	host_eglCreateWindowSurface = tipsy_test_egl_guest_handoff_create;
	host_eglSwapBuffers = tipsy_test_egl_guest_handoff_swap;
	host_eglDestroySurface = tipsy_test_egl_guest_handoff_destroy;
	egl_guest_surface_created_fn = tipsy_test_egl_guest_handoff_created;
	egl_guest_swap_fn = tipsy_test_egl_guest_handoff_swap_callback;
	egl_guest_surface_destroyed_fn = tipsy_test_egl_guest_handoff_destroyed;

	/* A failed host create cannot register a generation. */
	t.next_surface = NULL;
	if (tipsy_test_egl_guest_handoff_create_window(display, xid) != NULL) {
		passed = 0;
	}

	/* First exact surface: creation callback re-enters a successful swap before
	 * local retention. The reentrant call is intentionally not signaled. */
	t.next_surface = (EGLSurface)surface;
	t.next_generation = first_generation;
	t.swap_result = TIPSY_EGL_TRUE;
	t.guest_swap_result = 1;
	t.reenter_created = 1;
	if (tipsy_test_egl_guest_handoff_create_window(display, xid) != (EGLSurface)surface) {
		passed = 0;
	}
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}
	/* The first accepted boot handoff remains retained for destroy, but a
	 * second successful guest swap must stay entirely in C. */
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE ||
		t.callback_count != 2) {
		passed = 0;
	}
	/* A failed host swap, a different surface, and a different display never
	 * cross the bridge. */
	t.swap_result = TIPSY_EGL_FALSE;
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_FALSE) {
		passed = 0;
	}
	t.swap_result = TIPSY_EGL_TRUE;
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)other_surface) != TIPSY_EGL_TRUE ||
		tipsy_eglSwapBuffers((EGLDisplay)other_display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	/* Destroy removes the entry before invoking the external callback. Its
	 * reentrant swap and a post-destroy swap are both stale. */
	t.destroy_result = TIPSY_EGL_TRUE;
	t.reenter_destroyed = 1;
	if (tipsy_eglDestroySurface((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	/* A non-Tipsy/zero-XID native window is unwrapped for the host but never
	 * registered. It cannot manufacture a bridge signal. */
	t.next_surface = (EGLSurface)other_surface;
	t.next_generation = first_generation;
	if (tipsy_test_egl_guest_handoff_create_window(display, 0) != (EGLSurface)other_surface ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)other_surface) != TIPSY_EGL_TRUE ||
		tipsy_eglDestroySurface((EGLDisplay)display, (EGLSurface)other_surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	/* Recreate the exact numeric host surface with a new generation. A destroy
	 * callback is required before the host call even when that host call fails. */
	t.next_surface = (EGLSurface)surface;
	t.next_generation = second_generation;
	/* Graphics reports -1 when this exact surface was already retired. The C
	 * gate treats every nonzero reply as handoff-complete, retaining only the
	 * generation for the later destroy callback. */
	t.guest_swap_result = -1;
	t.reenter_created = 1;
	if (tipsy_test_egl_guest_handoff_create_window(display, xid) != (EGLSurface)surface ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE ||
		t.callback_count != 5) {
		passed = 0;
	}
	t.destroy_result = TIPSY_EGL_FALSE;
	t.reenter_destroyed = 1;
	if (tipsy_eglDestroySurface((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_FALSE ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	if (t.create_calls != 4 || t.create_host_windows[0] != xid ||
		t.create_host_windows[1] != xid || t.create_host_windows[2] != 0 ||
		t.create_host_windows[3] != xid || t.destroy_callback_order_violations != 0 ||
		t.callback_count != 6) {
		passed = 0;
	}
	if (t.callback_count == 6 &&
		(t.callbacks[0].kind != TIPSY_EGL_GUEST_HANDOFF_CREATED ||
		 t.callbacks[1].kind != TIPSY_EGL_GUEST_HANDOFF_SWAP ||
		 t.callbacks[2].kind != TIPSY_EGL_GUEST_HANDOFF_DESTROYED ||
		 t.callbacks[3].kind != TIPSY_EGL_GUEST_HANDOFF_CREATED ||
		 t.callbacks[4].kind != TIPSY_EGL_GUEST_HANDOFF_SWAP ||
		 t.callbacks[5].kind != TIPSY_EGL_GUEST_HANDOFF_DESTROYED ||
		 t.callbacks[0].generation != first_generation ||
		 t.callbacks[1].generation != first_generation ||
		 t.callbacks[2].generation != first_generation ||
		 t.callbacks[3].generation != second_generation ||
		 t.callbacks[4].generation != second_generation ||
		 t.callbacks[5].generation != second_generation)) {
		passed = 0;
	}

	out->create_calls = t.create_calls;
	memcpy(out->create_host_windows, t.create_host_windows, sizeof(out->create_host_windows));
	out->swap_calls = t.swap_calls;
	out->destroy_calls = t.destroy_calls;
	out->destroy_callback_order_violations = t.destroy_callback_order_violations;
	out->callback_count = t.callback_count;
	memcpy(out->callbacks, t.callbacks, sizeof(out->callbacks));
	out->passed = passed;
	tipsy_egl_guest_surface_clear_all();
	egl_guest_surface_created_fn = saved_created;
	egl_guest_swap_fn = saved_guest_swap;
	egl_guest_surface_destroyed_fn = saved_destroyed;
	host_eglCreateWindowSurface = saved_create;
	host_eglSwapBuffers = saved_swap;
	host_eglDestroySurface = saved_destroy;
	active_egl_guest_handoff_test = NULL;
	return passed;
}

void *tipsy_gles_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	ensure_egl();
	if (lib_gles != NULL) {
		p = dlsym(lib_gles, name);
		if (p != NULL) {
			return p;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}
